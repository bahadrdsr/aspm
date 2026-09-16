//go:build integration

package runtime_roles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/service"
)

func noBucketProbes(t *testing.T, tap *storageTap, bucket string) {
	t.Helper()
	for _, call := range tap.snapshot() {
		if call.key == "/"+bucket || call.key == "" {
			t.Errorf("main role attempted a whole-bucket %s probe (actual storage status %d)", call.method, call.status)
		}
	}
}

func stopRole(t *testing.T, role *runningRole) {
	t.Helper()
	role.cancel()
	select {
	case <-role.done:
		if role.err != nil && !errors.Is(role.err, context.Canceled) {
			t.Fatalf("role shutdown failed (%T; details withheld)", role.err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("role shutdown exceeded its bound")
	}
}

func TestRuntimeRolesCoreAndIngestionConsumeScopedStorage(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(func() {
		noBucketProbes(t, f.taps["core"], f.bucket)
		noBucketProbes(t, f.taps["ingestion"], f.bucket)
	})
	ingestionClient := s3Client(t, f.store("ingestion"))
	_, err := objectBytes(f.ctx, ingestionClient, f.bucket, f.rawPrefix+"readiness.txt")
	must(t, "ingestion permitted readiness-object read", err)
	denied, err := ingestionClient.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.deniedKey)})
	if denied != nil && denied.Body != nil {
		must(t, "close unexpected denied read", denied.Body.Close())
	}
	var status interface{ HTTPStatusCode() int }
	var apiError smithy.APIError
	if !errors.As(err, &status) || status.HTTPStatusCode() != 403 || !errors.As(err, &apiError) || apiError.ErrorCode() != "AccessDenied" {
		t.Fatal("ingestion unapproved-prefix read must be denied by actual S3 with HTTP 403 / AccessDenied")
	}
	t.Log("ingestion key: allowed raw readiness read; real S3 denied approved-prefix GetObject with 403/AccessDenied")
	core := f.core(t)
	core.json(t, "GET", "/api/v1/session", nil, "", 401, nil)
	assetID := f.enroll(t, core)
	report := "{\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"synthetic-role-scanner\"}},\"results\":[{\"ruleId\":\"synthetic-runtime-rule\",\"level\":\"warning\",\"message\":{\"text\":\"Synthetic exact role report.\"},\"locations\":[{\"physicalLocation\":{\"artifactLocation\":{\"uri\":\"src/synthetic.go\"},\"region\":{\"startLine\":12}}}]}]}]}\r\n"
	if !json.Valid([]byte(report)) {
		t.Fatal("synthetic SARIF fixture is not valid JSON")
	}
	var accepted struct {
		Import app.Import `json:"import"`
	}
	core.json(t, "POST", "/api/v1/imports", map[string]any{
		"apiVersion": app.APIVersion, "assetId": assetID, "format": "sarif", "report": report,
		"sourceId": "synthetic-runtime-source", "scanId": "synthetic-runtime-scan",
		"scope":        map[string]string{"id": "synthetic-runtime-scope", "revision": "1", "branch": "main"},
		"sourceScanAt": "2026-09-01T08:00:00Z", "collectedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete",
	}, "", 202, &accepted)
	if accepted.Import.ID == "" || accepted.Import.RunID == "" || accepted.Import.State != "queued" {
		t.Fatal("core did not durably acknowledge queued raw intake")
	}
	var rawKey string
	for _, call := range f.taps["core"].snapshot() {
		if call.method == "PUT" && call.status >= 200 && call.status < 300 && strings.HasPrefix(call.key, f.rawPrefix) {
			rawKey = call.key
		}
	}
	if rawKey == "" {
		t.Fatal("core did not publish raw evidence with the supplied core signing identity")
	}
	stored, err := objectBytes(f.ctx, f.admin, f.bucket, rawKey)
	must(t, "independently read core-published raw bytes", err)
	if !bytes.Equal(stored, []byte(report)) {
		t.Fatal("core changed acknowledged raw report bytes")
	}
	before := len(f.taps["ingestion"].snapshot())
	if Production.Ingestion == nil {
		t.Fatal("runtime role binding missing: Ingestion")
	}
	address := freeAddress(t)
	worker := Production.Ingestion(IngestionConfig{Database: f.database, Raw: f.store("ingestion"), NormalizedPrefix: f.normalizedPrefix, Listen: address})
	if worker == nil {
		t.Fatal("Ingestion factory returned nil")
	}
	running := startRole(t, f.ctx, worker.Run, address)
	deadline := time.Now().Add(12 * time.Second)
	var final struct {
		Import app.Import `json:"import"`
	}
	for {
		core.json(t, "GET", "/api/v1/imports/"+accepted.Import.ID, nil, "", 200, &final)
		if final.Import.State == "succeeded" || final.Import.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("independent ingestion did not finish the real queued report")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopRole(t, running)
	if final.Import.State != "succeeded" || final.Import.RunID != accepted.Import.RunID || final.Import.ObservationCount != 1 {
		t.Fatal("independent ingestion did not preserve and successfully reconcile acknowledged work")
	}
	read := false
	for _, call := range f.taps["ingestion"].snapshot()[before:] {
		if call.method == "GET" && call.key == rawKey && call.status == 200 {
			read = true
		}
		if call.method == "PUT" && !strings.HasPrefix(call.key, f.normalizedPrefix) {
			t.Error("ingestion wrote outside its normalized prefix")
		}
	}
	if !read {
		t.Fatal("ingestion did not consume actual raw storage with its own credential")
	}
	var work struct {
		Items []app.WorkItem `json:"items"`
		Total int            `json:"total"`
	}
	core.json(t, "GET", "/api/v1/work", nil, "", 200, &work)
	if work.Total != 1 || len(work.Items) != 1 {
		t.Fatal("ingestion result is not visible through authenticated core API/real PostgreSQL")
	}
}

func clearStorageEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_S3_") || (strings.HasPrefix(name, "ASPM_RUNTIME_ROLE_") && (strings.HasSuffix(name, "_ACCESS_KEY") || strings.HasSuffix(name, "_SECRET_KEY"))) {
			t.Setenv(name, "")
		}
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func TestRuntimeRolesReportsRequireOnlyDatabase(t *testing.T) {
	f := newFixture(t)
	clearStorageEnvironment(t)
	t.Setenv("ASPM_DATABASE_URL", f.database.URL)
	t.Setenv("ASPM_SCHEMA", f.database.Schema)
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "3")
	t.Setenv("ASPM_TLS_CERT_FILE", "")
	t.Setenv("ASPM_TLS_KEY_FILE", "")
	t.Run("main-environment", func(t *testing.T) {
		_, err := service.Environment("reports")
		if err != nil {
			t.Fatalf("report-only main configuration still requires storage credentials (%T; details withheld)", err)
		}
	})
	t.Run("queued-snapshot-without-storage", func(t *testing.T) {
		if Production.Reports == nil {
			t.Fatal("runtime role binding missing: Reports (database-only worker)")
		}
		address := freeAddress(t)
		worker := Production.Reports(ReportConfig{Database: f.database, Listen: address})
		if worker == nil {
			t.Fatal("Reports factory returned nil")
		}
		if _, exposed := any(worker).(http.Handler); exposed {
			t.Fatal("report worker exposes an application/auth handler")
		}
		original := http.DefaultTransport
		guard := original.(*http.Transport).Clone()
		var networkCalls atomic.Int32
		guard.DialContext = func(context.Context, string, string) (net.Conn, error) {
			networkCalls.Add(1)
			return nil, errors.New("report-only worker attempted forbidden HTTP/storage I/O")
		}
		http.DefaultTransport = guard
		t.Cleanup(func() { http.DefaultTransport = original; guard.CloseIdleConnections() })
		running := startRole(t, f.ctx, worker.Run, address)
		if networkCalls.Load() != 0 {
			t.Fatal("report constructor attempted HTTP/storage I/O")
		}
		client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
		defer client.CloseIdleConnections()
		response, err := client.Get("http://" + address + "/api/v1/session")
		must(t, "inspect report-only role surface", err)
		must(t, "close report-only route response", response.Body.Close())
		if response.StatusCode != 404 {
			t.Fatal("report-only main role exposes an application/auth endpoint")
		}
		stopRole(t, running)
		http.DefaultTransport = original
		core := f.core(t)
		f.enroll(t, core)
		var queued struct {
			Snapshot app.ReportSnapshot `json:"snapshot"`
		}
		core.json(t, "POST", "/api/v1/reports/snapshots", map[string]any{"name": "Synthetic storage-free snapshot", "freshnessDays": 7}, "", 202, &queued)
		if queued.Snapshot.ID == "" || queued.Snapshot.State != "queued" {
			t.Fatal("real API did not enqueue an authenticated snapshot")
		}
		var pending struct {
			Snapshot app.ReportSnapshot `json:"snapshot"`
		}
		core.json(t, "GET", "/api/v1/reports/snapshots/"+queued.Snapshot.ID, nil, "", 200, &pending)
		if pending.Snapshot.State != "queued" {
			t.Fatal("core processed reporting work inline rather than leaving it queued")
		}
		before := len(f.taps["core"].snapshot()) + len(f.taps["ingestion"].snapshot())
		http.DefaultTransport = guard
		worker = Production.Reports(ReportConfig{Database: f.database, Listen: address})
		if worker == nil {
			t.Fatal("Reports factory returned nil on independent reopen")
		}
		running = startRole(t, f.ctx, worker.Run, address)
		deadline := time.Now().Add(12 * time.Second)
		var final struct {
			Snapshot app.ReportSnapshot `json:"snapshot"`
		}
		for {
			core.json(t, "GET", "/api/v1/reports/snapshots/"+queued.Snapshot.ID, nil, "", 200, &final)
			if final.Snapshot.State == "succeeded" || final.Snapshot.State == "failed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("database-only report worker did not process the real queued snapshot")
			}
			time.Sleep(50 * time.Millisecond)
		}
		stopRole(t, running)
		http.DefaultTransport = original
		if networkCalls.Load() != 0 || before != len(f.taps["core"].snapshot())+len(f.taps["ingestion"].snapshot()) {
			t.Error("report snapshot processing performed storage/HTTP I/O")
		}
		report := final.Snapshot.Report
		if final.Snapshot.State != "succeeded" || report == nil || report.WorkspaceID != core.workspace ||
			report.Totals.Assets != 1 || report.Totals.Findings != 0 || report.Coverage.UnscannedAssets != 1 {
			t.Fatal("report worker did not persist the actual authorized PostgreSQL snapshot")
		}
	})
}

func TestRuntimeRolesMissingCallerCredentialsBeforeIO(t *testing.T) {
	for _, role := range []string{"core", "ingestion"} {
		for _, missing := range []string{"access-key", "secret-key"} {
			t.Run(role+"/"+missing, func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				must(t, "create owned database I/O detector", err)
				var connections atomic.Int32
				var storageCalls atomic.Int32
				storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					storageCalls.Add(1)
					http.Error(w, "missing-credential I/O must never reach this detector", 500)
				}))
				t.Cleanup(storage.Close)
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						conn, e := listener.Accept()
						if e != nil {
							return
						}
						connections.Add(1)
						conn.Close()
					}
				}()
				t.Cleanup(func() { listener.Close(); <-done })
				db := DatabaseConfig{URL: "postgres://synthetic:synthetic@" + listener.Addr().String() + "/synthetic?sslmode=disable", Schema: "runtime_role_preflight", ApplicationName: "runtime-role-preflight", MaxConnections: 3}
				raw := ScopedStorage{Endpoint: storage.URL, Region: "us-east-1", Bucket: "aspm-isolation", Prefix: "raw/team-a/preflight/",
					AccessKey: "synthetic-incomplete-caller-key", SecretKey: "synthetic-incomplete-caller-secret"}
				if missing == "access-key" {
					raw.AccessKey = ""
				} else {
					raw.SecretKey = ""
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var run func(context.Context) error
				if role == "core" {
					if Production.Core == nil {
						t.Fatal("runtime role binding missing: Core")
					}
					entry := Production.Core(CoreConfig{Database: db, Raw: raw, Listen: "127.0.0.1:0", Assets: t.TempDir()})
					if entry == nil {
						t.Fatal("Core must report invalid credentials through Run, not a nil runtime")
					}
					run = entry.Run
				} else {
					if Production.Ingestion == nil {
						t.Fatal("runtime role binding missing: Ingestion")
					}
					entry := Production.Ingestion(IngestionConfig{Database: db, Raw: raw, NormalizedPrefix: "normalized/team-a/preflight/", Listen: "127.0.0.1:0"})
					if entry == nil {
						t.Fatal("Ingestion must report invalid credentials through Run, not a nil runtime")
					}
					run = entry.Run
				}
				err = run(ctx)
				if err == nil {
					t.Error("missing required caller storage credentials were accepted")
				}
				if connections.Load() != 0 {
					t.Errorf("missing %s performed %d database connection(s) before validation", missing, connections.Load())
				}
				if storageCalls.Load() != 0 {
					t.Error("missing credentials performed storage I/O before validation")
				}
			})
		}
	}
}
