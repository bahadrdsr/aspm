//go:build integration && jira_runtime

package jira_work_items

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/service"
)

type runtimeDetector struct {
	listener net.Listener
	accepted atomic.Int32
	done     chan struct{}
	once     sync.Once
}

func runtimeDetect(t *testing.T, label string) *runtimeDetector {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned preflight socket detector", err)
	d := &runtimeDetector{listener: listener, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			d.accepted.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		d.close()
		runtimeEvidence(t, label+"-detector", object{"acceptedSockets": d.accepted.Load()})
	})
	return d
}

func (d *runtimeDetector) close() {
	d.once.Do(func() { _ = d.listener.Close(); <-d.done })
}

func runtimeRejectDial(t *testing.T, ctx context.Context, client *http.Client, network, address string) {
	t.Helper()
	transport := runtimeClientPolicy(t, client)
	bound, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := transport.DialContext(bound, network, address)
	if conn != nil {
		_ = conn.Close()
	}
	check(t, err != nil && conn == nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled),
		"unapproved destination was dialed or merely timed out instead of being rejected before I/O")
}

func runtimeOriginError(t *testing.T, err error, input string, private ...string) {
	t.Helper()
	check(t, err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled),
		"RED: malformed ASPM_JIRA_API_ORIGINS was ignored instead of rejected in preflight")
	message := err.Error()
	lower := strings.ToLower(message)
	check(t, strings.Contains(lower, "aspm_jira_api_origins") || strings.Contains(lower, "jiraapiorigins"),
		"origin preflight must identify its field before a DB error or conflicting listener")
	check(t, len(message) <= 512 && !strings.Contains(message, "private-origin-canary"),
		"origin preflight echoed private input or emitted an unbounded diagnostic")
	if len(input) > 5 {
		check(t, !strings.Contains(message, input), "origin preflight echoed the raw operator setting")
	}
	for _, value := range private {
		check(t, value == "" || !strings.Contains(message, value), "origin preflight exposed unrelated private configuration")
	}
}

func runtimeNativeRequest(t *testing.T, ctx context.Context, client *http.Client, method, target, token string, body any) {
	t.Helper()
	var input []byte
	if body != nil {
		input = encoded(t, body)
	}
	bound, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(bound, method, target, bytes.NewReader(input))
	must(t, "construct real TLS origin-routing request", err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	must(t, "RED: environment client must reach every explicitly approved TLS origin", err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	must(t, "read actual native TLS response", err)
	check(t, response.StatusCode == 200 && len(data) <= 64<<10, "actual owned TLS origin did not return its native response")
	result := decoded[object](t, data)
	if method == "GET" {
		_, present := result["fields"]
		check(t, present, "actual Jira createmeta response is missing")
	} else {
		check(t, result["ok"] == true, "actual native Slack response is missing")
	}
}

func TestJiraRuntimeR1EnvironmentAndPreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	db := runtimeDetect(t, "r1-database")
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve conflicting health listener", err)
	t.Cleanup(func() { _ = reserved.Close() })
	key := random(t, 32)
	dbURL := "postgres://owned:private-origin-canary@" + db.listener.Addr().String() + "/runtime?sslmode=disable"
	base := runtimeSettings(t, dbURL, "jira_runtime_preflight", reserved.Addr().String(), key, "", "", nil)
	delete(base, "ASPM_DELIVERY_LEASE_DURATION")
	base["ASPM_S3_ENDPOINT"] = "private-origin-canary-not-a-storage-endpoint"
	base["ASPM_S3_PREPARE_READINESS"] = "not-a-delivery-setting"
	base["ASPM_S3_ACCESS_KEY"] = "private-origin-canary-storage-key"
	base["ASPM_S3_SECRET_KEY"] = "private-origin-canary-storage-secret"
	base["ASPM_BOOTSTRAP_TOKEN"] = "private-origin-canary-bootstrap-token"
	base["ASPM_ASSESSMENT_SCOPE"] = "not-a-delivery-scope"
	base["HTTPS_PROXY"] = "http://private-origin-canary.invalid:9"
	runtimeEnvironment(t, base)
	t.Cleanup(func() {
		db.close()
		check(t, db.accepted.Load() == 0, "configuration/preflight performed database I/O")
	})

	t.Run("unchanged-defaults-and-empty-sets", func(t *testing.T) {
		for _, value := range []string{"<unset>", "", "[]", " \n [] \t"} {
			t.Run(fmt.Sprintf("empty-%d", len(value)), func(t *testing.T) {
				t.Setenv("ASPM_JIRA_API_ORIGINS", value)
				if value == "<unset>" {
					must(t, "unset optional process field", os.Unsetenv("ASPM_JIRA_API_ORIGINS"))
				}
				config, err := service.Environment("delivery")
				must(t, "preserve default delivery environment", err)
				t.Cleanup(config.DeliveryClient.CloseIdleConnections)
				runtimeClientPolicy(t, config.DeliveryClient)
				check(t, config.SlackEndpoint == "https://slack.com" && config.DeliveryLeaseDuration == 15*time.Second &&
					config.Jobs.MaxConnections == 1 && bytes.Equal(config.IntegrationEncryptionKey, key),
					"Jira origin support changed legacy Slack/key/lease/pool defaults")
				check(t, reflect.ValueOf(config.Evidence).IsZero() && config.BootstrapToken == "" &&
					config.CollectionStorage == nil && config.CollectionClient == nil && config.AssessmentClient == nil &&
					config.AssessmentScope == "", "delivery environment inherited S3/bootstrap/AI capability")
				detector := runtimeDetect(t, "r1-default-unapproved")
				runtimeRejectDial(t, ctx, config.DeliveryClient, "tcp", detector.listener.Addr().String())
				detector.close()
				check(t, detector.accepted.Load() == 0, "empty Jira origins disabled the old Slack pin")
			})
		}
	})

	t.Run("strict-json-and-canonical-origins", func(t *testing.T) {
		tooMany := make([]string, 17)
		for i := range tooMany {
			tooMany[i] = fmt.Sprintf("https://private-origin-canary-%d.invalid", i)
		}
		raw := []struct{ name, value string }{
			{"malformed", `["private-origin-canary"`}, {"whitespace-only", " \t"},
			{"null", "null"}, {"scalar", `"private-origin-canary"`}, {"number", "7"},
			{"boolean", "true"}, {"object", `{"private-origin-canary":"not-an-array"}`},
			{"null-element", "[null]"}, {"number-element", "[7]"}, {"nested-array", "[[]]"},
			{"object-element", `[{"private-origin-canary":true}]`}, {"empty", `[""]`},
			{"trailing-json", `[] []`}, {"trailing-comma", `["https://private-origin-canary.invalid",]`},
			{"seventeen-origins", string(encoded(t, tooMany))},
			{"duplicate", `["https://private-origin-canary.invalid","https://private-origin-canary.invalid"]`},
			{"duplicate-effective-port", `["https://private-origin-canary.invalid","https://private-origin-canary.invalid:443"]`},
		}
		for _, value := range []string{
			"http://private-origin-canary.invalid", "HTTPS://private-origin-canary.invalid",
			"https://Private-origin-canary.invalid", "https://private-origin-canary.invalid.",
			"https://private-origin-canary.invalid/", "https://private-origin-canary.invalid/path",
			"https://private-origin-canary.invalid?", "https://private-origin-canary.invalid?secret=value",
			"https://private-origin-canary.invalid#", "https://private-origin-canary.invalid#fragment",
			"https://user:private-origin-canary@localhost", "https://*.private-origin-canary.invalid",
			"https://private_origin_canary.invalid", "https://private-origin-canary..invalid",
			"https://-private-origin-canary.invalid", "https://private-origin-canary-.invalid",
			"https://private-origin-canary.invalid:", "https://private-origin-canary.invalid:0",
			"https://private-origin-canary.invalid:65536", "https://private-origin-canary.invalid:0443",
			"https://private-origin-canary.invalid:+443", "https://private-origin-canary.invalid:port",
			"https://private-origin-canary.invalid/%2f", "https://%70rivate-origin-canary.invalid",
			"https://private-origin-canary.invalid\\path", " https://private-origin-canary.invalid",
			"https://private-origin-canary.invalid ", "https://private-origin-canary.invalid\x00",
			"https://127.000.0.1", "https://127.1", "https://[0:0:0:0:0:0:0:1]",
			"https://[2001:DB8::1]", "https://[::1%25zone]", "https://[::ffff:127.0.0.1]",
			"https://" + strings.Repeat("a", 64) + ".invalid",
			"https://" + strings.Repeat(strings.Repeat("a", 63)+".", 4) + "invalid",
		} {
			raw = append(raw, struct{ name, value string }{fmt.Sprintf("noncanonical-%02d", len(raw)), string(encoded(t, []string{value}))})
		}
		for _, row := range raw {
			t.Run(row.name, func(t *testing.T) {
				t.Setenv("ASPM_JIRA_API_ORIGINS", row.value)
				config, err := service.Environment("delivery")
				if config.DeliveryClient != nil {
					defer config.DeliveryClient.CloseIdleConnections()
				}
				runtimeOriginError(t, err, row.value, dbURL, base64.StdEncoding.EncodeToString(key))
				check(t, db.accepted.Load() == 0, "invalid JSON origin set opened the owned DB detector")
			})
		}
		for i, value := range []string{
			"https://localhost", "https://localhost:443", "https://localhost:1", "https://localhost:65535",
			"https://127.0.0.1", "https://127.0.0.1:8443", "https://[::1]",
			"https://[2001:db8::1]:8443", "https://xn--bcher-kva.synthetic.invalid", "https://slack.com",
		} {
			t.Run(fmt.Sprintf("canonical-%02d", i), func(t *testing.T) {
				t.Setenv("ASPM_JIRA_API_ORIGINS", string(encoded(t, []string{value})))
				config, err := service.Environment("delivery")
				must(t, "accept canonical origin without probing it", err)
				t.Cleanup(config.DeliveryClient.CloseIdleConnections)
				runtimeClientPolicy(t, config.DeliveryClient)
			})
		}
		t.Run("sixteen-unique-with-json-whitespace", func(t *testing.T) {
			t.Setenv("ASPM_JIRA_API_ORIGINS", " \n"+string(encoded(t, tooMany[:16]))+"\t ")
			config, err := service.Environment("delivery")
			must(t, "accept exactly sixteen unique explicit origins", err)
			t.Cleanup(config.DeliveryClient.CloseIdleConnections)
		})
	})

	t.Run("entire-explicit-set-real-tls-and-before-dns", func(t *testing.T) {
		n, second := newJira(t), newJira(t)
		routes := runtimeObserveNative(t, ctx, n, "r1-first")
		other := runtimeObserveNative(t, ctx, second, "r1-second")
		ca := runtimeCA(t, n.rootDER, second.rootDER)
		t.Setenv("ASPM_SLACK_ENDPOINT", routes.slack.origin())
		t.Setenv("ASPM_DELIVERY_CA_FILE", ca)
		t.Setenv("ASPM_JIRA_API_ORIGINS", string(encoded(t, []string{
			routes.jira.origin(), other.jira.origin(), routes.slack.origin(),
		})))
		config, err := service.Environment("delivery")
		must(t, "parse full trusted origin set", err)
		t.Cleanup(config.DeliveryClient.CloseIdleConnections)
		runtimeClientPolicy(t, config.DeliveryClient)
		token, otherToken, slackToken := secret(t), secret(t), secret(t)
		p, q := n.arm(token, "ok", "ok", nil), second.arm(otherToken, "ok", "ok", nil)
		t.Cleanup(func() { runtimeNativeEvidence(t, "r1-first", n, p); runtimeNativeEvidence(t, "r1-second", second, q) })
		slackArrival := n.allowSlack(slackToken, "C123")
		runtimeNativeRequest(t, ctx, config.DeliveryClient, "POST", routes.slack.origin()+"/api/chat.postMessage", slackToken,
			object{"channel": "C123", "text": "Owned environment TLS routing probe"})
		awaitCall(t, slackArrival)
		for _, item := range []struct{ origin, token string }{{routes.jira.origin(), token}, {other.jira.origin(), otherToken}} {
			runtimeNativeRequest(t, ctx, config.DeliveryClient, "GET",
				item.origin+"/ex/jira/"+cloudID+"/rest/api/3/issue/createmeta/SYN/issuetypes/10001?startAt=0&maxResults=50",
				item.token, nil)
		}
		check(t, p.gets.Load() == 1 && q.gets.Load() == 1 && p.posts.Load()+q.posts.Load() == 0 &&
			n.slackPosts.Load() == 1, "environment client did not admit the full explicit union with isolated credentials")
		unknown := runtimeDetect(t, "r1-unapproved-origin")
		var dns atomic.Int32
		prior := net.DefaultResolver
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
			dns.Add(1)
			return nil, errors.New("owned observer denied unexpected DNS")
		}}
		t.Cleanup(func() { net.DefaultResolver = prior })
		runtimeRejectDial(t, ctx, config.DeliveryClient, "tcp", unknown.listener.Addr().String())
		runtimeRejectDial(t, ctx, config.DeliveryClient, "tcp", "unapproved.synthetic.invalid:443")
		runtimeRejectDial(t, ctx, config.DeliveryClient, "udp", strings.TrimPrefix(routes.jira.origin(), "https://"))
		offline, stop := context.WithCancel(ctx)
		stop()
		runtimeRejectDial(t, offline, config.DeliveryClient, "tcp", "api.atlassian.com:443")
		runtimeRejectDial(t, offline, config.DeliveryClient, "tcp", "slack.com:443")
		unknown.close()
		check(t, unknown.accepted.Load() == 0 && dns.Load() == 0, "unapproved origin reached socket or DNS before denial")
		runtimeEvidence(t, "r1-dns", object{"resolverDialCalls": dns.Load(), "unapprovedSockets": unknown.accepted.Load()})
	})

	t.Run("programmatic-preflight-before-owned-witnesses", func(t *testing.T) {
		n := newJira(t)
		routes := runtimeObserveNative(t, ctx, n, "r1-preflight")
		t.Setenv("ASPM_SLACK_ENDPOINT", routes.slack.origin())
		t.Setenv("ASPM_DELIVERY_CA_FILE", runtimeCA(t, n.rootDER))
		t.Setenv("ASPM_JIRA_API_ORIGINS", "[]")
		baseConfig, err := service.Environment("delivery")
		must(t, "construct valid config for actual Run preflight", err)
		t.Cleanup(baseConfig.DeliveryClient.CloseIdleConnections)
		t.Cleanup(func() { runtimeNativeEvidence(t, "r1-preflight", n) })
		tooMany := make([]string, 17)
		for i := range tooMany {
			tooMany[i] = fmt.Sprintf("https://private-origin-canary-%d.invalid", i)
		}
		for i, origins := range [][]string{
			{""}, {"http://private-origin-canary.invalid"}, {"https://private-origin-canary.invalid/"},
			{"https://private-origin-canary.invalid", "https://private-origin-canary.invalid:443"}, tooMany,
		} {
			t.Run(fmt.Sprintf("invalid-slice-%d", i), func(t *testing.T) {
				config := baseConfig
				runtimeOrigins(t, &config).Set(reflect.ValueOf(origins))
				snapshot := runtimeOwnSnapshot(t, &config)
				bound, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				err := service.Run(bound, "delivery", config)
				runtimeOriginError(t, err, string(encoded(t, origins)), dbURL, base64.StdEncoding.EncodeToString(key))
				snapshot.unchanged(t, &config)
				check(t, db.accepted.Load() == 0 && routes.jira.accepted.Load() == 0 && routes.slack.accepted.Load() == 0 &&
					n.calls.Load() == 0, "Run preflight touched the owned DB/provider/listener witnesses")
			})
		}
	})
}
