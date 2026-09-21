//go:build integration

package assessment_runtime

import (
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

var assessmentVariables = []string{
	"ASPM_ASSESSMENT_SCOPE", "ASPM_ASSESSMENT_LEASE_DURATION", "ASPM_ASSESSMENT_AUTHORIZATION_INTERVAL",
	"ASPM_ASSESSMENT_REQUEST_TIMEOUT", "ASPM_ASSESSMENT_REQUEST_WINDOW", "ASPM_ASSESSMENT_MAX_CONCURRENT",
	"ASPM_ASSESSMENT_REQUESTS_PER_WINDOW", "ASPM_ASSESSMENT_MAX_INPUT_BYTES", "ASPM_ASSESSMENT_MAX_OUTPUT_TOKENS",
	"ASPM_ASSESSMENT_MAX_RESPONSE_BYTES", "ASPM_ASSESSMENT_CA_FILE",
}

func unset(t *testing.T, name string) {
	t.Helper()
	value, present := os.LookupEnv(name)
	must(t, "unset only process-local test environment", os.Unsetenv(name))
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
func baseEnvironment(t *testing.T, db app.DatabaseConfig) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_") && !strings.HasPrefix(name, "ASPM_ASSESSMENT_RUNTIME_") {
			unset(t, name)
		}
	}
	t.Setenv("ASPM_DATABASE_URL", db.DatabaseURL)
	t.Setenv("ASPM_SCHEMA", db.Schema)
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "3")
}
func rawEnvironment(t *testing.T, c evidence.Config) {
	t.Helper()
	for name, value := range map[string]string{"ASPM_S3_ENDPOINT": c.Endpoint, "ASPM_S3_BUCKET": c.Bucket, "ASPM_S3_PREFIX": c.Prefix,
		"ASPM_S3_REGION": c.Region, "ASPM_S3_ACCESS_KEY": c.AccessKey, "ASPM_S3_SECRET_KEY": c.SecretKey} {
		t.Setenv(name, value)
	}
}
func workerEnvironment(t *testing.T, f *fixture, n *nativeFixture, key []byte) runtimeConfig {
	t.Helper()
	check(t, Production.Environment != nil, "BLOCKED: real assessment environment binding missing")
	baseEnvironment(t, f.database)
	unset(t, "ASPM_DB_MAX_CONNECTIONS")
	t.Setenv("ASPM_LISTEN", ownedAddress(t))
	t.Setenv("ASPM_ASSESSMENT_SCOPE", f.scope)
	t.Setenv("ASPM_ASSESSMENT_CA_FILE", n.ca)
	if key == nil {
		unset(t, "ASPM_INTEGRATION_ENCRYPTION_KEY")
	} else {
		t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	}
	c, err := Production.Environment("assessment")
	must(t, "parse actual assessment environment", err)
	check(t, c.Scope == f.scope && c.Database.MaxConnections == 1, "scope/default independent pool was not selected")
	check(t, c.Client != nil, "environment omitted explicit normal-TLS client")
	t.Cleanup(c.Client.CloseIdleConnections)
	return c
}
func defaults(c runtimeConfig) bool {
	return c.Lease == 15*time.Second && c.AuthorizationInterval == 100*time.Millisecond && c.RequestTimeout == 10*time.Second && c.Window == time.Minute &&
		c.MaxConcurrent == 1 && c.RequestsPerWindow == 30 && c.MaxInput == 32768 && c.MaxOutput == 1024 && c.MaxResponse == 65536
}
func workerOnly(t *testing.T, c runtimeConfig) {
	t.Helper()
	check(t, c.Raw.Endpoint == "" && c.Raw.Bucket == "" && c.Raw.Prefix == "" && c.Raw.AccessKey == "" && c.Raw.SecretKey == "" &&
		c.CollectionStorage == nil && c.Bootstrap == "" && c.Assets == "" && c.ReadinessKey == "" && c.NormalizedPrefix == "" && !c.PrepareReadiness &&
		c.DeliveryClient == nil && c.CollectionClient == nil && c.SlackEndpoint == "" && c.GitHubEndpoint == "", "assessment inherited core/storage/provider-gateway capabilities")
}
func assertClient(t *testing.T, c runtimeConfig) {
	t.Helper()
	check(t, c.Client != nil && c.Client != http.DefaultClient && c.Client.Timeout > 0 && c.Client.Timeout <= 30*time.Second &&
		c.Client.Jar == nil && c.Client.CheckRedirect != nil, "explicit bounded client/no cookie/redirect policy missing")
	tr, ok := c.Client.Transport.(*http.Transport)
	check(t, ok && tr != nil && tr.Proxy == nil && tr.DialTLS == nil && tr.DialTLSContext == nil && tr.DialContext != nil &&
		tr.TLSClientConfig != nil && !tr.TLSClientConfig.InsecureSkipVerify && tr.TLSClientConfig.ServerName == "" &&
		tr.TLSClientConfig.MinVersion >= 0x0303, "normal TLS/direct transport selection weakened")
}
func selectedLimitsEnvironment(t *testing.T, c runtimeConfig) {
	for name, value := range map[string]string{
		"ASPM_ASSESSMENT_LEASE_DURATION": c.Lease.String(), "ASPM_ASSESSMENT_AUTHORIZATION_INTERVAL": c.AuthorizationInterval.String(),
		"ASPM_ASSESSMENT_REQUEST_TIMEOUT": c.RequestTimeout.String(), "ASPM_ASSESSMENT_REQUEST_WINDOW": c.Window.String(),
		"ASPM_ASSESSMENT_MAX_CONCURRENT": strconv.Itoa(c.MaxConcurrent), "ASPM_ASSESSMENT_REQUESTS_PER_WINDOW": strconv.Itoa(c.RequestsPerWindow),
		"ASPM_ASSESSMENT_MAX_INPUT_BYTES": strconv.Itoa(c.MaxInput), "ASPM_ASSESSMENT_MAX_OUTPUT_TOKENS": strconv.Itoa(c.MaxOutput),
		"ASPM_ASSESSMENT_MAX_RESPONSE_BYTES": strconv.FormatInt(c.MaxResponse, 10),
	} {
		t.Setenv(name, value)
	}
}
