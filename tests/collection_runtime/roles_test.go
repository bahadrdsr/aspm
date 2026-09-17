//go:build integration

package collection_runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

var collectionEnvironmentNames = []string{
	"ASPM_COLLECTION_S3_ENDPOINT", "ASPM_COLLECTION_S3_BUCKET", "ASPM_COLLECTION_S3_PREFIX",
	"ASPM_COLLECTION_S3_REGION", "ASPM_COLLECTION_S3_ACCESS_KEY", "ASPM_COLLECTION_S3_SECRET_KEY",
}

func unset(t *testing.T, name string) {
	t.Helper()
	old, present := os.LookupEnv(name)
	must(t, "unset process-local test selection", os.Unsetenv(name))
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
func setBaseEnvironment(t *testing.T, db app.DatabaseConfig) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_") && !strings.HasPrefix(name, "ASPM_COLLECTION_RUNTIME_") {
			unset(t, name)
		}
	}
	t.Setenv("ASPM_DATABASE_URL", db.DatabaseURL)
	t.Setenv("ASPM_SCHEMA", db.Schema)
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "3")
}
func setRawEnvironment(t *testing.T, raw evidence.Config) {
	t.Helper()
	for name, value := range map[string]string{"ASPM_S3_ENDPOINT": raw.Endpoint, "ASPM_S3_BUCKET": raw.Bucket,
		"ASPM_S3_PREFIX": raw.Prefix, "ASPM_S3_REGION": raw.Region, "ASPM_S3_ACCESS_KEY": raw.AccessKey, "ASPM_S3_SECRET_KEY": raw.SecretKey} {
		t.Setenv(name, value)
	}
}
func setCollectionEnvironment(t *testing.T, storage *app.StorageConfig) {
	t.Helper()
	if storage == nil {
		for _, name := range collectionEnvironmentNames {
			unset(t, name)
		}
		return
	}
	for name, value := range map[string]string{"ASPM_COLLECTION_S3_ENDPOINT": storage.Endpoint, "ASPM_COLLECTION_S3_BUCKET": storage.Bucket,
		"ASPM_COLLECTION_S3_PREFIX": storage.Prefix, "ASPM_COLLECTION_S3_REGION": storage.Region,
		"ASPM_COLLECTION_S3_ACCESS_KEY": storage.AccessKey, "ASPM_COLLECTION_S3_SECRET_KEY": storage.SecretKey} {
		t.Setenv(name, value)
	}
}

func TestCollectionRuntimeCoreEnvironmentRoleEvidenceAndRealReadOnlyRights(t *testing.T) {
	f := newFixture(t)
	f.rights()
	core := f.coreService()
	token := secret(t)
	native := github(t, token, false)
	source := core.source(token)
	job := core.enqueue(source, "actual-runtime-source")
	check(t, native.count.Load() == 0 && f.publishTap.writes.Load() == 0, "core collected or published instead of queuing")
	running := startRole(t, f.ctx, "collection", f.collectionConfig(native))
	checkCollectionHealth(t, running)
	complete := core.await(job, "succeeded")
	check(t, complete.Collection.Complete && complete.Collection.RecordCount == 2 && complete.Collection.AssetID != nil, "actual collection role did not persist complete selected records and asset")
	records := core.json(core.writer, "GET", "/api/v1/sources/collections/"+job+"/records", nil, 200)
	check(t, len(records.Items) == 2, "core source record metadata unavailable after real collection service")
	beforeReads := f.readTap.calls.Load()
	for _, record := range records.Items {
		want := native.repo
		if record.Kind == "finding" {
			want = native.alert
		}
		data, _ := core.request(core.writer, "GET", "/api/v1/sources/collections/"+job+"/records/"+record.ID+"/evidence", nil, 200)
		check(t, bytes.Equal(data, want) && record.Evidence.SHA256 == digest(want), "actual core service RO evidence wiring lost exact bytes/digest")
	}
	check(t, f.readTap.calls.Load() > beforeReads && f.readTap.writes.Load() == 0 && f.publishTap.writes.Load() >= 2, "actual role identities did not stay on their separate read/publish paths")
	second := core.enqueue(source, "stable-next-runtime-collection")
	secondResult := core.await(second, "succeeded")
	check(t, secondResult.Collection.AssetID != nil && *secondResult.Collection.AssetID == *complete.Collection.AssetID && core.json(core.admin, "GET", "/api/v1/assets", nil, 200).Total == 1, "service loop duplicated stable repository assets")
	check(t, core.json(core.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "collection runtime normalized alert polling into findings")
	stopRole(t, running)
}

func TestCollectionRuntimeInvalidAndPartialCapabilitiesFailBeforeIO(t *testing.T) {
	check(t, Production.Run != nil && Production.Environment != nil, "actual runtime binding missing")
	database, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned DB preflight detector", err)
	var dbCalls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := database.Accept()
			if e != nil {
				return
			}
			dbCalls.Add(1)
			conn.Close()
		}
	}()
	t.Cleanup(func() { database.Close(); <-done })
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned listen-conflict preflight witness", err)
	t.Cleanup(func() { reserved.Close() })
	var storageCalls atomic.Int32
	storageServer := httptestStorageCounter(t, &storageCalls)
	native := github(t, secret(t), false)
	selected := &app.StorageConfig{Endpoint: storageServer, Bucket: "owned-only", Prefix: "selected/", Region: "us-east-1", AccessKey: "synthetic-publisher", SecretKey: secret(t), Timeout: time.Second}
	config := runtimeConfig{Database: app.DatabaseConfig{DatabaseURL: "postgres://synthetic:" + nonce(t) + "@" + database.Addr().String() + "/fixture?sslmode=disable", Schema: "runtime_preflight", ApplicationName: "collection-preflight", MaxConnections: 1},
		Listen: reserved.Addr().String(), WorkerID: "owned-preflight", Key: random(t, 32), CollectionStorage: selected, Lease: 15 * time.Second, Endpoint: native.server.URL, Client: native.client,
		Limits: connectors.Limits{Requests: 4, Pages: 2, PageSize: 1, Bytes: 32768}}
	for _, row := range []struct {
		name, role, category string
		change               func(*runtimeConfig)
	}{
		{"database", "collection", "database", func(c *runtimeConfig) { c.Database.DatabaseURL = "" }},
		{"key", "collection", "key", func(c *runtimeConfig) { c.Key = nil }},
		{"missing-storage", "collection", "storage", func(c *runtimeConfig) { c.CollectionStorage = nil }},
		{"partial-storage-key", "collection", "storage", func(c *runtimeConfig) { s := *c.CollectionStorage; s.SecretKey = ""; c.CollectionStorage = &s }},
		{"partial-core-storage", "core", "storage", func(c *runtimeConfig) {
			s := *c.CollectionStorage
			s.Region = ""
			c.CollectionStorage = &s
			c.Database.MaxConnections = 3
			c.Assets = required(t, "ASPM_COLLECTION_RUNTIME_ARTIFACT_DIR")
			c.Raw = evidence.Config{Endpoint: storageServer, Bucket: "raw", Prefix: "raw/", Region: "us-east-1", AccessKey: "synthetic-raw", SecretKey: secret(t), Timeout: time.Second}
		}},
		{"client", "collection", "client", func(c *runtimeConfig) { c.Client = nil }},
		{"gateway", "collection", "gateway", func(c *runtimeConfig) { c.Endpoint = strings.Replace(c.Endpoint, "https:", "http:", 1) }},
		{"lease", "collection", "lease", func(c *runtimeConfig) { c.Lease = 0 }},
		{"limits", "collection", "limit", func(c *runtimeConfig) { c.Limits.PageSize = 101 }},
		{"TLS", "collection", "tls", func(c *runtimeConfig) {
			c.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: time.Second}
		}},
		{"TLS-range", "collection", "tls", func(c *runtimeConfig) {
			c.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS12}}, Timeout: time.Second}
		}},
		{"proxy", "collection", "proxy", func(c *runtimeConfig) {
			c.Client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}, Timeout: time.Second}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			bad := config
			row.change(&bad)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Production.Run(ctx, row.role, bad)
			check(t, err != nil && !errors.Is(err, context.DeadlineExceeded) && strings.Contains(strings.ToLower(err.Error()), row.category), "invalid configuration did not fail safely in its preflight category")
			check(t, !strings.Contains(err.Error(), config.Database.DatabaseURL) && !strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(config.Key)), "private preflight input leaked")
			check(t, dbCalls.Load() == 0 && storageCalls.Load() == 0 && native.count.Load() == 0, "invalid input reached database/storage/provider before preflight")
		})
	}
}

func TestCollectionRuntimeCancellationFreshCommandAndNoResurrection(t *testing.T) {
	for _, kind := range []string{"TLS", "S3"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.rights()
			core := f.coreService()
			token := secret(t)
			native := github(t, token, kind == "TLS")
			source := core.source(token)
			job := core.enqueue(source, "held-"+kind)
			var gate *putGate
			if kind == "S3" {
				gate = f.publishTap.holdPut()
				t.Cleanup(gate.allow)
			}
			role := startRole(t, f.ctx, "collection", f.collectionConfig(native))
			if gate == nil {
				wait(t, native.arrived, "actual held TLS read")
			} else {
				wait(t, gate.arrived, "actual held S3 publication")
			}
			checkCollectionHealth(t, role)
			stopRole(t, role)
			if gate == nil {
				wait(t, native.cancelled, "native context cancellation")
			} else {
				wait(t, gate.done, "storage context cancellation")
			}
			failed := core.await(job, "failed")
			check(t, !failed.Collection.Complete && failed.Collection.AssetID == nil && failed.Collection.RecordCount == 0, "canceled service committed source asset/snapshot")
			blocked := core.enqueue(source, "blocked-before-restart-"+kind)
			core.json(core.admin, "PATCH", "/api/v1/sources/"+source, map[string]bool{"enabled": false}, 200)
			beforeNative := native.count.Load()
			native.hold.Store(false)
			command := startCommand(t, f, native)
			checkCollectionHealth(t, command.role)
			core.await(blocked, "blocked")
			for n := 0; n < 3; n++ {
				check(t, core.collection(job).Collection.State == "failed" && core.collection(blocked).Collection.State == "blocked", "fresh command resurrected prior failed/blocked collection")
				health(t, command.role, "/readyz", 200)
			}
			check(t, native.count.Load() == beforeNative, "fresh command requeued terminal source reads")
			core.json(core.admin, "PATCH", "/api/v1/sources/"+source, map[string]bool{"enabled": true}, 200)
			next := core.enqueue(source, "new-manual-after-"+kind)
			done := core.await(next, "succeeded")
			check(t, done.Collection.Complete && done.Collection.RecordCount == 2 && done.Collection.AssetID != nil, "actual command did not process separately authorized new work")
			check(t, native.count.Load() == beforeNative+2, "actual command duplicated the selected repository/alert reads")
		})
	}
}

func TestCollectionRuntimeEnvironmentDefaultsAndLegacyRoleIsolation(t *testing.T) {
	f := newFixture(t)
	setBaseEnvironment(t, f.database)
	setRawEnvironment(t, f.raw)
	setCollectionEnvironment(t, nil)
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(f.key))
	core, err := Production.Environment("core")
	must(t, "old core environment without optional collection storage", err)
	check(t, core.CollectionStorage == nil && core.Raw.AccessKey == f.raw.AccessKey, "absent collection storage silently borrowed raw credentials")
	for _, field := range collectionEnvironmentNames {
		t.Run("partial-"+field, func(t *testing.T) {
			t.Setenv(field, "synthetic-partial-value")
			_, err := Production.Environment("core")
			check(t, err != nil, "partial optional core collection storage was accepted")
		})
	}
	setCollectionEnvironment(t, &f.publish)
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
	t.Setenv("ASPM_S3_PREPARE_READINESS", "not-a-collection-setting")
	t.Setenv("ASPM_BOOTSTRAP_TOKEN", secret(t))
	t.Setenv("ASPM_ASSETS", "not-a-collection-capability")
	config, err := Production.Environment("collection")
	must(t, "parse independent collection environment", err)
	check(t, config.CollectionStorage != nil && config.CollectionStorage.AccessKey == f.publish.AccessKey && config.CollectionStorage.Prefix == f.publish.Prefix && bytes.Equal(config.Key, f.key), "collection environment lost its explicitly selected publisher/key")
	check(t, config.Database.MaxConnections == 1 && config.Lease == 15*time.Second && config.Endpoint == "https://api.github.com", "collection default pool/lease/gateway differs")
	check(t, config.Raw == (evidence.Config{}) && config.Bootstrap == "" && config.Assets == "" && config.ReadinessKey == "" && !config.PrepareReadiness, "collection inherited raw storage/bootstrap/assets/readiness authority")
	check(t, config.Limits == (connectors.Limits{Requests: 32, Pages: 8, PageSize: 50, Bytes: 8 << 20}), "collection default limits are not the accepted bounded native profile")
	environmentClient(t, config)
	for _, field := range collectionEnvironmentNames {
		t.Run("required-"+field, func(t *testing.T) {
			unset(t, field)
			_, err := Production.Environment("collection")
			check(t, err != nil, "collection role borrowed missing storage input from raw/ambient settings")
		})
	}
	for _, item := range []struct{ name, value string }{
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", ""}, {"ASPM_INTEGRATION_ENCRYPTION_KEY", "not-base64"}, {"ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(random(t, 31))},
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(random(t, 33))},
		{"ASPM_COLLECTION_LEASE_DURATION", "0s"}, {"ASPM_COLLECTION_LEASE_DURATION", "61s"},
		{"ASPM_GITHUB_ENDPOINT", "http://127.0.0.1:1"}, {"ASPM_GITHUB_ENDPOINT", "https://127.0.0.1:1/#not-a-request-target"},
		{"ASPM_COLLECTION_CA_FILE", filepath.Join(required(t, "ASPM_COLLECTION_RUNTIME_ARTIFACT_DIR"), "missing-ca.pem")},
	} {
		t.Run(item.name+"-invalid", func(t *testing.T) {
			t.Setenv(item.name, item.value)
			_, err := Production.Environment("collection")
			check(t, err != nil, "invalid explicit collection setting silently fell back")
		})
	}
	t.Setenv("ASPM_S3_PREPARE_READINESS", "")
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "3")
	t.Setenv("ASPM_S3_NORMALIZED_PREFIX", "normalized/runtime/")
	setCollectionEnvironment(t, nil)
	for _, role := range []string{"core", "reports", "ingestion", "delivery"} {
		if role == "delivery" {
			t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
		}
		_, err := Production.Environment(role)
		must(t, "preserve existing "+role+" role environment", err)
	}
}

func environmentClient(t *testing.T, config runtimeConfig) {
	t.Helper()
	check(t, config.Client != nil && config.Client.Timeout > 0 && config.Client.Timeout <= 30*time.Second && config.Client.Jar == nil && config.Client.CheckRedirect != nil, "collection environment client is not explicit/bounded/no-redirect")
	transport, ok := config.Client.Transport.(*http.Transport)
	check(t, ok && transport.Proxy == nil && transport.DialContext != nil && transport.TLSClientConfig != nil && !transport.TLSClientConfig.InsecureSkipVerify && transport.TLSClientConfig.MinVersion >= tls.VersionTLS12, "collection environment weakened normal TLS/proxy policy")
	t.Cleanup(transport.CloseIdleConnections)
}

type commandRole struct{ role *runningRole }

func startCommand(t *testing.T, f *fixture, g *githubFixture) *commandRole {
	t.Helper()
	binary := required(t, "ASPM_COLLECTION_RUNTIME_BINARY")
	info, err := os.Stat(binary)
	must(t, "locate actual built collection-worker command", err)
	check(t, !info.IsDir(), "command is not a file")
	directory := filepath.Join(required(t, "ASPM_COLLECTION_RUNTIME_ARTIFACT_DIR"), "owned-command-"+nonce(t))
	must(t, "create owned synthetic public-CA directory", os.MkdirAll(directory, 0700))
	ca := filepath.Join(directory, "gateway-ca.pem")
	must(t, "write only owned public TLS certificate", os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.server.Certificate().Raw}), 0600))
	t.Cleanup(func() { _ = os.Remove(ca); _ = os.Remove(directory) })
	address := ownedAddress(t)
	command := exec.Command(binary)
	command.Stdout, command.Stderr = &f.log, &f.log
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "GH_") || strings.HasPrefix(upper, "GITHUB_") || strings.HasPrefix(upper, "SLACK_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "ASPM_DATABASE_URL="+f.database.DatabaseURL, "ASPM_SCHEMA="+f.database.Schema, "ASPM_DB_MAX_CONNECTIONS=1", "ASPM_LISTEN="+address,
		"ASPM_INTEGRATION_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(f.key), "ASPM_COLLECTION_LEASE_DURATION=1s",
		"ASPM_GITHUB_ENDPOINT="+g.server.URL, "ASPM_COLLECTION_CA_FILE="+ca,
		"ASPM_COLLECTION_S3_ENDPOINT="+f.publishTap.server.URL, "ASPM_COLLECTION_S3_BUCKET="+f.publish.Bucket, "ASPM_COLLECTION_S3_PREFIX="+f.publish.Prefix,
		"ASPM_COLLECTION_S3_REGION="+f.publish.Region, "ASPM_COLLECTION_S3_ACCESS_KEY="+f.publish.AccessKey, "ASPM_COLLECTION_S3_SECRET_KEY="+f.publish.SecretKey, "AWS_EC2_METADATA_DISABLED=true")
	must(t, "start actual owned collection-worker process", command.Start())
	role := &runningRole{address: address, client: ownedClient(t, address), done: make(chan struct{})}
	go func() { role.err = command.Wait(); close(role.done) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-role.done:
		case <-time.After(5 * time.Second):
			t.Error("owned command process did not stop")
		}
	})
	awaitReady(t, role)
	return &commandRole{role: role}
}
