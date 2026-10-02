//go:build integration && teams_runtime

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/service"
)

func teamsRuntimeNativePOST(t *testing.T, ctx context.Context, client *http.Client, target string) {
	t.Helper()
	p := payload{Title: "Owned environment TLS probe", Body: "Not a user delivery intent.", DeepLink: origin + "/work"}
	bound, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(bound, "POST", target, bytes.NewReader(encoded(t, teamsCard(p))))
	must(t, "construct signed-target Environment probe", err)
	request.Header.Set("Content-Type", "application/json")
	request.GetBody = nil
	response, err := client.Do(request)
	must(t, "RED: Environment must admit the separately approved Teams TLS origin", err)
	defer response.Body.Close()
	size, err := io.Copy(io.Discard, io.LimitReader(response.Body, (64<<10)+1))
	must(t, "consume bounded native response without secret readback", err)
	check(t, response.StatusCode == 202 && size <= 64<<10, "owned Teams TLS probe did not return native 202")
}

func TestTeamsRuntimeR1EnvironmentPreflightUnionAndCallerPolicy(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	routes := teamsRuntimeObserve(t, h.ctx, n, "tr1")
	ca := runtimeCA(t, n.rootDER, n.common.rootDER)
	db := runtimeDetect(t, "tr1-preflight-db")
	unknown := runtimeDetect(t, "tr1-unapproved-socket")
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve conflicting preflight listener", err)
	t.Cleanup(func() { _ = reserved.Close() })
	dbURL := (&url.URL{Scheme: "postgres", User: url.UserPassword("runtime", "private-origin-canary-db"),
		Host: db.listener.Addr().String(), Path: "/runtime", RawQuery: "sslmode=disable"}).String()
	base := teamsRuntimeSettings(t, dbURL, "teams_runtime_preflight", reserved.Addr().String(),
		h.cfg.IntegrationEncryptionKey, "", "", nil, nil)
	delete(base, "ASPM_DELIVERY_LEASE_DURATION")
	base["ASPM_S3_ENDPOINT"] = "private-origin-canary-invalid-storage"
	base["ASPM_S3_SECRET_KEY"] = "private-origin-canary-storage-secret"
	base["ASPM_S3_PREPARE_READINESS"] = "not-a-delivery-setting"
	base["ASPM_BOOTSTRAP_TOKEN"] = "private-origin-canary-bootstrap"
	base["ASPM_ASSESSMENT_SCOPE"] = "not-a-delivery-scope"
	base["HTTPS_PROXY"] = "http://private-origin-canary.invalid:9"
	runtimeEnvironment(t, base)
	t.Cleanup(func() {
		db.close()
		unknown.close()
		check(t, db.accepted.Load() == 0 && unknown.accepted.Load() == 0,
			"preflight or default policy touched an unapproved DB/native socket")
	})

	t.Run("unset-empty-and-legacy-defaults", func(t *testing.T) {
		for _, value := range []string{"<unset>", "", "[]", " \n[] \t"} {
			t.Setenv(teamsOriginsEnvironment, value)
			if value == "<unset>" {
				must(t, "unset optional Teams process field", os.Unsetenv(teamsOriginsEnvironment))
			}
			config, err := service.Environment("delivery")
			must(t, "preserve delivery defaults with no Teams selection", err)
			t.Cleanup(config.DeliveryClient.CloseIdleConnections)
			runtimeClientPolicy(t, config.DeliveryClient)
			check(t, config.SlackEndpoint == "https://slack.com" && len(config.JiraAPIOrigins) == 0 &&
				config.DeliveryLeaseDuration == 15*time.Second && config.Jobs.MaxConnections == 1 &&
				bytes.Equal(config.IntegrationEncryptionKey, h.cfg.IntegrationEncryptionKey),
				"Teams selection changed Slack/Jira/key/lease/pool defaults")
			check(t, reflect.ValueOf(config.Evidence).IsZero() && config.BootstrapToken == "" &&
				config.CollectionStorage == nil && config.CollectionClient == nil &&
				config.AssessmentClient == nil && config.AssessmentScope == "",
				"delivery inherited unrelated S3/bootstrap/assessment authority")
			field := reflect.ValueOf(&config).Elem().FieldByName("TeamsWorkflowOrigins")
			if field.IsValid() {
				check(t, field.Type() == reflect.TypeOf([]string{}) && field.Len() == 0,
					"an empty Teams setting invented an implicit origin")
			}
			runtimeRejectDial(t, h.ctx, config.DeliveryClient, "tcp", unknown.listener.Addr().String())
			offline, stop := context.WithCancel(h.ctx)
			stop()
			for _, address := range []string{"api.atlassian.com:443", "prod-00.westus.logic.azure.com:443", "workflow.example.invalid:443"} {
				runtimeRejectDial(t, offline, config.DeliveryClient, "tcp", address)
			}
		}
	})

	tooMany := make([]string, 17)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("https://private-origin-canary-%d.invalid", i)
	}
	t.Run("strict-json-canonical-and-field-specific", func(t *testing.T) {
		cases := []struct{ name, value string }{
			{"malformed", `["private-origin-canary"`}, {"whitespace-only", " \t"},
			{"null", "null"}, {"scalar", `"private-origin-canary"`}, {"number", "7"}, {"boolean", "true"},
			{"object", `{"private-origin-canary":true}`}, {"null-element", "[null]"}, {"number-element", "[7]"},
			{"boolean-element", "[false]"}, {"nested", "[[]]"}, {"object-element", "[{}]"},
			{"trailing-json", "[] []"}, {"empty-origin", `[""]`},
			{"duplicate", `["https://private-origin-canary.invalid","https://private-origin-canary.invalid"]`},
			{"effective-duplicate", `["https://private-origin-canary.invalid","https://private-origin-canary.invalid:443"]`},
			{"seventeen", string(encoded(t, tooMany))},
		}
		for i, value := range []string{
			"http://private-origin-canary.invalid", "HTTPS://private-origin-canary.invalid",
			"https://Private-origin-canary.invalid", "https://private-origin-canary.invalid.",
			"https://private-origin-canary.invalid/", "https://private-origin-canary.invalid?sig=private-origin-canary",
			"https://private-origin-canary.invalid#secret", "https://private-origin-canary@localhost",
			"https://*.private-origin-canary.invalid", "https://private_origin_canary.invalid",
			"https://private-origin-canary.invalid:0443", "https://private-origin-canary.invalid:65536",
			"https://private-origin-canary.invalid\\path", "https://private-origin-canary.invalid\x00",
			"https://127.000.0.1", "https://[0:0:0:0:0:0:0:1]", "https://[::ffff:127.0.0.1]",
		} {
			cases = append(cases, struct{ name, value string }{fmt.Sprintf("canonical-vector-%02d", i), string(encoded(t, []string{value}))})
		}
		for _, row := range cases {
			t.Run(row.name, func(t *testing.T) {
				t.Setenv(teamsOriginsEnvironment, row.value)
				config, err := service.Environment("delivery")
				if config.DeliveryClient != nil {
					defer config.DeliveryClient.CloseIdleConnections()
				}
				teamsRuntimeOriginError(t, err, row.value, dbURL, base64.StdEncoding.EncodeToString(h.cfg.IntegrationEncryptionKey))
				check(t, db.accepted.Load() == 0 && n.calls.Load() == 0,
					"invalid Teams setting performed DB/native I/O")
			})
		}
		for i, origins := range [][]string{
			{"https://localhost:1", "https://127.0.0.1:65535", "https://[2001:db8::1]:8443"},
			tooMany[:16],
		} {
			t.Run(fmt.Sprintf("canonical-boundary-%d", i), func(t *testing.T) {
				t.Setenv(teamsOriginsEnvironment, " \n"+string(encoded(t, origins))+"\t ")
				config, err := service.Environment("delivery")
				must(t, "accept canonical Teams boundary without native probing", err)
				t.Cleanup(config.DeliveryClient.CloseIdleConnections)
				runtimeClientPolicy(t, config.DeliveryClient)
			})
		}
		t.Run("independent-list-bounds-and-cross-profile-port-alias", func(t *testing.T) {
			jira := make([]string, 16)
			for i := range jira {
				jira[i] = fmt.Sprintf("https://jira-%d.example.invalid", i)
			}
			for _, selection := range []struct{ jira, teams []string }{
				{jira, tooMany[:16]},
				{[]string{"https://shared.example.invalid"}, []string{"https://shared.example.invalid:443"}},
			} {
				t.Setenv("ASPM_JIRA_API_ORIGINS", string(encoded(t, selection.jira)))
				t.Setenv(teamsOriginsEnvironment, string(encoded(t, selection.teams)))
				config, err := service.Environment("delivery")
				must(t, "admit independent explicit lists without a combined cap or cross-profile duplicate error", err)
				t.Cleanup(config.DeliveryClient.CloseIdleConnections)
			}
		})
		t.Run("existing-jira-error-name", func(t *testing.T) {
			t.Setenv(teamsOriginsEnvironment, `["https://teams.example.invalid"]`)
			t.Setenv("ASPM_JIRA_API_ORIGINS", `["http://private-origin-canary.invalid"]`)
			_, err := service.Environment("delivery")
			runtimeOriginError(t, err, os.Getenv("ASPM_JIRA_API_ORIGINS"))
			message := strings.ToLower(err.Error())
			check(t, !strings.Contains(message, "aspm_teams_workflow_origins") && !strings.Contains(message, "teamsworkfloworigins"),
				"valid Teams selection changed the existing Jira-specific diagnostic")
		})
	})

	t.Run("real-tls-union-and-before-dns", func(t *testing.T) {
		jira := []string{routes.common.jira.origin()}
		teams := []string{routes.teams.origin(), routes.common.jira.origin(), routes.common.slack.origin(),
			"https://teams-network-family.synthetic.invalid"}
		runtimeEnvironment(t, teamsRuntimeSettings(t, dbURL, "teams_runtime_preflight", reserved.Addr().String(),
			h.cfg.IntegrationEncryptionKey, routes.common.slack.origin(), ca, jira, teams))
		config, err := service.Environment("delivery")
		must(t, "accept cross-profile duplicate as a network union", err)
		t.Cleanup(config.DeliveryClient.CloseIdleConnections)
		value := n.workflowURL()
		h.rememberTeamsURL(value)
		plan := n.arm(value, "accepted", nil)
		teamsRuntimeNativePOST(t, h.ctx, config.DeliveryClient, value)
		awaitCall(t, plan.arrived)
		token, slackToken := secret(t), secret(t)
		h.remember(token)
		h.remember(slackToken)
		p := n.common.arm(token, "ok", "ok", nil)
		t.Cleanup(func() { runtimeNativeEvidence(t, "tr1", n.common, p) })
		slack := n.common.allowSlack(slackToken, "C123")
		runtimeNativeRequest(t, h.ctx, config.DeliveryClient, "GET",
			routes.common.jira.origin()+"/ex/jira/"+cloudID+"/rest/api/3/issue/createmeta/SYN/issuetypes/10001?startAt=0&maxResults=50", token, nil)
		runtimeNativeRequest(t, h.ctx, config.DeliveryClient, "POST", routes.common.slack.origin()+"/api/chat.postMessage",
			slackToken, object{"channel": "C123", "text": "Owned union probe"})
		awaitCall(t, slack)
		same(t, "Teams Environment rewrote caller origin order", teamsRuntimeOrigins(t, &config).Interface(), teams)
		same(t, "Teams Environment rewrote Jira origin order", config.JiraAPIOrigins, jira)
		var dns atomic.Int32
		prior := net.DefaultResolver
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
			dns.Add(1)
			return nil, errors.New("owned observer denied unexpected DNS")
		}}
		t.Cleanup(func() { net.DefaultResolver = prior })
		runtimeRejectDial(t, h.ctx, config.DeliveryClient, "tcp", unknown.listener.Addr().String())
		runtimeRejectDial(t, h.ctx, config.DeliveryClient, "tcp", "unapproved.synthetic.invalid:443")
		runtimeRejectDial(t, h.ctx, config.DeliveryClient, "udp", "teams-network-family.synthetic.invalid:443")
		runtimeRejectDial(t, h.ctx, config.DeliveryClient, "unix", routes.teams.address)
		check(t, dns.Load() == 0 && unknown.accepted.Load() == 0 && plan.calls.Load() == 1 &&
			p.gets.Load() == 1 && p.posts.Load() == 0 && n.common.slackPosts.Load() == 1,
			"Environment lost complete origin admission, credential isolation or pre-DNS/socket denial")
		runtimeEvidence(t, "tr1-environment", object{"resolverDialCalls": dns.Load(), "unapprovedSockets": unknown.accepted.Load(),
			"teamsPOST": plan.calls.Load(), "jiraGET": p.gets.Load(), "slackPOST": n.common.slackPosts.Load()})
	})

	t.Run("run-invalid-slices-before-io", func(t *testing.T) {
		runtimeEnvironment(t, teamsRuntimeSettings(t, dbURL, "teams_runtime_preflight", reserved.Addr().String(),
			h.cfg.IntegrationEncryptionKey, routes.common.slack.origin(), ca, nil, []string{}))
		baseConfig, err := service.Environment("delivery")
		must(t, "construct normal configuration for programmatic Run preflight", err)
		t.Cleanup(baseConfig.DeliveryClient.CloseIdleConnections)
		for i, origins := range [][]string{{""}, {"http://private-origin-canary.invalid"},
			{"https://private-origin-canary.invalid", "https://private-origin-canary.invalid:443"}, tooMany} {
			t.Run(fmt.Sprintf("invalid-slice-%d", i), func(t *testing.T) {
				config := baseConfig
				teamsRuntimeOrigins(t, &config).Set(reflect.ValueOf(origins))
				unchanged := teamsRuntimeOwnSnapshot(t, &config)
				before := routes.teams.accepted.Load() + routes.common.slack.accepted.Load()
				bound, cancel := context.WithTimeout(h.ctx, time.Second)
				err := service.Run(bound, "delivery", config)
				cancel()
				teamsRuntimeOriginError(t, err, string(encoded(t, origins)), dbURL)
				unchanged()
				check(t, db.accepted.Load() == 0 &&
					routes.teams.accepted.Load()+routes.common.slack.accepted.Load() == before,
					"Run reached DB/native/listener before origin validation")
			})
		}
	})

	finding := h.seed(h.admin, "Teams runtime narrower caller")
	value := n.workflowURL()
	c := h.teamsConnection(h.admin, value)
	queued := h.teamsQueue(h.admin, h.teamsPreview(h.admin, finding.ID, c), "tr1-narrow-caller", 202)
	plan := n.arm(value, "accepted", func() error { return h.marker(queued.ID) })
	database, pg := runtimeDatabase(h, "tr1-narrow")
	config := teamsRuntimeConfig(t, h, database, routes.common.slack.origin(), ca,
		[]string{routes.common.jira.origin()}, []string{routes.teams.origin()})
	transport := runtimeClientPolicy(t, config.DeliveryClient)
	baseDial := transport.DialContext
	var rejected, forwarded atomic.Int32
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == routes.teams.address {
			rejected.Add(1)
			return nil, errors.New("caller denied this operator-approved Teams origin")
		}
		forwarded.Add(1)
		return baseDial(ctx, network, address)
	}
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig.MinVersion = 0
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSClientConfig.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256}
	unchanged := teamsRuntimeOwnSnapshot(t, &config)
	beforeSockets, beforeHTTP := routes.teams.accepted.Load(), n.calls.Load()
	role := runtimeStartService(h, config, "tr1-narrow")
	done := teamsRuntimeAwaitTerminal(h, h.admin, queued.ID)
	teamsRuntimeRefused(t, queued, done, false)
	runtimeAwaitClaims(t, h.ctx, pg, pg.claims.Load(), role.done)
	role.stop()
	pg.close()
	check(t, rejected.Load() == 1 && forwarded.Load() == 0 && plan.calls.Load() == 0 &&
		routes.teams.accepted.Load() == beforeSockets && n.calls.Load() == beforeHTTP,
		"Run bypassed the narrower caller dialer or retained an inner Jira/Slack-only gate")
	unchanged()
	runtimeEvidence(t, "tr1-caller-policy", object{"callerRefusals": rejected.Load(), "callerForwards": forwarded.Load()})
	h.noStoredSecrets()
	t.Log("REACHED: Teams Environment/preflight/network union and preserved narrower actual Run caller policy")
}
