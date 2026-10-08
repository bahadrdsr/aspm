//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const genericWebhookV1 = "generic-webhook-v1"

type webhookMetadata struct {
	Origin, Path, Signature string
}

type webhookConnection struct {
	ID, WorkspaceID, Profile, Name, PermissionState string
	Enabled, CredentialConfigured                   bool
	Revision                                        int64
	CreatedAt, UpdatedAt                            time.Time
	Webhook                                         webhookMetadata
}

type webhookPreview struct {
	WorkspaceID, FindingID, ConnectionID, Profile, RequestedBy string
	ConnectionRevision                                         int64
	Webhook                                                    webhookMetadata
	Payload                                                    struct {
		Title, Body, DeepLink string
	}
	BindingDigest      string
	NativeValidation   string
	ReviewRequirements []string
}

type webhookDelivery struct {
	ID, WorkspaceID, FindingID, ConnectionID, Profile, RequestedBy, State string
	ConnectionRevision                                                    int64
	Webhook                                                               webhookMetadata
	Payload                                                               struct {
		Title, Body, DeepLink string
	}
	TriggerKind                                         string
	PolicyID                                            *string
	PolicyRevision, FindingChangeRevision               *int64
	CreatedAt                                           time.Time
	DispatchStartedAt, OutboundAttemptedAt, CompletedAt *time.Time
	Receipt                                             any
	Failure                                             *webhookFailure
}

type webhookFailure struct {
	Code, NativeCode  string
	HTTPStatus        int
	RetryAfterSeconds int64
	Retryable         bool
}

type webhookReply struct {
	APIVersion string
	Error      *apiFailure
	Connection webhookConnection
	Preview    webhookPreview
	Delivery   webhookDelivery
	Items      []json.RawMessage
	Total      int
	NextCursor *string
}

type capturedWebhookRequest struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   []byte
}

type syntheticWebhookReceiver struct {
	t         *testing.T
	server    *httptest.Server
	client    *http.Client
	calls     atomic.Int32
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
	hold      atomic.Bool
	mu        sync.Mutex
	requests  []capturedWebhookRequest
}

func newSyntheticWebhookReceiver(t *testing.T) *syntheticWebhookReceiver {
	t.Helper()
	receiver := &syntheticWebhookReceiver{
		t: t, entered: make(chan struct{}, 8), release: make(chan struct{}),
		cancelled: make(chan struct{}, 8),
	}
	receiver.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		ok(t, "read bounded synthetic webhook request", err)
		if len(data) > 64<<10 {
			t.Error("generic webhook body exceeded the bounded receiver contract")
		}
		receiver.calls.Add(1)
		receiver.mu.Lock()
		receiver.requests = append(receiver.requests, capturedWebhookRequest{
			Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
			Header: r.Header.Clone(), Body: bytes.Clone(data),
		})
		receiver.mu.Unlock()
		select {
		case receiver.entered <- struct{}{}:
		default:
			t.Error("unexpected duplicate generic webhook activity")
		}
		if receiver.hold.Load() {
			select {
			case <-receiver.release:
			case <-r.Context().Done():
				receiver.cancelled <- struct{}{}
				return
			case <-time.After(5 * time.Second):
				t.Error("held generic webhook request was not released or canceled")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.server.Close)
	address := receiver.server.Listener.Addr().String()
	transport := receiver.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: time.Second}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			t.Error("generic webhook client attempted a host or port outside the selected synthetic origin")
			return nil, errors.New("unapproved generic webhook destination")
		}
		return dial(ctx, network, target)
	}
	receiver.client = &http.Client{
		Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") },
	}
	t.Cleanup(receiver.client.CloseIdleConnections)
	return receiver
}

func (r *syntheticWebhookReceiver) origin() string { return r.server.URL }
func (r *syntheticWebhookReceiver) endpoint(path string) string {
	return r.server.URL + path
}
func (r *syntheticWebhookReceiver) captured() []capturedWebhookRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]capturedWebhookRequest, len(r.requests))
	copy(result, r.requests)
	return result
}

func newGenericWebhookHarness(t *testing.T, origins []string) *harness {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 10, 8, 6, 30, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.BootstrapToken = secret(t)
	key := sha256.Sum256([]byte(secret(t)))
	h.services.cfg.IntegrationEncryptionKey = append([]byte(nil), key[:]...)
	h.services.cfg.PublicOrigin = "https://aspm.test"
	h.services.cfg.WebhookOrigins = append([]string(nil), origins...)
	h.services.cfg.LogOutput = io.Discard
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close generic-webhook application before owned-resource cleanup", h.app.Close())
		}
	})
	h.enroll()
	return h
}

func (h *harness) webhookJSON(a actor, method, path string, body any, want int) (webhookReply, []byte) {
	h.t.Helper()
	var data []byte
	if body != nil {
		data = encode(h.t, body)
	}
	response := h.request(a, method, path, data, want)
	raw := bytes.Clone(response.Body.Bytes())
	var reply webhookReply
	ok(h.t, "decode generic-webhook response", json.Unmarshal(raw, &reply))
	equal(h.t, "generic-webhook API version", reply.APIVersion, apiVersion)
	if want >= 400 && (reply.Error == nil || reply.Error.Code == "" ||
		reply.Error.RequestID == "" || reply.Error.Retryable) {
		h.t.Fatal("generic-webhook rejection must be explicit, identified and nonretryable")
	}
	return reply, raw
}

func (h *harness) createWebhookConnection(name, endpoint, secret string, enabled bool) webhookConnection {
	h.t.Helper()
	reply, raw := h.webhookJSON(h.admin, "POST", "/api/v1/integrations/connections", object{
		"profile": genericWebhookV1, "name": name, "enabled": enabled,
		"webhookUrl": endpoint, "secret": secret,
	}, 201)
	connection := reply.Connection
	if connection.ID == "" || connection.WorkspaceID != h.admin.workspace ||
		connection.Profile != genericWebhookV1 || connection.Name != name ||
		connection.Enabled != enabled || !connection.CredentialConfigured ||
		connection.Revision != 1 || connection.PermissionState != "not-verified" ||
		connection.Webhook.Signature != "hmac-sha256" {
		h.t.Fatalf("generic webhook connection lost its bounded public metadata: %#v", connection)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(endpoint)) {
		h.t.Fatal("generic webhook response echoed a secret or full configured URL")
	}
	var shape map[string]json.RawMessage
	ok(h.t, "inspect generic-webhook public connection shape", json.Unmarshal(raw, &shape))
	var metadata map[string]json.RawMessage
	ok(h.t, "decode generic-webhook public connection metadata", json.Unmarshal(shape["connection"], &metadata))
	wantKeys := []string{
		"createdAt", "credentialConfigured", "enabled", "id", "name", "permissionState",
		"profile", "revision", "updatedAt", "webhook", "workspaceId",
	}
	gotKeys := make([]string, 0, len(metadata))
	for key := range metadata {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		h.t.Fatalf("generic webhook public metadata keys got %v, want %v", gotKeys, wantKeys)
	}
	return connection
}

func (h *harness) webhookFinding(label string) finding {
	h.t.Helper()
	asset := h.asset(h.admin, label, &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(h.t, "sarif.json"))
	input["scanId"] = "webhook-" + nonce(h.t)
	h.finish(h.upload(input).ID, "succeeded")
	for _, item := range h.work(h.admin, label) {
		if item.AssetName == label {
			return h.finding(h.admin, item.ID)
		}
	}
	h.t.Fatal("generic-webhook fixture did not create its canonical finding")
	return finding{}
}

func (h *harness) webhookPreview(a actor, findingID, connectionID string, want int) webhookPreview {
	h.t.Helper()
	reply, _ := h.webhookJSON(a, "POST", "/api/v1/findings/"+findingID+"/delivery-previews",
		object{"connectionId": connectionID}, want)
	return reply.Preview
}

func webhookQueueInput(preview webhookPreview, key string) object {
	return object{
		"connectionId": preview.ConnectionID, "idempotencyKey": key,
		"previewDigest": preview.BindingDigest, "confirm": true,
	}
}

func (h *harness) webhookQueue(a actor, preview webhookPreview, key string, want int) webhookDelivery {
	h.t.Helper()
	reply, _ := h.webhookJSON(a, "POST", "/api/v1/findings/"+preview.FindingID+"/deliveries",
		webhookQueueInput(preview, key), want)
	return reply.Delivery
}

func (h *harness) webhookDelivery(a actor, id string) webhookDelivery {
	h.t.Helper()
	reply, _ := h.webhookJSON(a, "GET", "/api/v1/integrations/deliveries/"+id, nil, 200)
	return reply.Delivery
}

func (h *harness) openWebhookWorker(t *testing.T, receiver *syntheticWebhookReceiver,
	origins []string, label string) DeliveryWorker {
	t.Helper()
	requireDeliveryWorker(t)
	nameDigest := sha256.Sum256([]byte(label))
	nameSuffix := "-webhook-" + hex.EncodeToString(nameDigest[:6])
	namePrefix := h.services.cfg.ApplicationName
	if limit := 63 - len(nameSuffix); len(namePrefix) > limit {
		namePrefix = namePrefix[:limit]
	}
	worker, err := Production.OpenDeliveryWorker(h.services.ctx, DeliveryWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: namePrefix + nameSuffix,
		MaxConnections:  1, EncryptionKey: h.services.cfg.IntegrationEncryptionKey,
		WorkerID: label + "-" + nonce(t), LeaseDuration: 2 * time.Second,
		PublicOrigin: h.services.cfg.PublicOrigin, WebhookOrigins: append([]string(nil), origins...),
		SlackEndpoint: "https://slack.com", Client: receiver.client,
		LogOutput: h.services.cfg.LogOutput, QueryTracer: h.services.cfg.QueryTracer,
	})
	ok(t, "open independent generic-webhook delivery worker", err)
	if worker.ProcessNext == nil || worker.Ping == nil || worker.Close == nil {
		t.Fatal("generic-webhook delivery worker binding omitted process, SQL availability or close")
	}
	t.Cleanup(func() { ok(t, "close generic-webhook delivery worker", worker.Close()) })
	return worker
}

func assertWebhookCipher(t *testing.T, h *harness, connection webhookConnection, secret string) []byte {
	t.Helper()
	var envelope []byte
	ok(t, "read generic-webhook ciphertext", h.services.db.QueryRow(h.services.ctx,
		`SELECT credential_ciphertext FROM `+notificationTable(h, "integration_connections")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, connection.ID).Scan(&envelope))
	if bytes.Contains(envelope, []byte(secret)) {
		t.Fatal("generic webhook HMAC secret was stored in plaintext")
	}
	block, err := aes.NewCipher(h.services.cfg.IntegrationEncryptionKey)
	ok(t, "open independent generic-webhook AES assertion", err)
	aead, err := cipher.NewGCM(block)
	ok(t, "open independent generic-webhook GCM assertion", err)
	if len(envelope) <= 1+aead.NonceSize()+aead.Overhead() || envelope[0] != 1 {
		t.Fatal("generic webhook credential envelope is not versioned authenticated encryption")
	}
	end := 1 + aead.NonceSize()
	aad := []byte("aspm/generic-webhook-credential/v1\x00" + connection.WorkspaceID + "\x00" + connection.ID)
	plain, err := aead.Open(nil, envelope[1:end], envelope[end:], aad)
	ok(t, "authenticate generic-webhook profile/workspace/connection AAD", err)
	equal(t, "decrypted generic-webhook HMAC secret", string(plain), secret)
	clear(plain)
	for _, wrong := range []string{
		"aspm/slack-credential/v1\x00" + connection.WorkspaceID + "\x00" + connection.ID,
		"aspm/generic-webhook-credential/v1\x00other\x00" + connection.ID,
		"aspm/generic-webhook-credential/v1\x00" + connection.WorkspaceID + "\x00other",
	} {
		opened, openErr := aead.Open(nil, envelope[1:end], envelope[end:], []byte(wrong))
		clear(opened)
		if openErr == nil {
			t.Fatal("generic webhook secret lost profile/workspace/connection AAD isolation")
		}
	}
	return envelope
}

func TestM08_V22GenericWebhookConnectionPreviewAndExplicitQueueConsent(t *testing.T) {
	receiver := newSyntheticWebhookReceiver(t)
	h := newGenericWebhookHarness(t, []string{receiver.origin()})
	secret := "0123456789abcdef0123456789abcdef"
	endpoint := receiver.endpoint("/owned/aspm")
	connection := h.createWebhookConnection("Owned generic receiver", endpoint, secret, true)
	equal(t, "generic webhook origin metadata", connection.Webhook.Origin, receiver.origin())
	equal(t, "generic webhook path metadata", connection.Webhook.Path, "/owned/aspm")
	ciphertext := assertWebhookCipher(t, h, connection, secret)

	catalog := h.json(h.admin, "GET", "/api/v1/integrations/catalog", nil, 200)
	equal(t, "native integration catalog remains exactly eight", len(catalog.Items), 8)
	for _, item := range catalog.Items {
		if bytes.Contains(item, []byte(genericWebhookV1)) {
			t.Fatal("non-native generic webhook appeared as a ninth native catalog family")
		}
	}
	viewer, analyst := h.addUser(h.admin, "viewer"), h.addUser(h.admin, "analyst")
	read, raw := h.webhookJSON(viewer, "GET", "/api/v1/integrations/connections/"+connection.ID, nil, 200)
	equal(t, "viewer safe generic webhook metadata", read.Connection, connection)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(endpoint)) {
		t.Fatal("viewer metadata exposed a generic webhook secret or full URL")
	}
	list, _ := h.webhookJSON(viewer, "GET",
		"/api/v1/integrations/connections?profile="+genericWebhookV1, nil, 200)
	if list.Total != 1 || len(list.Items) != 1 || list.NextCursor != nil {
		t.Fatal("generic webhook list did not return the exact scoped small collection")
	}
	h.webhookJSON(analyst, "POST", "/api/v1/integrations/connections", object{
		"profile": genericWebhookV1, "name": "Denied analyst config", "enabled": true,
		"webhookUrl": endpoint, "secret": secret,
	}, 403)

	for _, input := range []object{
		{"profile": genericWebhookV1, "name": "Unapproved", "enabled": true,
			"webhookUrl": "https://unapproved.synthetic.invalid/hook", "secret": secret},
		{"profile": genericWebhookV1, "name": "HTTP", "enabled": true,
			"webhookUrl": strings.Replace(endpoint, "https:", "http:", 1), "secret": secret},
		{"profile": genericWebhookV1, "name": "Query", "enabled": true,
			"webhookUrl": endpoint + "?secret=forbidden", "secret": secret},
		{"profile": genericWebhookV1, "name": "Fragment", "enabled": true,
			"webhookUrl": endpoint + "#forbidden", "secret": secret},
		{"profile": genericWebhookV1, "name": "Dot segment", "enabled": true,
			"webhookUrl": receiver.endpoint("/owned/../aspm"), "secret": secret},
		{"profile": genericWebhookV1, "name": "Encoded separator", "enabled": true,
			"webhookUrl": receiver.endpoint("/owned%2faspm"), "secret": secret},
		{"profile": genericWebhookV1, "name": "No explicit path", "enabled": true,
			"webhookUrl": receiver.origin(), "secret": secret},
		{"profile": genericWebhookV1, "name": "Oversize URL", "enabled": true,
			"webhookUrl": receiver.origin() + "/" + strings.Repeat("p", 16384), "secret": secret},
		{"profile": genericWebhookV1, "name": "Short secret", "enabled": true,
			"webhookUrl": endpoint, "secret": strings.Repeat("s", 31)},
		{"profile": genericWebhookV1, "name": "Long secret", "enabled": true,
			"webhookUrl": endpoint, "secret": strings.Repeat("s", 4097)},
		{"profile": genericWebhookV1, "name": "Whitespace secret", "enabled": true,
			"webhookUrl": endpoint, "secret": " " + secret},
		{"profile": genericWebhookV1, "name": "Control secret", "enabled": true,
			"webhookUrl": endpoint, "secret": secret + "\n"},
		{"profile": genericWebhookV1, "name": "Caller headers", "enabled": true,
			"webhookUrl": endpoint, "secret": secret, "headers": object{"Authorization": "forged"}},
		{"profile": genericWebhookV1, "name": "Caller method", "enabled": true,
			"webhookUrl": endpoint, "secret": secret, "method": "PUT"},
		{"profile": genericWebhookV1, "name": "Caller template", "enabled": true,
			"webhookUrl": endpoint, "secret": secret, "bodyTemplate": "{{finding.evidence}}"},
		{"profile": genericWebhookV1, "name": "Caller script", "enabled": true,
			"webhookUrl": endpoint, "secret": secret, "script": "return finding"},
		{"profile": genericWebhookV1, "name": "Caller resolution", "enabled": true,
			"webhookUrl": endpoint, "secret": secret, "resolveFinding": true},
	} {
		h.webhookJSON(h.admin, "POST", "/api/v1/integrations/connections", input, 400)
	}
	atLimitURL := receiver.origin() + "/" +
		strings.Repeat("p", 16384-len(receiver.origin())-1)
	if len(atLimitURL) != 16384 {
		t.Fatal("generic webhook URL boundary fixture is not exactly 16 KiB")
	}
	h.createWebhookConnection("Maximum bounded generic receiver", atLimitURL,
		strings.Repeat("k", 4096), false)

	unchanged, _ := h.webhookJSON(h.admin, "PATCH",
		"/api/v1/integrations/connections/"+connection.ID, object{
			"name": connection.Name, "enabled": connection.Enabled,
			"webhookUrl": endpoint, "secret": secret,
		}, 200)
	equal(t, "generic webhook no-op revision", unchanged.Connection.Revision, connection.Revision)
	if !bytes.Equal(ciphertext, assertWebhookCipher(t, h, unchanged.Connection, secret)) {
		t.Fatal("generic webhook no-op patch rotated authenticated ciphertext")
	}
	h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"secret": ""}, 400)
	h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"webhookUrl": nil}, 400)
	rotatedSecret := "abcdef0123456789abcdef0123456789"
	rotatedEndpoint := receiver.endpoint("/owned/rotated")
	rotated, _ := h.webhookJSON(h.admin, "PATCH",
		"/api/v1/integrations/connections/"+connection.ID, object{
			"name": "Renamed generic receiver", "enabled": false,
			"webhookUrl": rotatedEndpoint, "secret": rotatedSecret,
		}, 200)
	if rotated.Connection.Revision != connection.Revision+1 || rotated.Connection.Enabled ||
		rotated.Connection.Webhook.Path != "/owned/rotated" {
		t.Fatal("real generic webhook target/secret change did not advance one atomic revision")
	}
	assertWebhookCipher(t, h, rotated.Connection, rotatedSecret)
	foreign := h.addWorkspace()
	h.webhookJSON(foreign, "GET", "/api/v1/integrations/connections/"+connection.ID, nil, 404)
	h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"enabled": true}, 200)

	finding := h.webhookFinding("Generic webhook consent source")
	preview := h.webhookPreview(analyst, finding.ID, connection.ID, 200)
	if preview.WorkspaceID != h.admin.workspace || preview.FindingID != finding.ID ||
		preview.ConnectionID != connection.ID || preview.ConnectionRevision != connection.Revision+2 ||
		preview.Profile != genericWebhookV1 || preview.RequestedBy != analyst.user.ID ||
		preview.Webhook.Origin != receiver.origin() || preview.Webhook.Path != "/owned/rotated" ||
		preview.Webhook.Signature != "hmac-sha256" ||
		preview.NativeValidation != "not-run" ||
		!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(preview.BindingDigest) ||
		!reflect.DeepEqual(preview.ReviewRequirements, []string{
			"explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification",
		}) {
		t.Fatalf("generic webhook preview lost its exact local consent contract: %#v", preview)
	}
	if preview.Payload.Title != finding.Title ||
		preview.Payload.Body != "Severity: "+finding.Severity+"\nAsset: "+finding.AssetName ||
		preview.Payload.DeepLink != "https://aspm.test/#/work?finding="+url.QueryEscape(finding.ID) {
		t.Fatal("generic webhook preview did not use the canonical common notification payload")
	}
	equal(t, "preview performs zero provider I/O", receiver.calls.Load(), int32(0))
	empty, _ := h.webhookJSON(viewer, "GET",
		"/api/v1/findings/"+finding.ID+"/deliveries?profile="+genericWebhookV1, nil, 200)
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatal("opening a generic webhook preview created a delivery side effect")
	}
	h.webhookJSON(viewer, "POST", "/api/v1/findings/"+finding.ID+"/deliveries",
		webhookQueueInput(preview, "viewer-denied"), 403)
	for _, input := range []object{
		{"connectionId": preview.ConnectionID, "idempotencyKey": "missing-consent",
			"previewDigest": preview.BindingDigest},
		{"connectionId": preview.ConnectionID, "idempotencyKey": "false-consent",
			"previewDigest": preview.BindingDigest, "confirm": false},
		{"connectionId": preview.ConnectionID, "idempotencyKey": "forged-digest",
			"previewDigest": "sha256:" + strings.Repeat("0", 64), "confirm": true},
	} {
		want := 400
		if input["idempotencyKey"] == "forged-digest" {
			want = 409
		}
		h.webhookJSON(analyst, "POST", "/api/v1/findings/"+finding.ID+"/deliveries", input, want)
	}
	h.json(h.admin, "PATCH", "/api/v1/assets/"+finding.AssetID,
		object{"name": "Changed after generic webhook review"}, 200)
	h.webhookJSON(analyst, "POST", "/api/v1/findings/"+finding.ID+"/deliveries",
		webhookQueueInput(preview, "stale-preview"), 409)
	h.json(h.admin, "PATCH", "/api/v1/assets/"+finding.AssetID,
		object{"name": finding.AssetName}, 200)
	preview = h.webhookPreview(analyst, finding.ID, connection.ID, 200)
	queued := h.webhookQueue(analyst, preview, "generic-manual-1", 202)
	if queued.ID == "" || queued.State != "queued" || queued.RequestedBy != analyst.user.ID ||
		queued.ConnectionRevision != preview.ConnectionRevision || queued.DispatchStartedAt != nil ||
		queued.OutboundAttemptedAt != nil || queued.CompletedAt != nil ||
		queued.Receipt != nil || queued.Failure != nil {
		t.Fatalf("202 generic webhook response was not an immutable queued intent: %#v", queued)
	}
	equal(t, "same generic webhook binding/key replay", h.webhookQueue(analyst, preview, "generic-manual-1", 200), queued)
	second := h.webhookQueue(analyst, preview, "generic-manual-2", 202)
	if second.ID == queued.ID {
		t.Fatal("new explicit generic webhook idempotency key blindly replaced the prior intent")
	}
	equal(t, "queue performs zero provider I/O", receiver.calls.Load(), int32(0))
}

func verifyWebhookRequest(t *testing.T, request capturedWebhookRequest, delivery webhookDelivery,
	secret string, triggerKind string, policyID *string, policyRevision, changeRevision *int64) {
	t.Helper()
	if request.Method != http.MethodPost || request.Path != delivery.Webhook.Path || request.RawQuery != "" ||
		request.Header.Get("Content-Type") != "application/json" ||
		request.Header.Get("Accept") != "application/json" ||
		request.Header.Get("X-ASPM-Event") != "finding.notification.v1" ||
		request.Header.Get("X-ASPM-Delivery-ID") != delivery.ID ||
		request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		t.Fatal("generic webhook request escaped its fixed method, target or server-owned header contract")
	}
	var body struct {
		APIVersion, Event, DeliveryID, WorkspaceID, FindingID string
		Trigger                                               struct {
			Kind                  string
			PolicyID              *string
			PolicyRevision        *int64
			FindingChangeRevision *int64
		}
		Notification struct{ Title, Body, DeepLink string }
	}
	ok(t, "decode exact generic webhook body", json.Unmarshal(request.Body, &body))
	if body.APIVersion != "aspm/webhook/v1" || body.Event != "finding.notification" ||
		body.DeliveryID != delivery.ID || body.WorkspaceID != delivery.WorkspaceID ||
		body.FindingID != delivery.FindingID || body.Trigger.Kind != triggerKind ||
		!reflect.DeepEqual(body.Trigger.PolicyID, policyID) ||
		!reflect.DeepEqual(body.Trigger.PolicyRevision, policyRevision) ||
		!reflect.DeepEqual(body.Trigger.FindingChangeRevision, changeRevision) ||
		body.Notification.Title != delivery.Payload.Title ||
		body.Notification.Body != delivery.Payload.Body ||
		body.Notification.DeepLink != delivery.Payload.DeepLink {
		t.Fatalf("generic webhook exact body identity differs: %#v", body)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(request.Body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	equal(t, "generic webhook exact body signature", request.Header.Get("X-ASPM-Signature"), want)
}

func TestM08_V22GenericWebhookWorkerCommitsAttemptBeforeHTTPAndRecordsAccepted(t *testing.T) {
	receiver := newSyntheticWebhookReceiver(t)
	receiver.hold.Store(true)
	h := newGenericWebhookHarness(t, []string{receiver.origin()})
	secret := "0123456789abcdef0123456789abcdef"
	connection := h.createWebhookConnection("Held generic receiver",
		receiver.endpoint("/held/manual"), secret, true)
	writer := h.addUser(h.admin, "analyst")
	finding := h.webhookFinding("Generic webhook held source")
	preview := h.webhookPreview(writer, finding.ID, connection.ID, 200)
	queued := h.webhookQueue(writer, preview, "held-manual-webhook", 202)
	worker := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "held-manual")
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessNext(h.services.ctx)
		done <- err
	}()
	select {
	case <-receiver.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("generic webhook worker did not reach the synthetic receiver")
	}
	var attempted bool
	ok(t, "observe committed generic webhook attempt marker", h.services.db.QueryRow(h.services.ctx,
		`SELECT create_attempted_at IS NOT NULL FROM `+notificationTable(h, "finding_deliveries")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, queued.ID).Scan(&attempted))
	if !attempted {
		t.Fatal("generic webhook reached HTTP before committing its outbound attempt marker")
	}
	probe, cancel := context.WithTimeout(h.services.ctx, 750*time.Millisecond)
	defer cancel()
	ok(t, "generic webhook HTTP must not hold the worker SQL connection", worker.Ping(probe))
	close(receiver.release)
	select {
	case err := <-done:
		ok(t, "finish accepted generic webhook delivery", err)
	case <-time.After(3 * time.Second):
		t.Fatal("generic webhook worker did not finish after receiver acceptance")
	}
	accepted := h.webhookDelivery(h.admin, queued.ID)
	if accepted.State != "accepted" || accepted.Receipt != nil || accepted.Failure != nil ||
		accepted.DispatchStartedAt == nil || accepted.OutboundAttemptedAt == nil ||
		accepted.CompletedAt == nil {
		t.Fatalf("HTTP 2xx did not persist endpoint accepted without downstream receipt: %#v", accepted)
	}
	requests := receiver.captured()
	if len(requests) != 1 {
		t.Fatalf("generic webhook worker sent %d requests, want one", len(requests))
	}
	verifyWebhookRequest(t, requests[0], accepted, secret, "manual", nil, nil, nil)
	equal(t, "generic webhook dispatch never mutates finding truth",
		h.finding(h.admin, finding.ID), finding)
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "observe empty generic webhook queue after terminal acceptance", err)
	if processed || receiver.calls.Load() != 1 {
		t.Fatal("terminal accepted generic webhook resurrected or retried")
	}
}

func TestM08_V22GenericWebhookAuthorityChangesBlockOrCancelWithoutResurrection(t *testing.T) {
	receiver := newSyntheticWebhookReceiver(t)
	h := newGenericWebhookHarness(t, []string{receiver.origin()})
	secret := "0123456789abcdef0123456789abcdef"
	writer := h.addUser(h.admin, "analyst")

	type scenario struct {
		name    string
		mutate  func(webhookConnection)
		origins []string
	}
	for _, row := range []scenario{
		{name: "role-loss", origins: []string{receiver.origin()}, mutate: func(webhookConnection) {
			h.notificationJSON(h.admin, "PATCH", "/api/v1/users/"+writer.user.ID,
				object{"role": "viewer"}, 200)
		}},
		{name: "connection-disable", origins: []string{receiver.origin()}, mutate: func(connection webhookConnection) {
			h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
				object{"enabled": false}, 200)
		}},
		{name: "target-secret-revision", origins: []string{receiver.origin()}, mutate: func(connection webhookConnection) {
			h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
				object{"webhookUrl": receiver.endpoint("/changed"), "secret": "abcdef0123456789abcdef0123456789"}, 200)
		}},
		{name: "operator-origin-removal", origins: []string{}, mutate: func(webhookConnection) {}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.name != "role-loss" {
				h.notificationJSON(h.admin, "PATCH", "/api/v1/users/"+writer.user.ID,
					object{"role": "analyst"}, 200)
			}
			connection := h.createWebhookConnection("Predispatch "+row.name,
				receiver.endpoint("/predispatch/"+row.name), secret, true)
			finding := h.webhookFinding("Predispatch " + row.name)
			preview := h.webhookPreview(writer, finding.ID, connection.ID, 200)
			queued := h.webhookQueue(writer, preview, "predispatch-"+row.name, 202)
			row.mutate(connection)
			worker := h.openWebhookWorker(t, receiver, row.origins, "predispatch-"+row.name)
			processed, err := worker.ProcessNext(h.services.ctx)
			ok(t, "settle generic webhook predispatch denial", err)
			if !processed {
				t.Fatal("generic webhook predispatch denial found no durable intent")
			}
			blocked := h.webhookDelivery(h.admin, queued.ID)
			if blocked.State != "blocked" || blocked.Failure == nil ||
				blocked.DispatchStartedAt != nil || blocked.OutboundAttemptedAt != nil {
				t.Fatalf("generic webhook predispatch authority change did not block before an attempt: %#v", blocked)
			}
			ok(t, "close predispatch worker", worker.Close())
		})
	}
	h.notificationJSON(h.admin, "PATCH", "/api/v1/users/"+writer.user.ID,
		object{"role": "analyst"}, 200)
	fresh := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "predispatch-fresh")
	processed, err := fresh.ProcessNext(h.services.ctx)
	ok(t, "check terminal generic webhook predispatch denials", err)
	if processed || receiver.calls.Load() != 0 {
		t.Fatal("regrant or reopen resurrected a blocked generic webhook delivery")
	}

	receiver.hold.Store(true)
	connection := h.createWebhookConnection("Cancelable generic receiver",
		receiver.endpoint("/held/cancel"), secret, true)
	finding := h.webhookFinding("Generic webhook cancellation source")
	preview := h.webhookPreview(writer, finding.ID, connection.ID, 200)
	queued := h.webhookQueue(writer, preview, "held-cancel-webhook", 202)
	worker := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "held-cancel")
	done := make(chan error, 1)
	go func() {
		_, processErr := worker.ProcessNext(h.services.ctx)
		done <- processErr
	}()
	select {
	case <-receiver.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("generic webhook cancellation fixture did not reach possible POST")
	}
	h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"enabled": false}, 200)
	select {
	case <-receiver.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("generic webhook authority watcher did not cancel held HTTP")
	}
	select {
	case err := <-done:
		ok(t, "persist generic webhook uncertain cancellation", err)
	case <-time.After(3 * time.Second):
		t.Fatal("generic webhook cancellation did not settle")
	}
	uncertain := h.webhookDelivery(h.admin, queued.ID)
	if uncertain.State != "uncertain" || uncertain.Failure == nil ||
		uncertain.DispatchStartedAt == nil || uncertain.OutboundAttemptedAt == nil ||
		uncertain.Receipt != nil {
		t.Fatalf("possible generic webhook POST was not durably uncertain: %#v", uncertain)
	}
	h.webhookJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"enabled": true}, 200)
	ok(t, "close canceled generic webhook worker", worker.Close())
	next := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "held-cancel-fresh")
	processed, err = next.ProcessNext(h.services.ctx)
	ok(t, "observe terminal generic webhook uncertainty", err)
	if processed || receiver.calls.Load() != 1 {
		t.Fatal("fresh worker resent an uncertain generic webhook delivery")
	}
}

func TestM08_V22NotificationPolicyQueuesGenericWebhookWithoutProviderIOOrJiraEffect(t *testing.T) {
	receiver := newSyntheticWebhookReceiver(t)
	h := newGenericWebhookHarness(t, []string{receiver.origin()})
	secret := "0123456789abcdef0123456789abcdef"
	connection := h.createWebhookConnection("Policy generic receiver",
		receiver.endpoint("/policy"), secret, true)
	policy := h.createPolicy(policyInput(
		"High severity generic webhook", connection.ID, true,
		[]string{"new", "changed", "reopened"}, "medium",
		"Approve one fixed signed webhook for each matching finding change revision.",
	))
	if policy.ConnectionProfile != genericWebhookV1 ||
		policy.Connection.Profile != genericWebhookV1 ||
		policy.ConnectionRevision != connection.Revision {
		t.Fatal("notification policy did not snapshot the configured generic webhook profile")
	}
	asset := h.asset(h.admin, "Generic policy source", &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["scanId"] = "generic-policy-new"
	h.finish(h.upload(input).ID, "succeeded")
	finding := h.finding(h.admin, h.work(h.admin, "Generic policy source")[0].ID)
	events := h.changeEvents()
	if len(events) != 1 {
		t.Fatal("generic webhook policy fixture did not create one durable change event")
	}
	worker := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "generic-policy")
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "evaluate generic webhook notification policy", err)
	if !processed || receiver.calls.Load() != 0 {
		t.Fatal("generic webhook policy evaluation performed provider I/O or found no event")
	}
	history := h.policyEvents(h.admin, policy.ID)
	if len(history) != 1 || history[0].Outcome != "queued" || history[0].DeliveryID == nil {
		t.Fatalf("generic webhook policy did not create one normal queued delivery: %#v", history)
	}
	delivery := h.webhookDelivery(h.admin, *history[0].DeliveryID)
	if delivery.TriggerKind != "notification-policy" || delivery.PolicyID == nil ||
		*delivery.PolicyID != policy.ID || delivery.PolicyRevision == nil ||
		*delivery.PolicyRevision != policy.Revision ||
		delivery.FindingChangeRevision == nil ||
		*delivery.FindingChangeRevision != finding.ChangeRevision {
		t.Fatalf("generic webhook policy delivery lost its exact positive trigger identity: %#v", delivery)
	}
	var jiraEffects int
	ok(t, "count no generic webhook Jira effect", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+notificationTable(h, "jira_finding_effects")+`
		WHERE workspace_id=$1 AND delivery_id=$2`, h.admin.workspace, delivery.ID).Scan(&jiraEffects))
	equal(t, "generic webhook does not use Jira effect keys", jiraEffects, 0)

	processed, err = worker.ProcessNext(h.services.ctx)
	ok(t, "dispatch queued generic webhook policy delivery", err)
	if !processed || receiver.calls.Load() != 1 {
		t.Fatal("generic webhook policy delivery did not use the normal durable outbox")
	}
	accepted := h.webhookDelivery(h.admin, delivery.ID)
	requests := receiver.captured()
	verifyWebhookRequest(t, requests[0], accepted, secret, "notification-policy",
		accepted.PolicyID, accepted.PolicyRevision, accepted.FindingChangeRevision)

	ok(t, "close policy worker before operator-origin removal", worker.Close())
	secondAsset := h.asset(h.admin, "Generic stale-origin policy source", &h.admin.user.ID)
	secondInput := h.input(secondAsset.ID, "sarif", fixture(t, "sarif.json"))
	secondInput["scanId"], secondInput["sourceId"] =
		"generic-policy-origin-removed", "generic-policy-origin-removed-source"
	h.finish(h.upload(secondInput).ID, "succeeded")
	removed := h.openWebhookWorker(t, receiver, nil, "generic-policy-origin-removed")
	processed, err = removed.ProcessNext(h.services.ctx)
	ok(t, "evaluate generic webhook after operator origin removal", err)
	if !processed || receiver.calls.Load() != 1 {
		t.Fatal("origin-removed policy evaluation performed receiver I/O or found no event")
	}
	history = h.policyEvents(h.admin, policy.ID)
	stale := 0
	for _, event := range history {
		if event.Outcome == "connection-stale" && event.DeliveryID == nil {
			stale++
		}
	}
	if len(history) != 2 || stale != 1 {
		t.Fatalf("operator origin removal did not record connection-stale without a delivery: %#v", history)
	}

	thirdAsset := h.asset(h.admin, "Generic invalid-payload policy source", &h.admin.user.ID)
	thirdInput := h.input(thirdAsset.ID, "sarif", fixture(t, "sarif.json"))
	thirdInput["scanId"], thirdInput["sourceId"] =
		"generic-policy-invalid-payload", "generic-policy-invalid-payload-source"
	h.finish(h.upload(thirdInput).ID, "succeeded")
	pending := h.changeEvents()
	var pendingID string
	for _, event := range pending {
		if event.State == "pending" {
			pendingID = event.ID
		}
	}
	if pendingID == "" {
		t.Fatal("invalid-payload fixture did not create a pending policy event")
	}
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_change_events")+`
		SET title=' ' WHERE workspace_id=$1 AND id=$2 AND state='pending'`,
		h.admin.workspace, pendingID)
	ok(t, "calibrate only the owned invalid policy payload", err)
	invalidWorker := h.openWebhookWorker(t, receiver, []string{receiver.origin()}, "generic-policy-invalid-payload")
	processed, err = invalidWorker.ProcessNext(h.services.ctx)
	ok(t, "evaluate invalid generic webhook policy payload", err)
	if !processed || receiver.calls.Load() != 1 {
		t.Fatal("invalid generic webhook policy payload performed receiver I/O or found no event")
	}
	history = h.policyEvents(h.admin, policy.ID)
	invalid := 0
	for _, event := range history {
		if event.Outcome == "invalid-payload" && event.DeliveryID == nil {
			invalid++
		}
	}
	if len(history) != 3 || invalid != 1 {
		t.Fatalf("invalid generic webhook policy payload did not record invalid-payload: %#v", history)
	}
}
