//go:build integration

package delivery_runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func webhookEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range []string{
		"ASPM_DATABASE_URL", "ASPM_SCHEMA", "ASPM_DB_MAX_CONNECTIONS",
		"ASPM_INTEGRATION_ENCRYPTION_KEY", "ASPM_SLACK_ENDPOINT",
		"ASPM_JIRA_API_ORIGINS", "ASPM_TEAMS_WORKFLOW_ORIGINS",
		"ASPM_WEBHOOK_ORIGINS", "ASPM_DELIVERY_CA_FILE",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
	} {
		t.Setenv(entry, "")
	}
	t.Setenv("ASPM_DATABASE_URL", "postgres://synthetic:"+id(t)+"@127.0.0.1:15432/fixture?sslmode=disable")
	t.Setenv("ASPM_SCHEMA", "delivery_webhook_environment")
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(random(t, 32)))
}

func TestDeliveryRuntimeV22WebhookOriginsAreExplicitCanonicalAndRoleScoped(t *testing.T) {
	require(t, Production.Environment != nil, "real service.Environment delivery binding missing")
	webhookEnvironment(t)
	allowed, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned explicitly allowed webhook address", err)
	t.Cleanup(func() { _ = allowed.Close() })
	allowedDone := make(chan struct{})
	go func() {
		defer close(allowedDone)
		connection, acceptErr := allowed.Accept()
		if acceptErr == nil {
			_ = connection.Close()
		}
	}()
	origin := "https://" + allowed.Addr().String()
	raw, err := json.Marshal([]string{origin})
	must(t, "encode explicit webhook origin list", err)
	t.Setenv("ASPM_WEBHOOK_ORIGINS", string(raw))

	config, err := Production.Environment("delivery")
	must(t, "parse explicit delivery webhook origins", err)
	require(t, reflect.DeepEqual(config.WebhookOrigins, []string{origin}),
		"delivery role did not receive the exact operator-owned webhook origin list")
	checkEnvironmentClient(t, config)
	transport := config.Client.Transport.(*http.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := transport.DialContext(ctx, "tcp", allowed.Addr().String())
	must(t, "dial only the explicitly allowed synthetic webhook address", err)
	_ = connection.Close()
	_ = allowed.Close()
	<-allowedDone

	unapproved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned unapproved webhook detector", err)
	address := unapproved.Addr().String()
	var accepted bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = unapproved.(*net.TCPListener).SetDeadline(time.Now().Add(300 * time.Millisecond))
		c, acceptErr := unapproved.Accept()
		if acceptErr == nil {
			accepted = true
			_ = c.Close()
		}
	}()
	c, dialErr := transport.DialContext(ctx, "tcp", address)
	if c != nil {
		_ = c.Close()
	}
	_ = unapproved.Close()
	<-done
	require(t, dialErr != nil && !accepted && !errors.Is(dialErr, context.DeadlineExceeded),
		"delivery transport dialed a host or port outside the exact webhook allowlist")

	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "2")
	t.Setenv("ASPM_S3_ENDPOINT", "http://127.0.0.1:18333")
	t.Setenv("ASPM_S3_BUCKET", "synthetic-existing-bucket")
	t.Setenv("ASPM_S3_ACCESS_KEY", secret(t))
	t.Setenv("ASPM_S3_SECRET_KEY", secret(t))
	t.Setenv("ASPM_S3_PREFIX", "evidence/")
	t.Setenv("ASPM_S3_REGION", "us-east-1")
	core, err := Production.Environment("core")
	must(t, "parse explicit core webhook origins", err)
	require(t, reflect.DeepEqual(core.WebhookOrigins, []string{origin}),
		"core role did not receive the same explicit webhook origin list")

	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
	reports, err := Production.Environment("reports")
	must(t, "preserve reports environment parsing", err)
	require(t, len(reports.WebhookOrigins) == 0,
		"non-core/non-delivery role received outbound webhook capability")
}

func TestDeliveryRuntimeV22WebhookOriginInputHasNoAmbientDefaultOrCanonicalAlias(t *testing.T) {
	require(t, Production.Environment != nil, "real service.Environment delivery binding missing")
	webhookEnvironment(t)
	config, err := Production.Environment("delivery")
	must(t, "parse delivery environment without webhook origins", err)
	require(t, len(config.WebhookOrigins) == 0,
		"missing ASPM_WEBHOOK_ORIGINS created an ambient webhook destination")

	tooMany := make([]string, 17)
	for index := range tooMany {
		tooMany[index] = "https://allowed-" + strconv.Itoa(index) + ".synthetic.invalid"
	}
	encodedTooMany, err := json.Marshal(tooMany)
	must(t, "encode over-limit origin list", err)
	for _, row := range []struct {
		name, value string
	}{
		{name: "invalid-json", value: `[`},
		{name: "null", value: `null`},
		{name: "object", value: `{}`},
		{name: "too-many", value: string(encodedTooMany)},
		{name: "duplicate", value: `["https://receiver.synthetic.invalid","https://receiver.synthetic.invalid"]`},
		{name: "duplicate-effective-port", value: `["https://receiver.synthetic.invalid","https://receiver.synthetic.invalid:443"]`},
		{name: "http", value: `["http://receiver.synthetic.invalid"]`},
		{name: "wildcard", value: `["https://*.synthetic.invalid"]`},
		{name: "userinfo", value: `["https://user@receiver.synthetic.invalid"]`},
		{name: "path", value: `["https://receiver.synthetic.invalid/hook"]`},
		{name: "query", value: `["https://receiver.synthetic.invalid?secret=x"]`},
		{name: "fragment", value: `["https://receiver.synthetic.invalid#fragment"]`},
		{name: "uppercase", value: `["https://RECEIVER.synthetic.invalid"]`},
		{name: "trailing-slash", value: `["https://receiver.synthetic.invalid/"]`},
		{name: "noncanonical-port", value: `["https://receiver.synthetic.invalid:0443"]`},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("ASPM_WEBHOOK_ORIGINS", row.value)
			_, err := Production.Environment("delivery")
			require(t, err != nil, "invalid webhook origin declaration was accepted")
		})
	}
}
