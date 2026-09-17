package connector_links

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/bahadrdsr/aspm/internal/connectors"
)

func generated(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal("cannot generate synthetic identity")
	}
	return hex.EncodeToString(value)
}

type capturedLink struct {
	Path, Query, Text string
	Links             []string
}

type ownedSlack struct {
	server *httptest.Server
	client *http.Client
	config connectors.DeliveryConfig
	action connectors.Action
	mu     sync.Mutex
	calls  []capturedLink
}

type exactTransport struct {
	origin string
	next   http.RoundTripper
	t      *testing.T
}

func (g exactTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != "POST" || request.URL.Scheme+"://"+request.URL.Host != g.origin ||
		request.URL.Path != "/api/chat.postMessage" || request.URL.RawQuery != "" || request.URL.Fragment != "" {
		g.t.Error("non-owned or fragment-bearing HTTP destination blocked before I/O")
		return nil, errors.New("owned transport boundary")
	}
	return g.next.RoundTrip(request)
}

func newOwnedSlack(t *testing.T) *ownedSlack {
	t.Helper()
	f := &ownedSlack{}
	token := "synthetic-owned-token-" + generated(t)
	channel := "C" + strings.ToUpper(generated(t)[:10])
	workspace, finding := generated(t), generated(t)
	f.action = connectors.Action{
		WorkspaceID: workspace, FindingID: finding, IntentID: generated(t),
		ApprovalRef: "synthetic-explicit-approval-" + generated(t),
		Title:       "Synthetic browser link", Body: "Owned protocol context only.",
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		var body struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
			Blocks  []struct {
				Elements []struct {
					URL string `json:"url"`
				} `json:"elements"`
			} `json:"blocks"`
		}
		if err != nil || len(data) > 8192 || json.Unmarshal(data, &body) != nil ||
			r.TLS == nil || r.Method != "POST" || r.URL.Path != "/api/chat.postMessage" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+token || body.Channel != channel {
			t.Error("owned native request violated method/path/TLS/credential/channel boundaries; values withheld")
			http.Error(w, "invalid synthetic request", 400)
			return
		}
		call := capturedLink{Path: r.URL.Path, Query: r.URL.RawQuery, Text: body.Text}
		for _, block := range body.Blocks {
			for _, element := range block.Elements {
				if element.URL != "" {
					call.Links = append(call.Links, element.URL)
				}
			}
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": channel, "ts": "1789560000.123456"})
	}))
	endpoint, err := url.Parse(f.server.URL)
	if err != nil {
		t.Fatal("cannot parse owned TLS endpoint")
	}
	transport := f.server.Client().Transport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("owned TLS client must validate its certificate")
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != endpoint.Host {
			t.Error("unapproved provider dial blocked")
			return nil, errors.New("unapproved provider address")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	f.client = &http.Client{Transport: exactTransport{f.server.URL, transport, t}, Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	f.config = connectors.DeliveryConfig{
		Profile: connectors.SlackWorkspaceBot, Endpoint: f.server.URL, WorkspaceID: workspace,
		Token: token, Channel: channel, Client: f.client, Limits: connectors.Limits{Requests: 1, Bytes: 8192},
	}
	t.Cleanup(func() { transport.CloseIdleConnections(); f.server.Close() })
	return f
}

func (f *ownedSlack) snapshot() []capturedLink {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedLink(nil), f.calls...)
}

func (f *ownedSlack) open(t *testing.T) connectors.DeliveryAdapter {
	t.Helper()
	adapter, err := connectors.OpenDelivery(context.Background(), f.config)
	if err != nil || adapter == nil {
		t.Fatal("existing native Slack constructor rejected the valid owned fixture")
	}
	return adapter
}

func assertSendPreserved(t *testing.T, f *ownedSlack, link string) {
	t.Helper()
	action := f.action
	action.IntentID, action.DeepLink = generated(t), link
	prior := len(f.snapshot())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := f.open(t).Send(ctx, action)
	if err != nil {
		t.Fatalf("native Slack Send rejected an HTTPS browser link (%T); link/token values withheld", err)
	}
	if result.State != "confirmed" || result.WorkspaceID != action.WorkspaceID || result.IntentID != action.IntentID {
		t.Fatal("the owned Slack acknowledgement did not confirm the original action identity")
	}
	calls := f.snapshot()
	if len(calls) != prior+1 {
		t.Fatal("one explicit send must make exactly one owned native POST")
	}
	call := calls[prior]
	if call.Path != "/api/chat.postMessage" || call.Query != "" {
		t.Fatal("browser link changed the actual native HTTPS destination")
	}
	if call.Text != action.Title+"\n"+action.Body+"\n"+link || len(call.Links) != 1 || call.Links[0] != link {
		t.Fatal("browser link was stripped, decoded, rewritten or not preserved byte-exactly in fallback text/button")
	}
}

func TestCanonicalFindingBrowserFragmentIsPreserved(t *testing.T) {
	f := newOwnedSlack(t)
	link := "https://browser.synthetic.invalid/#/work?finding=" + f.action.FindingID
	t.Run("PreviewIsValidAndHasNoIO", func(t *testing.T) {
		action := f.action
		action.DeepLink = link
		_, err := f.open(t).Preview(context.Background(), action)
		if err != nil {
			t.Errorf("Preview rejected the canonical HTTPS finding hash route (%T); link/token values withheld", err)
		}
		if len(f.snapshot()) != 0 {
			t.Error("Preview performed native I/O")
		}
	})
	t.Run("SendPreservesExactBrowserRouteWithoutChangingRequestPath", func(t *testing.T) {
		assertSendPreserved(t, f, link)
	})
}

func TestFragmentFreeHTTPSBrowserLinksRemainValid(t *testing.T) {
	f := newOwnedSlack(t)
	for _, link := range []string{
		"https://browser.synthetic.invalid/findings/" + f.action.FindingID,
		"https://browser.synthetic.invalid/work?finding=" + f.action.FindingID + "&view=triage",
	} {
		action := f.action
		action.DeepLink = link
		before := len(f.snapshot())
		if _, err := f.open(t).Preview(context.Background(), action); err != nil {
			t.Fatal("existing fragment-free HTTPS browser link no longer passes Preview")
		}
		if len(f.snapshot()) != before {
			t.Fatal("fragment-free Preview performed I/O")
		}
		assertSendPreserved(t, f, link)
	}
}

func TestEndpointFragmentsAndUnsafeBrowserLinksStayRejected(t *testing.T) {
	f := newOwnedSlack(t)
	t.Run("RequestDestinationsRemainFragmentFree", func(t *testing.T) {
		for _, suffix := range []string{"#fragment", "#/work?finding=" + f.action.FindingID} {
			config := f.config
			config.Endpoint += suffix
			adapter, err := connectors.OpenDelivery(context.Background(), config)
			if adapter != nil || !errors.Is(err, connectors.ErrScope) {
				t.Error("fragment-bearing delivery request endpoint was accepted")
			}
			collector, err := connectors.OpenCollector(context.Background(), connectors.CollectorConfig{
				Profile: connectors.GitHubCloudApp, Endpoint: f.server.URL + suffix, Token: f.config.Token, Client: f.client,
			})
			if collector != nil || !errors.Is(err, connectors.ErrScope) {
				t.Error("fragment-bearing collector request endpoint was accepted")
			}
			var retrieved atomic.Int32
			provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				retrieved.Add(1)
				return aws.Credentials{}, errors.New("credential retrieval not authorized by this constructor-only control")
			})
			collector, err = connectors.OpenCollector(context.Background(), connectors.CollectorConfig{
				Profile: connectors.AWSEC2SecurityHub, Endpoint: f.server.URL, FindingsEndpoint: f.server.URL + suffix,
				Credentials: provider, Client: f.client,
			})
			if collector != nil || !errors.Is(err, connectors.ErrScope) || retrieved.Load() != 0 {
				t.Error("fragment-bearing collector findings endpoint was accepted or discovered credentials")
			}
		}
	})
	id := f.action.FindingID
	for _, row := range []struct{ name, link string }{
		{"non-HTTPS", "http://browser.synthetic.invalid/#/work?finding=" + id},
		{"script-scheme", "javascript:void(0)"},
		{"userinfo", "https://" + generated(t) + ":" + generated(t) + "@browser.synthetic.invalid/#/work?finding=" + id},
		{"raw-control", "https://browser.synthetic.invalid/#/work?finding=" + id + "\x01"},
		{"raw-linebreak", "https://browser.synthetic.invalid/#/work?finding=" + id + "\n"},
		{"decoded-path-control", "https://browser.synthetic.invalid/work%0Aentry?finding=" + id},
		{"decoded-fragment-control", "https://browser.synthetic.invalid/#/work?finding=" + id + "%0A"},
		{"decoded-fragment-NUL", "https://browser.synthetic.invalid/#/work?finding=" + id + "%00"},
		{"decoded-fragment-C1-control", "https://browser.synthetic.invalid/#/work?finding=" + id + "%C2%85"},
		{"decoded-base-query-control", "https://browser.synthetic.invalid/?unsafe=%0D#/work?finding=" + id},
		{"out-of-range-port", "https://browser.synthetic.invalid:70000/#/work?finding=" + id},
		{"invalid-port", "https://browser.synthetic.invalid:notaport/#/work?finding=" + id},
		{"oversize-link", "https://browser.synthetic.invalid/#/work?finding=" + strings.Repeat("a", 16384)},
	} {
		t.Run(row.name, func(t *testing.T) {
			action := f.action
			action.DeepLink = row.link
			adapter := f.open(t)
			if _, err := adapter.Preview(context.Background(), action); !errors.Is(err, connectors.ErrScope) {
				t.Error("unsafe browser link did not fail Preview with ErrScope")
			}
			result, err := adapter.Send(context.Background(), action)
			if !errors.Is(err, connectors.ErrScope) || result.State != "blocked" {
				t.Error("unsafe browser link did not fail closed before native Send")
			}
		})
	}
	if len(f.snapshot()) != 0 {
		t.Fatal("rejected request endpoints or browser links performed native I/O")
	}
}
