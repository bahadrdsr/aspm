//go:build integration

package runtime_roles

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/bahadrdsr/aspm/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const verificationFixtureBytes = 64 << 10

var verificationWorkerID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func unsetVerificationEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if !strings.HasPrefix(upper, "ASPM_") && !strings.HasPrefix(upper, "AWS_") &&
			!strings.HasPrefix(upper, "AZURE_") && !strings.HasPrefix(upper, "OPENAI_") &&
			!strings.HasPrefix(upper, "ANTHROPIC_") && !strings.HasPrefix(upper, "GITHUB_") &&
			!strings.HasPrefix(upper, "GH_") && !strings.HasPrefix(upper, "SLACK_") {
			continue
		}
		value, present := os.LookupEnv(name)
		must(t, "clear process-local verification environment", os.Unsetenv(name))
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func setVerificationDatabaseEnvironment(t *testing.T, databaseURL, schema string) {
	t.Helper()
	t.Setenv("ASPM_DATABASE_URL", databaseURL)
	t.Setenv("ASPM_SCHEMA", schema)
}

func verificationConfigField(t *testing.T, config service.Config, name string) reflect.Value {
	t.Helper()
	field := reflect.ValueOf(config).FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("service.Config is missing the required verification runtime field %s", name)
	}
	return field
}

func verificationDuration(t *testing.T, config service.Config, name string) time.Duration {
	t.Helper()
	field := verificationConfigField(t, config, name)
	if field.Type() != reflect.TypeFor[time.Duration]() {
		t.Fatalf("service.Config.%s must be time.Duration", name)
	}
	return time.Duration(field.Int())
}

func verificationInteger(t *testing.T, config service.Config, name string) int64 {
	t.Helper()
	field := verificationConfigField(t, config, name)
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int()
	default:
		t.Fatalf("service.Config.%s must be an integer", name)
		return 0
	}
}

func setVerificationConfigField(t *testing.T, config *service.Config, name string, value any) {
	t.Helper()
	field := reflect.ValueOf(config).Elem().FieldByName(name)
	if !field.IsValid() || !field.CanSet() {
		t.Fatalf("service.Config is missing settable verification runtime field %s", name)
	}
	selected := reflect.ValueOf(value)
	if !selected.Type().AssignableTo(field.Type()) {
		t.Fatalf("verification runtime field %s has unexpected type %s", name, field.Type())
	}
	field.Set(selected)
}

func requireVerificationWorkerID(t *testing.T, value string) {
	t.Helper()
	if !verificationWorkerID.MatchString(value) {
		t.Fatalf("verification worker identity %q is not exact lower-case 32-hex", value)
	}
}

func requireSupportedVerificationEnvironment(t *testing.T) service.Config {
	t.Helper()
	config, err := service.Environment("verification")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unsupported service role") {
			t.Fatal("verification service role is not registered")
		}
		t.Fatalf("valid verification environment failed (%T; details withheld)", err)
	}
	return config
}

func TestRuntimeRolesVerificationEnvironmentIsDatabaseOnlyAndGeneratesSchemaSafeIdentity(t *testing.T) {
	unsetVerificationEnvironment(t)
	setVerificationDatabaseEnvironment(t,
		"postgres://synthetic:synthetic-password@127.0.0.1:1/verification?sslmode=disable",
		"runtime_verification_environment")

	first := requireSupportedVerificationEnvironment(t)
	second := requireSupportedVerificationEnvironment(t)
	for _, config := range []service.Config{first, second} {
		requireVerificationWorkerID(t, config.WorkerID)
		if config.WorkerID == config.Jobs.ApplicationName || config.Jobs.ApplicationName != "aspm-verification" {
			t.Fatal("durable verification worker identity reused or replaced the human-readable application name")
		}
		if config.Jobs.MaxConnections != 1 {
			t.Fatal("verification role changed the accepted one-connection service budget")
		}
		if verificationDuration(t, config, "VerificationLeaseDuration") != 90*time.Second ||
			verificationDuration(t, config, "VerificationAuthorizationInterval") != 100*time.Millisecond ||
			verificationInteger(t, config, "VerificationMaxFixtureBytes") != verificationFixtureBytes {
			t.Fatal("verification role defaults differ from the bounded V27 worker contract")
		}
		if config.Listen != "127.0.0.1:18080" || config.Workspace != "" || config.Assets != "" ||
			config.PublicOrigin != "" || config.BootstrapToken != "" || config.ReadinessKey != "" ||
			config.PrepareReadiness || config.CollectionStorage != nil ||
			config.DeliveryClient != nil || config.CollectionClient != nil || config.AssessmentClient != nil ||
			!reflect.DeepEqual(config.Evidence, evidence.Config{}) || len(config.IntegrationEncryptionKey) != 0 {
			t.Fatal("verification environment acquired storage, application, provider, target or tool authority")
		}
	}
	if first.WorkerID == second.WorkerID {
		t.Fatal("two process configuration snapshots reused a durable verification worker identity")
	}

	t.Run("explicit-bounded-overrides", func(t *testing.T) {
		t.Setenv("ASPM_DB_MAX_CONNECTIONS", "2")
		t.Setenv("ASPM_VERIFICATION_LEASE_DURATION", "1s")
		t.Setenv("ASPM_VERIFICATION_AUTHORIZATION_INTERVAL", "25ms")
		t.Setenv("ASPM_VERIFICATION_MAX_FIXTURE_BYTES", "4096")
		config := requireSupportedVerificationEnvironment(t)
		if config.Jobs.MaxConnections != 2 ||
			verificationDuration(t, config, "VerificationLeaseDuration") != time.Second ||
			verificationDuration(t, config, "VerificationAuthorizationInterval") != 25*time.Millisecond ||
			verificationInteger(t, config, "VerificationMaxFixtureBytes") != 4096 {
			t.Fatal("verification role silently clamped, defaulted or remapped explicit bounded runtime values")
		}
	})

	for _, item := range []struct{ name, value string }{
		{"ASPM_DB_MAX_CONNECTIONS", "0"},
		{"ASPM_DB_MAX_CONNECTIONS", "101"},
		{"ASPM_VERIFICATION_LEASE_DURATION", "999ms"},
		{"ASPM_VERIFICATION_LEASE_DURATION", "10m1s"},
		{"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL", "9ms"},
		{"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL", "1s1ns"},
		{"ASPM_VERIFICATION_MAX_FIXTURE_BYTES", "0"},
		{"ASPM_VERIFICATION_MAX_FIXTURE_BYTES", "65537"},
	} {
		t.Run("invalid-"+strings.ToLower(item.name)+"-"+item.value, func(t *testing.T) {
			t.Setenv(item.name, item.value)
			_, err := service.Environment("verification")
			if err == nil || strings.Contains(strings.ToLower(err.Error()), "unsupported service role") {
				t.Fatal("invalid explicit verification setting defaulted or failed only because the role is absent")
			}
		})
	}

	for _, item := range []struct{ name, value string }{
		{"ASPM_WORKER_ID", strings.Repeat("a", 32)},
		{"ASPM_WORKSPACE", strings.Repeat("b", 32)},
		{"ASPM_S3_ENDPOINT", "https://storage.synthetic.invalid"},
		{"ASPM_S3_ACCESS_KEY", "synthetic-unused-storage-key"},
		{"ASPM_S3_PREPARE_READINESS", "false"},
		{"ASPM_COLLECTION_S3_ENDPOINT", "https://collection.synthetic.invalid"},
		{"ASPM_GITHUB_ENDPOINT", "https://provider.synthetic.invalid"},
		{"ASPM_AZURE_DEVOPS_ENDPOINT", "https://target.synthetic.invalid"},
		{"ASPM_ASSESSMENT_SCOPE", "unnecessary-assessment-scope"},
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", "unnecessary-tool-key"},
		{"ASPM_PUBLIC_ORIGIN", "https://public.synthetic.invalid"},
		{"ASPM_BOOTSTRAP_TOKEN", "unnecessary-bootstrap-token"},
		{"ASPM_ASSETS", "unnecessary-assets"},
	} {
		t.Run("reject-authority-"+strings.ToLower(item.name), func(t *testing.T) {
			t.Setenv(item.name, item.value)
			_, err := service.Environment("verification")
			if err == nil || strings.Contains(strings.ToLower(err.Error()), "unsupported service role") {
				t.Fatal("verification environment accepted unnecessary storage, provider, target, application or worker identity authority")
			}
			if strings.Contains(err.Error(), item.value) {
				t.Fatal("verification environment rejection exposed the supplied authority value")
			}
		})
	}
}

func TestRuntimeRolesVerificationInvalidDirectConfigurationFailsBeforeIO(t *testing.T) {
	unsetVerificationEnvironment(t)
	setVerificationDatabaseEnvironment(t,
		"postgres://synthetic:synthetic-password@127.0.0.1:1/verification?sslmode=disable",
		"runtime_verification_preflight")
	base := requireSupportedVerificationEnvironment(t)

	database, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "create verification database I/O detector", err)
	var databaseCalls atomic.Int32
	databaseDone := make(chan struct{})
	go func() {
		defer close(databaseDone)
		for {
			connection, acceptErr := database.Accept()
			if acceptErr != nil {
				return
			}
			databaseCalls.Add(1)
			connection.Close()
		}
	}()
	t.Cleanup(func() {
		database.Close()
		<-databaseDone
	})
	var httpCalls atomic.Int32
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCalls.Add(1)
		http.Error(w, "verification preflight must not perform HTTP or storage I/O", http.StatusInternalServerError)
	}))
	t.Cleanup(storage.Close)
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve verification listener preflight witness", err)
	t.Cleanup(func() { reserved.Close() })
	const authorityCanary = "synthetic-private-authority-canary"

	cases := []struct {
		name   string
		change func(*testing.T, *service.Config)
	}{
		{"human-worker-id", func(_ *testing.T, c *service.Config) { c.WorkerID = "aspm-verification" }},
		{"lease", func(t *testing.T, c *service.Config) {
			setVerificationConfigField(t, c, "VerificationLeaseDuration", 999*time.Millisecond)
		}},
		{"authorization", func(t *testing.T, c *service.Config) {
			setVerificationConfigField(t, c, "VerificationAuthorizationInterval", 9*time.Millisecond)
		}},
		{"fixture-limit", func(t *testing.T, c *service.Config) {
			setVerificationConfigField(t, c, "VerificationMaxFixtureBytes", int64(verificationFixtureBytes+1))
		}},
		{"connection-budget", func(_ *testing.T, c *service.Config) { c.Jobs.MaxConnections = 0 }},
		{"raw-storage", func(_ *testing.T, c *service.Config) {
			c.Evidence = evidence.Config{Endpoint: storage.URL, Bucket: authorityCanary, Prefix: authorityCanary + "/",
				Region: "us-east-1", AccessKey: authorityCanary, SecretKey: authorityCanary, Timeout: time.Second}
		}},
		{"collection-storage", func(_ *testing.T, c *service.Config) {
			c.CollectionStorage = &app.StorageConfig{Endpoint: storage.URL, Bucket: authorityCanary,
				Prefix: authorityCanary + "/", Region: "us-east-1", AccessKey: authorityCanary,
				SecretKey: authorityCanary, Timeout: time.Second}
		}},
		{"provider", func(_ *testing.T, c *service.Config) { c.GitHubEndpoint = "https://provider.synthetic.invalid" }},
		{"target", func(_ *testing.T, c *service.Config) { c.PublicOrigin = "https://target.synthetic.invalid" }},
		{"tool-key", func(_ *testing.T, c *service.Config) { c.IntegrationEncryptionKey = make([]byte, 32) }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			config := base
			config.Listen = reserved.Addr().String()
			config.Jobs.DatabaseURL = "postgres://synthetic:synthetic-password@" +
				database.Addr().String() + "/verification?sslmode=disable"
			item.change(t, &config)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := service.Run(ctx, "verification", config)
			if err == nil || errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(strings.ToLower(err.Error()), "unsupported service role") {
				t.Fatal("invalid verification configuration did not fail in pure role preflight")
			}
			if databaseCalls.Load() != 0 || httpCalls.Load() != 0 {
				t.Fatalf("invalid verification configuration reached native I/O: database=%d HTTP=%d",
					databaseCalls.Load(), httpCalls.Load())
			}
			if strings.Contains(err.Error(), config.Jobs.DatabaseURL) ||
				strings.Contains(err.Error(), authorityCanary) {
				t.Fatal("verification preflight exposed private configuration")
			}
		})
	}
}

func getRoleJSON(t *testing.T, client *http.Client, address, path string, want int) map[string]string {
	t.Helper()
	response, err := client.Get("http://" + address + path)
	must(t, "call verification role "+path, err)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("verification role %s status=%d, want=%d", path, response.StatusCode, want)
	}
	if want != http.StatusOK {
		return nil
	}
	var result map[string]string
	must(t, "decode verification role "+path, json.NewDecoder(response.Body).Decode(&result))
	return result
}

func verificationRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string) map[string]int {
	t.Helper()
	result := map[string]int{}
	for _, name := range []string{"verification_evidence", "verification_approvals", "verification_jobs"} {
		var count int
		must(t, "count empty verification "+name, pool.QueryRow(ctx,
			`SELECT count(*) FROM `+pgx.Identifier{schema, "app_" + name}.Sanitize()).Scan(&count))
		result[name] = count
	}
	return result
}

func TestRuntimeRolesVerificationHealthReadinessQuiescenceAndBoundedShutdown(t *testing.T) {
	f := newFixture(t)
	unsetVerificationEnvironment(t)
	address := freeAddress(t)
	setVerificationDatabaseEnvironment(t, f.database.URL, f.database.Schema)
	t.Setenv("ASPM_LISTEN", address)
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "2")
	t.Setenv("ASPM_VERIFICATION_LEASE_DURATION", "1s")
	t.Setenv("ASPM_VERIFICATION_AUTHORIZATION_INTERVAL", "25ms")
	t.Setenv("ASPM_VERIFICATION_MAX_FIXTURE_BYTES", "65536")
	config := requireSupportedVerificationEnvironment(t)

	poolConfig, err := pgxpool.ParseConfig(f.database.URL)
	must(t, "parse verification observation pool", err)
	poolConfig.MaxConns = 2
	poolConfig.ConnConfig.ConnectTimeout = 4 * time.Second
	pool, err := pgxpool.NewWithConfig(f.ctx, poolConfig)
	must(t, "open verification observation pool", err)
	t.Cleanup(pool.Close)

	running := startRole(t, f.ctx, func(ctx context.Context) error {
		return service.Run(ctx, "verification", config)
	}, address)
	client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	if got := getRoleJSON(t, client, address, "/healthz", http.StatusOK); !reflect.DeepEqual(got, map[string]string{
		"apiVersion": jobs.Version, "service": "verification", "status": "alive",
	}) {
		t.Fatalf("verification health made extra or missing claims: %#v", got)
	}
	if got := getRoleJSON(t, client, address, "/readyz", http.StatusOK); !reflect.DeepEqual(got, map[string]string{
		"service": "verification", "status": "ready", "database": "reachable",
		"schema": "compatible", "storage": "not-required", "pipeline": "inspect-job-state-separately",
	}) {
		t.Fatalf("verification readiness made dishonest processing, safety, provider, target or storage claims: %#v", got)
	}
	for _, path := range []string{"/", "/api", "/api/v1/session", "/metrics"} {
		getRoleJSON(t, client, address, path, http.StatusNotFound)
	}
	var version int
	must(t, "read verification-compatible migration version", pool.QueryRow(f.ctx,
		`SELECT max(version) FROM `+pgx.Identifier{f.database.Schema, "app_schema_versions"}.Sanitize()).Scan(&version))
	if version != 27 {
		t.Fatalf("verification readiness started on schema version %d, want exact V27", version)
	}
	before := verificationRows(t, f.ctx, pool, f.database.Schema)
	time.Sleep(550 * time.Millisecond)
	after := verificationRows(t, f.ctx, pool, f.database.Schema)
	if !reflect.DeepEqual(before, map[string]int{
		"verification_evidence": 0, "verification_approvals": 0, "verification_jobs": 0,
	}) || !reflect.DeepEqual(after, before) {
		t.Fatal("empty verification queue did not remain quiescent")
	}
	stopRole(t, running)
	for role, tap := range f.taps {
		if len(tap.snapshot()) != 0 {
			t.Fatalf("database-only verification role performed %s storage I/O", role)
		}
	}

	t.Run("newer-schema-never-listens", func(t *testing.T) {
		futureSchema := "runtime_verification_future_" + jobs.NewID()[:16]
		_, err := pool.Exec(f.ctx, `CREATE SCHEMA `+pgx.Identifier{futureSchema}.Sanitize()+`;
			CREATE TABLE `+pgx.Identifier{futureSchema, "app_schema_versions"}.Sanitize()+` (version integer PRIMARY KEY);
			INSERT INTO `+pgx.Identifier{futureSchema, "app_schema_versions"}.Sanitize()+` (version) VALUES (28)`)
		must(t, "create owned future verification schema", err)
		t.Cleanup(func() {
			_, dropErr := pool.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{futureSchema}.Sanitize()+` CASCADE`)
			if dropErr != nil {
				t.Errorf("drop owned future verification schema failed (%T)", dropErr)
			}
		})
		unsetVerificationEnvironment(t)
		futureAddress := freeAddress(t)
		setVerificationDatabaseEnvironment(t, f.database.URL, futureSchema)
		t.Setenv("ASPM_LISTEN", futureAddress)
		t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
		future := requireSupportedVerificationEnvironment(t)
		ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
		err = service.Run(ctx, "verification", future)
		cancel()
		if err == nil || strings.Contains(strings.ToLower(err.Error()), "unsupported service role") {
			t.Fatal("verification runtime accepted a schema newer than this binary")
		}
		listener, listenErr := net.Listen("tcp", futureAddress)
		if listenErr != nil {
			t.Fatal("schema-incompatible verification startup left a partial HTTP listener")
		}
		listener.Close()
	})
}
