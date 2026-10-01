//go:build integration && teams_workflows

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

const teamsProfile = "teams-workflows-channel"

type teamsMetadata struct {
	WorkflowOrigin        string `json:"workflowOrigin"`
	ChannelType           string `json:"channelType"`
	OwnershipAcknowledged bool   `json:"ownershipAcknowledged"`
}

type teamsDestination struct {
	Name string `json:"name"`
	teamsMetadata
}

type teamsConnection struct {
	connection
	Teams teamsMetadata
}

type teamsPreview struct {
	preview
	Destination teamsDestination
}

type teamsDelivery struct {
	delivery
	Destination         teamsDestination
	OutboundAttemptedAt *time.Time
}

type teamsReply struct {
	Connection teamsConnection
	Preview    teamsPreview
	Delivery   teamsDelivery
}

func teamsInput(workflowURL string) object {
	return object{
		"profile": teamsProfile, "name": "Owned standard channel", "enabled": true, "workflowUrl": workflowURL,
		"teams": object{"channelType": "standard", "ownershipAcknowledged": true},
	}
}

func (h *harness) rememberTeamsURL(value string) {
	h.t.Helper()
	u, err := url.Parse(value)
	must(h.t, "parse synthetic Workflow URL", err)
	for _, part := range []string{value, u.Path, u.EscapedPath(), u.RawQuery} {
		h.remember(part)
	}
	query, err := url.ParseQuery(u.RawQuery)
	must(h.t, "parse synthetic signed query", err)
	for _, pair := range strings.Split(u.RawQuery, "&") {
		_, raw, present := strings.Cut(pair, "=")
		if present {
			h.remember(raw)
		}
	}
	for _, values := range query {
		for _, part := range values {
			h.remember(part)
			h.remember(hex.EncodeToString([]byte(part)))
		}
	}
}

func (h *harness) teamsJSON(who actor, method, path string, input any, status int) teamsReply {
	h.t.Helper()
	body, _ := h.request(h.ctx, who, method, path, input, status)
	root := decoded[object](h.t, body)
	check(h.t, root["apiVersion"] == app.APIVersion, "Teams envelope lost API version")
	for _, kind := range []string{"connection", "preview", "delivery"} {
		value, present := root[kind]
		if !present {
			continue
		}
		keys := []string{"apiVersion", kind}
		if _, present := root["dataOrigin"]; present {
			keys = append(keys, "dataOrigin")
		}
		exactKeys(h.t, "Teams resource envelope", root, keys...)
		item, ok := value.(map[string]any)
		check(h.t, ok && item["profile"] == teamsProfile, "Teams resource is not its selected typed profile")
		switch kind {
		case "connection":
			exactKeys(h.t, "Teams connection", item, "id", "workspaceId", "profile", "name", "channel", "enabled",
				"credentialConfigured", "revision", "createdAt", "updatedAt", "teams", "permissionState")
			check(h.t, item["channel"] == "" && item["permissionState"] == "not-verified", "Teams invented channel identity or permission")
			meta, ok := item["teams"].(map[string]any)
			check(h.t, ok, "Teams metadata is not an object")
			exactKeys(h.t, "Teams public metadata", meta, "workflowOrigin", "channelType", "ownershipAcknowledged")
		case "preview":
			exactKeys(h.t, "Teams preview", item, "workspaceId", "findingId", "connectionId", "connectionRevision",
				"profile", "requestedBy", "destination", "payload", "bindingDigest", "nativeValidation", "reviewRequirements")
		case "delivery":
			exactKeys(h.t, "Teams delivery", item, "id", "workspaceId", "findingId", "connectionId", "connectionRevision",
				"profile", "channel", "requestedBy", "state", "payload", "createdAt", "dispatchStartedAt",
				"completedAt", "receipt", "failure", "destination", "outboundAttemptedAt")
			check(h.t, item["channel"] == "", "Teams delivery invented a channel ID")
			if item["failure"] != nil {
				fail, ok := item["failure"].(map[string]any)
				check(h.t, ok, "Teams failure is not an object")
				exactKeys(h.t, "Teams safe failure", fail, "code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable")
				check(h.t, fail["nativeCode"] == "" && fail["retryable"] == false, "Teams retained provider code or automatic retry")
			}
		}
		if kind != "connection" {
			target, ok := item["destination"].(map[string]any)
			check(h.t, ok, "public destination snapshot missing")
			exactKeys(h.t, "Teams destination", target, "name", "workflowOrigin", "channelType", "ownershipAcknowledged")
			p, ok := item["payload"].(map[string]any)
			check(h.t, ok, "canonical Teams payload missing")
			exactKeys(h.t, "Teams payload", p, "title", "body", "deepLink")
		}
		return decoded[teamsReply](h.t, body)
	}
	h.t.Fatal("Teams response omitted its resource")
	return teamsReply{}
}

func (h *harness) teamsConnection(who actor, workflowURL string) teamsConnection {
	h.t.Helper()
	h.rememberTeamsURL(workflowURL)
	c := h.teamsJSON(who, "POST", connectionsPath, teamsInput(workflowURL), 201).Connection
	u, err := url.Parse(workflowURL)
	must(h.t, "parse expected public origin", err)
	check(h.t, c.ID != "" && c.WorkspaceID == who.Workspace && c.Enabled && c.CredentialConfigured &&
		c.Revision == 1 && c.Jira == nil, "Teams connection lost explicit identity/credential metadata")
	same(h.t, "public metadata leaked or rewrote signed routing", c.Teams, teamsMetadata{u.Scheme + "://" + u.Host, "standard", true})
	return c
}

func (h *harness) teamsPreview(who actor, findingID string, c teamsConnection) teamsPreview {
	h.t.Helper()
	v := h.teamsJSON(who, "POST", previewPath(findingID), object{"connectionId": c.ID}, 200).Preview
	check(h.t, v.WorkspaceID == who.Workspace && v.FindingID == findingID && v.ConnectionID == c.ID &&
		v.ConnectionRevision == c.Revision && v.RequestedBy == who.ID && v.NativeValidation == "not-run" &&
		len(v.BindingDigest) == 71 && strings.HasPrefix(v.BindingDigest, "sha256:"),
		"Teams local preview lost its current canonical consent binding")
	raw, err := hex.DecodeString(strings.TrimPrefix(v.BindingDigest, "sha256:"))
	must(h.t, "decode opaque consent digest", err)
	check(h.t, len(raw) == 32 && strings.ToLower(v.BindingDigest) == v.BindingDigest, "preview digest shape differs")
	same(h.t, "preview public destination changed", v.Destination, teamsDestination{c.Name, c.Teams})
	same(h.t, "preview claimed external ownership or validation", v.ReviewRequirements,
		[]string{"explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"})
	return v
}

func (h *harness) teamsQueue(who actor, v teamsPreview, key string, status int) teamsDelivery {
	return h.teamsJSON(who, "POST", historyPath(v.FindingID), queueInput(v.preview, key), status).Delivery
}

func (h *harness) teamsDelivery(who actor, id string) teamsDelivery {
	return h.teamsJSON(who, "GET", deliveryPath(id), nil, 200).Delivery
}

func (h *harness) teamsEncrypted(c teamsConnection, workflowURL string) {
	h.t.Helper()
	envelope := h.ciphertext(c.ID)
	block, err := aes.NewCipher(h.cfg.IntegrationEncryptionKey)
	must(h.t, "open independent Teams AES assertion", err)
	aead, err := cipher.NewGCM(block)
	must(h.t, "open independent Teams GCM assertion", err)
	check(h.t, len(envelope) > 1+aead.NonceSize()+aead.Overhead() && envelope[0] == 1, "Teams credential envelope differs")
	end := 1 + aead.NonceSize()
	aad := []byte("aspm/teams-workflow-credential/v1\x00" + c.WorkspaceID + "\x00" + c.ID)
	plain, err := aead.Open(nil, envelope[1:end], envelope[end:], aad)
	must(h.t, "authenticate whole Workflow URL using independent AAD", err)
	check(h.t, bytes.Equal(plain, []byte(workflowURL)), "encrypted Workflow path/query bytes changed")
	clear(plain)
	for _, wrong := range []string{
		"aspm/teams-workflow-credential/v1\x00other\x00" + c.ID,
		"aspm/teams-workflow-credential/v1\x00" + c.WorkspaceID + "\x00other",
		"aspm/jira-credential/v1\x00" + c.WorkspaceID + "\x00" + c.ID,
		"aspm/slack-credential/v1\x00" + c.WorkspaceID + "\x00" + c.ID,
	} {
		opened, err := aead.Open(nil, envelope[1:end], envelope[end:], []byte(wrong))
		clear(opened)
		check(h.t, err != nil, "Teams credential lost workspace/connection/profile isolation")
	}
	h.remember(hex.EncodeToString(envelope))
}

func teamsPayload(f app.Finding) payload {
	p := expectedPayload(f)
	p.Fields = nil
	return p
}

func teamsCard(p payload) object {
	return object{"type": "message", "attachments": []any{object{
		"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil,
		"content": object{"type": "AdaptiveCard", "version": "1.4",
			"body": []any{
				object{"type": "TextBlock", "text": p.Title, "wrap": true, "weight": "Bolder"},
				object{"type": "TextBlock", "text": p.Body, "wrap": true},
			},
			"actions": []any{object{"type": "Action.OpenUrl", "title": "View finding", "url": p.DeepLink}},
		},
	}}}
}

func assertTeamsCard(t *testing.T, call nativeCall, p payload) {
	t.Helper()
	same(t, "native Teams card differs from reviewed inert text and trusted link", call.Body, teamsCard(p))
	data := string(encoded(t, call.Body))
	for _, canary := range []string{rawCanary, noteCanary, "SOURCE-DESCRIPTION-NOT-APPROVED",
		"scanner-not-approval", "scanner-not-a-credential", "scanner-not-a-mapping", "not-approved.invalid"} {
		check(t, !strings.Contains(data, canary), "Teams export exceeded canonical consent")
	}
}

type teamsPlan struct {
	url, mode, echo string
	marker          func() error
	arrived         chan nativeCall
	release, cancel chan struct{}
	once            sync.Once
	calls           atomic.Int32
}

func (p *teamsPlan) finish() { p.once.Do(func() { close(p.release) }) }

type teamsServer struct {
	t       *testing.T
	server  *httptest.Server
	common  *jiraServer
	rootDER []byte
	calls   atomic.Int32
	mu      sync.Mutex
	plan    *teamsPlan
}

func newTeamsServer(t *testing.T) *teamsServer {
	t.Helper()
	cert, _, rootDER := freshTLS(t)
	n := &teamsServer{t: t, common: newJira(t), rootDER: rootDER}
	n.server = startTLS(t, cert, http.HandlerFunc(n.serve))
	transport := n.common.client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
	root, err := x509.ParseCertificate(rootDER)
	must(t, "parse additional owned Teams TLS root", err)
	transport.TLSClientConfig.RootCAs.AddCert(root)
	allowed := map[string]bool{}
	for _, endpoint := range []string{n.server.URL, n.common.server.URL, n.common.slack.URL, n.common.trap.URL} {
		allowed[strings.TrimPrefix(endpoint, "https://")] = true
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !allowed[address] {
			n.common.forbidden.Add(1)
			return nil, errors.New("unapproved native destination")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	n.common.client = &http.Client{Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		check(t, n.calls.Load()+n.common.calls.Load()+n.common.slackPosts.Load() <= 64, "combined native budget exceeded")
		check(t, transport.Proxy == nil && !transport.TLSClientConfig.InsecureSkipVerify &&
			transport.TLSClientConfig.MinVersion >= tls.VersionTLS12, "owned native TLS policy changed")
	})
	return n
}

func (n *teamsServer) workflowURL() string {
	return n.server.URL + "/workflows/" + nonce(n.t) + "/triggers/%6danual/paths/invoke" +
		"?hint=owned_hint_" + nonce(n.t) + "&sig=owned_sig_" + nonce(n.t) + "%2bSigned" +
		"&route=%2Fowned%2f" + nonce(n.t) + "&hint=second_hint_" + nonce(n.t)
}

func (n *teamsServer) arm(value, mode string, marker func() error) *teamsPlan {
	n.t.Helper()
	u, err := url.Parse(value)
	must(n.t, "parse owned native plan", err)
	p := &teamsPlan{url: value, mode: mode, echo: u.Query().Get("hint"), marker: marker,
		arrived: make(chan nativeCall, 1), release: make(chan struct{}), cancel: make(chan struct{})}
	if mode != "hold" {
		p.finish()
	}
	n.mu.Lock()
	n.plan = p
	n.mu.Unlock()
	n.t.Cleanup(p.finish)
	return p
}

func (n *teamsServer) serve(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	p := n.plan
	n.mu.Unlock()
	_, hasAuth := r.Header["Authorization"]
	if n.calls.Add(1) > 64 || p == nil || p.calls.Add(1) != 1 || r.Method != "POST" ||
		r.RequestURI != strings.TrimPrefix(p.url, n.server.URL) || hasAuth ||
		r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" ||
		r.Header.Get("Content-Type") != "application/json" {
		n.t.Error("Teams native route, signed bytes, authorization or single-POST boundary changed")
		w.WriteHeader(403)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (28<<10)+1))
	var body object
	if err != nil || len(data) > 28<<10 || json.Unmarshal(data, &body) != nil {
		n.t.Error("Teams native card exceeded the adapter byte cap or was not JSON")
		w.WriteHeader(400)
		return
	}
	if p.marker != nil && p.marker() != nil {
		n.t.Error("Teams POST arrived before a committed independent fenced outbound marker")
		w.WriteHeader(500)
		return
	}
	if p.mode == "hold" {
		w.WriteHeader(202)
		w.(http.Flusher).Flush()
	}
	p.arrived <- nativeCall{Method: r.Method, Body: body}
	select {
	case <-r.Context().Done():
		close(p.cancel)
		return
	case <-p.release:
	}
	if p.mode == "drop" {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			n.t.Error("owned Teams lost-ACK fixture could not close its connection")
			return
		}
		_ = conn.Close()
		return
	}
	if p.mode != "hold" {
		status := 202
		if p.mode == "redirect" {
			status = 307
			w.Header().Set("Location", n.common.trap.URL+"/must-not-follow")
		} else if p.mode != "accepted" {
			var err error
			status, err = strconv.Atoi(p.mode)
			if err != nil {
				n.t.Error("invalid native fixture status")
				w.WriteHeader(500)
				return
			}
		}
		if status == 429 {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(object{"error": object{"code": p.echo, "message": p.url}})
}
