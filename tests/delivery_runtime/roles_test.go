//go:build integration

package delivery_runtime

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type liveRole struct {
	address string
	client  *http.Client
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

func configuredRole(f *fixture, native *nativeFixture) roleConfig {
	return roleConfig{Database: databaseConfig{
		URL: f.config.DatabaseURL, Schema: f.config.Schema, ApplicationName: "owned-delivery-" + id(f.t), MaxConnections: 1,
	}, Listen: freeAddress(f.t), WorkerID: "owned-delivery-" + id(f.t), EncryptionKey: f.config.IntegrationEncryptionKey,
		LeaseDuration: time.Second, SlackEndpoint: native.server.URL, Client: native.client}
}

func healthJSON(t *testing.T, client *http.Client, address, path string, status int) map[string]string {
	t.Helper()
	response, err := client.Get("http://" + address + path)
	must(t, "call real role health surface", err)
	defer response.Body.Close()
	require(t, response.StatusCode == status, "role health/API status differs from its actual responsibility")
	value := map[string]string{}
	if status == 200 {
		must(t, "decode real health metadata", json.NewDecoder(io.LimitReader(response.Body, 16384)).Decode(&value))
	}
	return value
}

func awaitReady(t *testing.T, client *http.Client, address string, done <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			status := response.StatusCode
			response.Body.Close()
			if status == 200 {
				return
			}
		}
		select {
		case <-done:
			t.Fatal("actual delivery entrypoint exited before readiness; private output withheld")
		case <-deadline.C:
			t.Fatal("actual delivery role never became ready within its bound")
		case <-ticker.C:
		}
	}
}

func startService(t *testing.T, ctx context.Context, config roleConfig) *liveRole {
	t.Helper()
	require(t, Production.Run != nil, "real service.Run delivery binding missing")
	runCtx, cancel := context.WithCancel(ctx)
	role := &liveRole{address: config.Listen, client: exactClient(t, config.Listen), cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() { stopService(t, role) })
	go func() { role.err = Production.Run(runCtx, config); close(role.done) }()
	awaitReady(t, role.client, role.address, role.done)
	return role
}

func stopService(t *testing.T, role *liveRole) {
	t.Helper()
	role.cancel()
	select {
	case <-role.done:
		require(t, role.err == nil || errors.Is(role.err, context.Canceled), "delivery service shutdown failed")
	case <-time.After(6 * time.Second):
		t.Error("delivery service/listener/worker did not shut down within its bound")
	}
}

func checkHealth(t *testing.T, role *liveRole) {
	t.Helper()
	health := healthJSON(t, role.client, role.address, "/healthz", 200)
	require(t, health["apiVersion"] == apiVersion && health["service"] == "delivery" && health["status"] == "alive",
		"healthz misidentified the real delivery role")
	ready := healthJSON(t, role.client, role.address, "/readyz", 200)
	require(t, ready["service"] == "delivery" && ready["status"] == "ready" &&
		ready["database"] == "reachable" && ready["storage"] == "not-required",
		"delivery readiness falsely depends on storage or omits its actual DB readiness")
	healthJSON(t, role.client, role.address, "/api/v1/session", 404)
	healthJSON(t, role.client, role.address, "/", 404)
}

func waitDelivery(f *fixture, deliveryID, state string) reply {
	f.t.Helper()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for reads := 0; reads < 100; reads++ {
		value := f.json(f.writer, "GET", "/api/v1/integrations/deliveries/"+deliveryID, nil, 200)
		if value.Delivery.State == state {
			return value
		}
		select {
		case <-deadline.C:
			f.t.Fatal("actual API delivery state did not reach the expected durable result")
		case <-ticker.C:
		}
	}
	f.t.Fatal("bounded durable-state observation limit exceeded")
	return reply{}
}

func TestDeliveryRuntimeServiceDrainsActualCoreIntentAndServesOnlyHealth(t *testing.T) {
	f := newFixture(t)
	token := secret(t)
	native := newNative(t, token, false)
	_, _, deliveryID := f.queued(token, "runtime-service")
	require(t, native.count.Load() == 0, "core sent native work before the independent role started")
	role := startService(t, f.ctx, configuredRole(f, native))
	checkHealth(t, role)
	result := waitDelivery(f, deliveryID, "confirmed")
	require(t, result.Delivery.Receipt != nil && result.Delivery.Receipt.RemoteID == "C123:1789560000.123456" &&
		result.Delivery.RequestedBy == f.writer.id, "real service role did not persist the native receipt and actual actor")
	require(t, native.count.Load() == 1, "real service role duplicated one explicit intent")
	stopService(t, role)
	require(t, native.count.Load() == 1, "shutdown caused another native write")
}

func TestDeliveryRuntimeInvalidInputsFailBeforeDBProviderOrListener(t *testing.T) {
	require(t, Production.Run != nil, "real service.Run delivery binding missing")
	db, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned DB access detector", err)
	var databaseCalls atomic.Int32
	dbDone := make(chan struct{})
	go func() {
		defer close(dbDone)
		for {
			connection, acceptErr := db.Accept()
			if acceptErr != nil {
				return
			}
			databaseCalls.Add(1)
			connection.Close()
		}
	}()
	t.Cleanup(func() { db.Close(); <-dbDone })
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve conflicting owned health address as preflight ordering witness", err)
	t.Cleanup(func() { reserved.Close() })
	token := secret(t)
	native := newNative(t, token, false)
	base := roleConfig{
		Database: databaseConfig{
			URL:    "postgres://synthetic:" + id(t) + "@" + db.Addr().String() + "/fixture?sslmode=disable",
			Schema: "delivery_preflight", ApplicationName: "delivery-preflight", MaxConnections: 1,
		},
		Listen: reserved.Addr().String(), WorkerID: "owned-preflight", EncryptionKey: random(t, 32),
		LeaseDuration: 15 * time.Second, SlackEndpoint: native.server.URL, Client: native.client,
	}
	for _, row := range []struct {
		name, category string
		change         func(*roleConfig)
	}{
		{"database", "database", func(c *roleConfig) { c.Database.URL = "" }},
		{"db-pool", "database", func(c *roleConfig) { c.Database.MaxConnections = 0 }},
		{"missing-key", "key", func(c *roleConfig) { c.EncryptionKey = nil }},
		{"short-key", "key", func(c *roleConfig) { c.EncryptionKey = make([]byte, 31) }},
		{"missing-client", "client", func(c *roleConfig) { c.Client = nil }},
		{"nil-transport", "transport", func(c *roleConfig) { c.Client = &http.Client{Timeout: time.Second} }},
		{"insecure-client", "tls", func(c *roleConfig) {
			c.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: time.Second}
		}},
		{"proxy-client", "proxy", func(c *roleConfig) {
			c.Client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}, Timeout: time.Second}
		}},
		{"http-gateway", "gateway", func(c *roleConfig) { c.SlackEndpoint = strings.Replace(native.server.URL, "https:", "http:", 1) }},
		{"fragment-gateway", "gateway", func(c *roleConfig) { c.SlackEndpoint += "#not-a-destination" }},
		{"zero-lease", "lease", func(c *roleConfig) { c.LeaseDuration = 0 }},
		{"excessive-lease", "lease", func(c *roleConfig) { c.LeaseDuration = 2 * time.Minute }},
	} {
		t.Run(row.name, func(t *testing.T) {
			config := base
			row.change(&config)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := Production.Run(ctx, config)
			require(t, err != nil && !errors.Is(err, context.DeadlineExceeded), "invalid delivery config attempted startup instead of preflight rejection")
			message := strings.ToLower(err.Error())
			require(t, strings.Contains(message, row.category), "preflight must identify the invalid category before any conflicting listener bind")
			require(t, !strings.Contains(message, strings.ToLower(base.Database.URL)) &&
				!strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(base.EncryptionKey)) &&
				!strings.Contains(message, hex.EncodeToString(base.EncryptionKey)),
				"preflight error exposed supplied private configuration")
			require(t, databaseCalls.Load() == 0 && native.count.Load() == 0, "invalid role configuration performed DB/provider I/O")
		})
	}
}

func TestDeliveryRuntimeCancellationThenFreshCommandPreservesUncertainty(t *testing.T) {
	f := newFixture(t)
	token := secret(t)
	native := newNative(t, token, true)
	findingID, connectionID, deliveryID := f.queued(token, "held-runtime")
	role := startService(t, f.ctx, configuredRole(f, native))
	select {
	case <-native.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("real delivery role did not reach the owned held Slack POST")
	}
	checkHealth(t, role)
	stopService(t, role)
	select {
	case <-native.cancelled:
	case <-time.After(time.Second):
		t.Fatal("service cancellation was not propagated to the owned TLS request")
	}
	waitDelivery(f, deliveryID, "uncertain")
	require(t, native.count.Load() == 1, "canceled native work was automatically resent")
	native.hold.Store(false)
	command := startCommand(t, f, native)
	checkHealth(t, command.role)
	for index := 0; index < 3; index++ {
		value := f.json(f.writer, "GET", "/api/v1/integrations/deliveries/"+deliveryID, nil, 200)
		require(t, value.Delivery.State == "uncertain" && value.Delivery.Receipt == nil, "fresh command lost durable uncertainty")
		healthJSON(t, command.role.client, command.role.address, "/readyz", 200)
	}
	require(t, native.count.Load() == 1, "new command process requeued terminal uncertainty")
	fresh := f.json(f.writer, "POST", "/api/v1/findings/"+findingID+"/deliveries", map[string]string{
		"connectionId": connectionID, "idempotencyKey": "new-explicit-runtime-intent",
	}, 202).Delivery
	waitDelivery(f, fresh.ID, "confirmed")
	require(t, native.count.Load() == 2, "actual command must send exactly the separately authorized new intent, not the old one")
}

type commandRole struct{ role *liveRole }

func startCommand(t *testing.T, f *fixture, native *nativeFixture) *commandRole {
	t.Helper()
	binary := env(t, "ASPM_DELIVERY_TEST_BINARY")
	info, err := os.Stat(binary)
	must(t, "locate the actual separately built delivery-worker command", err)
	require(t, !info.IsDir(), "delivery command is not a file")
	owned := filepath.Join(env(t, "ASPM_DELIVERY_TEST_ARTIFACT_DIR"), "command-fixture-"+id(t))
	must(t, "create owned command CA directory", os.MkdirAll(owned, 0700))
	caFile := filepath.Join(owned, "gateway-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: native.server.Certificate().Raw})
	must(t, "write only the synthetic public TLS CA", os.WriteFile(caFile, certificate, 0600))
	t.Cleanup(func() { _ = os.Remove(caFile); _ = os.Remove(owned) })
	address := freeAddress(t)
	command := exec.Command(binary)
	command.Stdout, command.Stderr = &f.log, &f.log
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "SLACK_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env,
		"ASPM_DATABASE_URL="+f.config.DatabaseURL, "ASPM_SCHEMA="+f.config.Schema, "ASPM_DB_MAX_CONNECTIONS=1",
		"ASPM_LISTEN="+address, "ASPM_INTEGRATION_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(f.config.IntegrationEncryptionKey),
		"ASPM_DELIVERY_LEASE_DURATION=1s", "ASPM_SLACK_ENDPOINT="+native.server.URL, "ASPM_DELIVERY_CA_FILE="+caFile,
		"AWS_EC2_METADATA_DISABLED=true")
	must(t, "start the actual owned delivery-worker binary", command.Start())
	role := &liveRole{address: address, client: exactClient(t, address), done: make(chan struct{})}
	go func() { role.err = command.Wait(); close(role.done) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-role.done:
		case <-time.After(5 * time.Second):
			t.Error("owned delivery command process did not stop")
		}
	})
	awaitReady(t, role.client, address, role.done)
	return &commandRole{role: role}
}

func TestDeliveryRuntimeEnvironmentDefaultsIsolationAndLegacyRoles(t *testing.T) {
	require(t, Production.Environment != nil, "real service.Environment binding missing")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_") && !strings.HasPrefix(name, "ASPM_DELIVERY_TEST_") {
			t.Setenv(name, "")
		}
	}
	key := random(t, 32)
	t.Setenv("ASPM_DATABASE_URL", "postgres://synthetic:"+id(t)+"@127.0.0.1:15432/fixture?sslmode=disable")
	t.Setenv("ASPM_SCHEMA", "delivery_environment")
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "1")
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	t.Setenv("ASPM_S3_ENDPOINT", "not-a-delivery-dependency")
	t.Setenv("ASPM_S3_PREPARE_READINESS", "not-a-delivery-setting")
	t.Setenv("ASPM_BOOTSTRAP_TOKEN", secret(t))
	t.Setenv("ASPM_S3_ACCESS_KEY", secret(t))
	t.Setenv("ASPM_S3_SECRET_KEY", secret(t))
	t.Setenv("HTTPS_PROXY", "http://synthetic:never-use@127.0.0.1:1")
	config, err := Production.Environment("delivery")
	must(t, "parse valid independent delivery environment", err)
	require(t, config.LeaseDuration == 15*time.Second && config.SlackEndpoint == "https://slack.com" &&
		bytes.Equal(config.EncryptionKey, key) && config.Database.MaxConnections == 1, "delivery defaults/key/pool configuration differ from the frozen contract")
	require(t, config.Evidence.Endpoint == "" && config.Evidence.AccessKey == "" && config.Evidence.SecretKey == "" &&
		config.Evidence.Bucket == "" && config.Evidence.Prefix == "" && config.Evidence.Region == "" &&
		config.Evidence.Timeout == 0 && config.BootstrapToken == "", "delivery inherited unrelated S3/bootstrap capabilities")
	checkEnvironmentClient(t, config)
	for _, item := range []struct{ name, value string }{
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", ""},
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", "not-base64"},
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(random(t, 31))},
		{"ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(random(t, 33))},
		{"ASPM_DELIVERY_LEASE_DURATION", "0s"},
		{"ASPM_DELIVERY_LEASE_DURATION", "61s"},
		{"ASPM_SLACK_ENDPOINT", "http://127.0.0.1:1"},
		{"ASPM_SLACK_ENDPOINT", "https://127.0.0.1:1/#fragment"},
		{"ASPM_DELIVERY_CA_FILE", filepath.Join(env(t, "ASPM_DELIVERY_TEST_ARTIFACT_DIR"), "absent-ca.pem")},
	} {
		t.Run(item.name+"-invalid", func(t *testing.T) {
			t.Setenv(item.name, item.value)
			_, err := Production.Environment("delivery")
			require(t, err != nil, "invalid explicit delivery environment silently used a default")
		})
	}
	t.Setenv("ASPM_S3_PREPARE_READINESS", "")
	t.Setenv("ASPM_S3_ENDPOINT", "http://127.0.0.1:18333")
	t.Setenv("ASPM_S3_BUCKET", "synthetic-existing-bucket")
	t.Setenv("ASPM_S3_PREFIX", "raw/owned/")
	t.Setenv("ASPM_S3_NORMALIZED_PREFIX", "normalized/owned/")
	t.Setenv("ASPM_S3_REGION", "us-east-1")
	t.Setenv("ASPM_DB_MAX_CONNECTIONS", "3")
	for _, role := range []string{"core", "reports", "ingestion"} {
		_, err := Production.Environment(role)
		must(t, "preserve existing "+role+" environment behavior", err)
	}
}

func checkEnvironmentClient(t *testing.T, config roleConfig) {
	t.Helper()
	require(t, config.Client != nil && config.Client.Timeout > 0 && config.Client.Timeout <= 30*time.Second &&
		config.Client.CheckRedirect != nil && config.Client.Jar == nil, "environment must construct an explicit bounded no-redirect client")
	transport, ok := config.Client.Transport.(*http.Transport)
	require(t, ok && transport.Proxy == nil && transport.TLSClientConfig != nil &&
		!transport.TLSClientConfig.InsecureSkipVerify && transport.TLSClientConfig.MinVersion >= tls.VersionTLS12 &&
		transport.DialContext != nil, "environment client must retain normal TLS and forbid ambient proxy transport")
	t.Cleanup(transport.CloseIdleConnections)
	request, err := http.NewRequest("GET", "https://not-the-selected-gateway.synthetic.invalid", nil)
	must(t, "build non-network redirect-policy probe", err)
	require(t, config.Client.CheckRedirect(request, nil) != nil, "environment client permits automatic redirects")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open unrelated owned egress detector", err)
	var accepted atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted.Add(1)
			connection.Close()
		}
	}()
	connection, err := transport.DialContext(ctx, "tcp", listener.Addr().String())
	if connection != nil {
		connection.Close()
	}
	listener.Close()
	<-done
	require(t, connection == nil && accepted.Load() == 0 && err != nil && !errors.Is(err, context.DeadlineExceeded),
		"selected-origin transport did not reject an unrelated owned dial before I/O")
}
