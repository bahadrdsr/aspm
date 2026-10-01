//go:build integration && jira_runtime

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/service"
)

func runtimeArtifact(t *testing.T, label string, data []byte) string {
	t.Helper()
	root := os.Getenv("ASPM_JIRA_COMMAND_ARTIFACT_DIR")
	cwd, err := os.Getwd()
	must(t, "locate runtime test package", err)
	base := filepath.Join(cwd, "..", "..", ".artifacts", "jira-runtime")
	relative, err := filepath.Rel(base, root)
	must(t, "check private runtime artifact boundary", err)
	check(t, filepath.IsAbs(root) && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative),
		"BLOCKED: new runtime artifacts must be below the dedicated project artifact root")
	check(t, !strings.ContainsAny(label, "\\/:") && len(data) <= 1<<20, "invalid owned artifact name or size")
	extension := filepath.Ext(label)
	path := filepath.Join(root, strings.TrimSuffix(label, extension)+"-"+nonce(t)+extension)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	must(t, "create unique owned artifact", err)
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	must(t, "write bounded owned artifact", errors.Join(writeErr, closeErr))
	return path
}

func runtimeEvidence(t *testing.T, label string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	must(t, "encode observed runtime evidence", err)
	runtimeArtifact(t, label+".json", append(data, '\n'))
}

func runtimeCA(t *testing.T, roots ...[]byte) string {
	t.Helper()
	var data []byte
	for _, root := range roots {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root})...)
	}
	path := runtimeArtifact(t, "public-gateway-ca.pem", data)
	t.Cleanup(func() { must(t, "remove owned public CA file", os.Remove(path)) })
	return path
}

func runtimeFreeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned health address", err)
	address := listener.Addr().String()
	must(t, "release owned health reservation", listener.Close())
	return address
}

func runtimeEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	kept := map[string]bool{
		"ASPM_JIRA_RUNTIME": true, "ASPM_JIRA_COMMAND_BINARY": true,
		"ASPM_JIRA_COMMAND_SHA256": true, "ASPM_JIRA_COMMAND_ARTIFACT_DIR": true,
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if kept[upper] {
			continue
		}
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			strings.HasPrefix(upper, "PG") || strings.HasPrefix(upper, "SLACK_") ||
			strings.HasPrefix(upper, "JIRA_") || strings.HasPrefix(upper, "GITHUB_") ||
			strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			t.Setenv(name, "")
		}
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func runtimeSettings(t *testing.T, database, schema, address string, key []byte, slack, ca string, origins []string) map[string]string {
	t.Helper()
	if origins == nil {
		origins = []string{}
	}
	return map[string]string{
		"ASPM_DATABASE_URL": database, "ASPM_SCHEMA": schema, "ASPM_LISTEN": address,
		"ASPM_DB_MAX_CONNECTIONS": "1", "ASPM_INTEGRATION_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(key),
		"ASPM_DELIVERY_LEASE_DURATION": "4s", "ASPM_SLACK_ENDPOINT": slack,
		"ASPM_DELIVERY_CA_FILE": ca, "ASPM_JIRA_API_ORIGINS": string(encoded(t, origins)),
	}
}

func runtimeConfig(t *testing.T, h *harness, database, slack, ca string, origins []string) service.Config {
	t.Helper()
	values := runtimeSettings(t, database, h.cfg.Schema, runtimeFreeAddress(t), h.cfg.IntegrationEncryptionKey, slack, ca, origins)
	runtimeEnvironment(t, values)
	config, err := service.Environment("delivery")
	must(t, "construct actual delivery environment", err)
	runtimeClientPolicy(t, config.DeliveryClient)
	t.Cleanup(config.DeliveryClient.CloseIdleConnections)
	return config
}

func runtimeClientPolicy(t *testing.T, client *http.Client) *http.Transport {
	t.Helper()
	check(t, client != nil && client.Timeout > 0 && client.Timeout <= 30*time.Second &&
		client.Jar == nil && client.CheckRedirect != nil, "delivery environment lost bounded no-cookie/no-redirect policy")
	transport, ok := client.Transport.(*http.Transport)
	check(t, ok && transport != nil && transport.Proxy == nil && transport.DialContext != nil &&
		transport.DialTLS == nil && transport.DialTLSContext == nil && transport.TLSClientConfig != nil &&
		!transport.TLSClientConfig.InsecureSkipVerify && transport.TLSClientConfig.MinVersion >= tls.VersionTLS12,
		"delivery environment lost direct normal TLS/origin dialing")
	request, err := http.NewRequest("GET", "https://unused.synthetic.invalid", nil)
	must(t, "construct offline redirect-policy probe", err)
	check(t, client.CheckRedirect(request, nil) != nil, "delivery environment permits redirects")
	return transport
}

func runtimeOrigins(t *testing.T, config *service.Config) reflect.Value {
	t.Helper()
	field := reflect.ValueOf(config).Elem().FieldByName("JiraAPIOrigins")
	check(t, field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf([]string{}),
		"ASPM_JIRA_API_ORIGINS must survive Environment as programmatic JiraAPIOrigins []string")
	return field
}

type runtimeOwnership struct {
	client       *http.Client
	transport    *http.Transport
	tls          *tls.Config
	roots        *x509.CertPool
	rootsCopy    *x509.CertPool
	key, origins []byte
	tlsPolicy    []byte
	timeout      time.Duration
	dial, redir  uintptr
	proxy        uintptr
	http2        bool
}

func runtimeTLSPolicy(t *testing.T, p *tls.Config) []byte {
	t.Helper()
	return encoded(t, object{
		"minimum": p.MinVersion, "maximum": p.MaxVersion, "insecure": p.InsecureSkipVerify,
		"hostname": p.ServerName, "protocols": p.NextProtos, "curves": p.CurvePreferences,
		"ciphers": p.CipherSuites, "clientCertificates": len(p.Certificates),
	})
}

func runtimeOwnSnapshot(t *testing.T, config *service.Config) runtimeOwnership {
	t.Helper()
	field := runtimeOrigins(t, config)
	origins := field.Slice(0, field.Cap()).Interface()
	transport := config.DeliveryClient.Transport.(*http.Transport)
	s := runtimeOwnership{
		client: config.DeliveryClient, transport: transport, tls: transport.TLSClientConfig,
		roots: transport.TLSClientConfig.RootCAs, key: bytes.Clone(config.IntegrationEncryptionKey),
		origins: encoded(t, origins), tlsPolicy: runtimeTLSPolicy(t, transport.TLSClientConfig),
		timeout: config.DeliveryClient.Timeout, dial: reflect.ValueOf(transport.DialContext).Pointer(),
		redir: reflect.ValueOf(config.DeliveryClient.CheckRedirect).Pointer(), http2: transport.ForceAttemptHTTP2,
	}
	if s.roots != nil {
		s.rootsCopy = s.roots.Clone()
	}
	if transport.Proxy != nil {
		s.proxy = reflect.ValueOf(transport.Proxy).Pointer()
	}
	return s
}

func (s runtimeOwnership) unchanged(t *testing.T, config *service.Config) {
	t.Helper()
	field := runtimeOrigins(t, config)
	transport := config.DeliveryClient.Transport.(*http.Transport)
	var proxy uintptr
	if transport.Proxy != nil {
		proxy = reflect.ValueOf(transport.Proxy).Pointer()
	}
	check(t, config.DeliveryClient == s.client && transport == s.transport &&
		transport.TLSClientConfig == s.tls && transport.TLSClientConfig.RootCAs == s.roots &&
		config.DeliveryClient.Timeout == s.timeout && config.DeliveryClient.Jar == nil &&
		reflect.ValueOf(transport.DialContext).Pointer() == s.dial &&
		reflect.ValueOf(config.DeliveryClient.CheckRedirect).Pointer() == s.redir &&
		proxy == s.proxy && transport.ForceAttemptHTTP2 == s.http2 &&
		bytes.Equal(config.IntegrationEncryptionKey, s.key) &&
		bytes.Equal(encoded(t, field.Slice(0, field.Cap()).Interface()), s.origins) &&
		bytes.Equal(runtimeTLSPolicy(t, transport.TLSClientConfig), s.tlsPolicy),
		"Run mutated caller-owned client/TLS/key/origin backing storage")
	if s.roots != nil {
		check(t, s.roots.Equal(s.rootsCopy), "Run mutated caller-owned CA pool contents")
	}
}

type runtimeRole struct {
	h                    *harness
	label, address, mode string
	client               *http.Client
	done                 chan struct{}
	err                  error
	cancel               context.CancelFunc
	stop                 func()
	health               []object
}

func (r *runtimeRole) alive() {
	r.h.t.Helper()
	select {
	case <-r.done:
		r.h.t.Fatal("actual delivery entrypoint exited before the required observation; private logs withheld")
	default:
	}
}

func (r *runtimeRole) response(ctx context.Context, method, path string) (int, []byte, error) {
	if r.h.api.Add(1) > 220 {
		return 0, nil, errors.New("shared API and role-health budget exceeded")
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+r.address+path, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if len(data) > 16<<10 {
		return 0, nil, errors.New("role response exceeded its bound")
	}
	r.h.private(data)
	return response.StatusCode, data, err
}

func (r *runtimeRole) ready() {
	t := r.h.t
	t.Helper()
	ctx, cancel := context.WithTimeout(r.h.ctx, 6*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.alive()
		status, data, err := r.response(ctx, "GET", "/readyz")
		if err == nil && status == 200 {
			value := decoded[map[string]string](t, data)
			check(t, value["service"] == "delivery" && value["status"] == "ready" &&
				value["database"] == "reachable" && value["storage"] == "not-required",
				"actual command readiness misstates its role or capabilities")
			return
		}
		select {
		case <-r.done:
			t.Fatal("actual delivery entrypoint failed before readiness; private output withheld")
		case <-ctx.Done():
			t.Fatal("actual delivery entrypoint readiness exceeded its bound")
		case <-ticker.C:
		}
	}
}

func (r *runtimeRole) checkHealth() {
	t := r.h.t
	t.Helper()
	for _, row := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 200},
		{"GET", "/api/v1/session", 404}, {"POST", "/api/v1/login", 404}, {"GET", "/", 404},
	} {
		r.alive()
		ctx, cancel := context.WithTimeout(r.h.ctx, time.Second)
		status, data, err := r.response(ctx, row.method, row.path)
		cancel()
		must(t, "call actual bounded role health surface", err)
		check(t, status == row.status, "delivery exposed core/auth routes or failed actual health")
		observation := object{"method": row.method, "path": row.path, "status": status}
		if status == 200 {
			value := decoded[map[string]string](t, data)
			if row.path == "/healthz" {
				check(t, value["apiVersion"] == "aspm/v1alpha1" && value["service"] == "delivery" && value["status"] == "alive",
					"healthz did not identify the actual delivery role")
			} else {
				check(t, value["service"] == "delivery" && value["status"] == "ready" &&
					value["database"] == "reachable" && value["storage"] == "not-required",
					"delivery readiness inherited storage or lost real DB readiness")
			}
			observation["body"] = value
		}
		r.health = append(r.health, observation)
	}
}

func runtimeStartService(h *harness, config service.Config, label string) *runtimeRole {
	h.t.Helper()
	ctx, cancel := context.WithCancel(h.ctx)
	r := &runtimeRole{h: h, label: label, address: config.Listen, mode: "actual-service.Run", done: make(chan struct{}), cancel: cancel}
	r.client = directClient(h.t, "http://"+config.Listen)
	r.client.Timeout = time.Second
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-r.done:
				check(h.t, r.err == nil || errors.Is(r.err, context.Canceled), "real Run did not shut down cleanly")
			case <-time.After(6 * time.Second):
				h.t.Error("real Run did not stop its listener and held native work within the shutdown bound")
			}
			runtimeEvidence(h.t, label+"-role", object{"mode": r.mode, "health": r.health, "contextCanceled": true})
		})
	}
	h.t.Cleanup(r.stop)
	go func() { r.err = service.Run(ctx, "delivery", config); close(r.done) }()
	r.ready()
	return r
}

func runtimeChildEnvironment(values map[string]string) ([]string, []string) {
	var env, names []string
	for _, name := range []string{"SystemRoot", "WINDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env, names = append(env, name+"="+value), append(names, name)
		}
	}
	for name, value := range values {
		env, names = append(env, name+"="+value), append(names, name)
	}
	sort.Strings(names)
	return env, names
}

func runtimeStartCommand(h *harness, database, slack, ca string, origins []string, label string) *runtimeRole {
	t := h.t
	t.Helper()
	binaryPath, wantHash := os.Getenv("ASPM_JIRA_COMMAND_BINARY"), os.Getenv("ASPM_JIRA_COMMAND_SHA256")
	check(t, filepath.IsAbs(binaryPath) && len(wantHash) == 64, "BLOCKED: runner-built actual main and its SHA256 are required")
	info, err := os.Stat(binaryPath)
	must(t, "locate actual runner-built delivery command", err)
	check(t, info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= 128<<20, "actual command is not a bounded regular binary")
	data, err := os.ReadFile(binaryPath)
	must(t, "hash actual command input", err)
	check(t, digest(data) == wantHash, "actual delivery command differs from the runner's mainbuild input")
	r := &runtimeRole{
		h: h, label: label, address: runtimeFreeAddress(t), mode: "actual-cmd/delivery-worker",
		done: make(chan struct{}),
	}
	values := runtimeSettings(t, database, h.cfg.Schema, r.address, h.cfg.IntegrationEncryptionKey, slack, ca, origins)
	env, names := runtimeChildEnvironment(values)
	command := exec.Command(binaryPath)
	command.Env, command.Stdout, command.Stderr = env, &h.log, &h.log
	command.Dir = filepath.Dir(binaryPath)
	began := time.Now().UTC()
	must(t, "start actual cmd/delivery-worker with explicit process environment", command.Start())
	stopOnDeadline := context.AfterFunc(h.ctx, func() { _ = command.Process.Kill() })
	r.client = directClient(t, "http://"+r.address)
	r.client.Timeout = time.Second
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			stopOnDeadline()
			_ = command.Process.Kill()
			exited := false
			select {
			case <-r.done:
				exited = true
			case <-time.After(5 * time.Second):
				t.Error("owned actual-main PID did not exit within cleanup bound")
			}
			h.private(h.log.data())
			runtimeEvidence(t, label+"-role", object{
				"mode": r.mode, "binarySHA256": wantHash, "pid": command.Process.Pid,
				"startedAt": began, "finishedAt": time.Now().UTC(), "environmentNames": names,
				"health": r.health, "ownedPIDExited": exited, "cleanup": "PID kill, not graceful signal evidence",
			})
		})
	}
	t.Cleanup(r.stop)
	go func() { r.err = command.Wait(); close(r.done) }()
	runtimeEvidence(t, label+"-process", object{
		"pid": command.Process.Pid, "binaryPath": binaryPath, "binarySHA256": wantHash,
		"startedNotBeforeUnixMs": began.UnixMilli(), "startedNotAfterUnixMs": time.Now().UnixMilli(),
	})
	r.ready()
	return r
}

func runtimeAwaitTerminal(h *harness, who actor, id string) delivery {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 6*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for reads := 0; reads < 160; reads++ {
		var state string
		var nativeReceipt, nativeFailure []byte
		var started, attempted, completed *time.Time
		must(h.t, "observe actual durable runtime outcome", h.db.QueryRow(ctx,
			"SELECT state,receipt,failure,dispatch_started_at,create_attempted_at,completed_at FROM "+
				h.table("finding_deliveries")+" WHERE workspace_id=$1 AND id=$2", who.Workspace, id).
			Scan(&state, &nativeReceipt, &nativeFailure, &started, &attempted, &completed))
		if state != "queued" && state != "dispatching" {
			actual := h.delivery(who, id)
			var storedReceipt *receipt
			var storedFailure *failure
			if len(nativeReceipt) != 0 {
				storedReceipt = decoded[*receipt](h.t, nativeReceipt)
			}
			if len(nativeFailure) != 0 {
				storedFailure = decoded[*failure](h.t, nativeFailure)
			}
			check(h.t, actual.State == state && runtimeSameTime(actual.DispatchStartedAt, started) &&
				runtimeSameTime(actual.CreateAttemptedAt, attempted) && runtimeSameTime(actual.CompletedAt, completed) &&
				reflect.DeepEqual(actual.Receipt, storedReceipt) && reflect.DeepEqual(actual.Failure, storedFailure),
				"actual API receipt differs from the scoped committed PG record")
			runtimeEvidence(h.t, "durable-outcome", object{
				"intentID": actual.ID, "profile": actual.Profile, "state": actual.State,
				"createAttemptedAt": actual.CreateAttemptedAt, "completedAt": actual.CompletedAt,
				"receipt": actual.Receipt, "failure": actual.Failure, "scopedPGMatchesAPI": true,
			})
			return actual
		}
		select {
		case <-ctx.Done():
			h.t.Fatal("durable runtime outcome exceeded its context bound")
		case <-ticker.C:
		}
	}
	h.t.Fatal("durable runtime observation count exceeded its bound")
	return delivery{}
}

func runtimeSameTime(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func runtimeKeyMarker(h *harness, who actor, key string) func() error {
	return func() error {
		var id string
		if err := h.db.QueryRow(h.ctx, "SELECT id FROM "+h.table("finding_deliveries")+
			" WHERE workspace_id=$1 AND idempotency_key=$2", who.Workspace, key).Scan(&id); err != nil {
			return errors.New("owned API-created key has no committed delivery")
		}
		return h.marker(id)
	}
}

func runtimeNativeEvidence(t *testing.T, label string, n *jiraServer, plans ...*jiraPlan) {
	t.Helper()
	var details []object
	var requests int32
	for _, p := range plans {
		details = append(details, object{"metadataGET": p.gets.Load(), "createPOST": p.posts.Load()})
		requests += p.gets.Load() + p.posts.Load()
	}
	runtimeEvidence(t, label+"-native", object{
		"jiraHTTP": n.calls.Load(), "slackPOST": n.slackPosts.Load(),
		"forbiddenHTTP": n.forbidden.Load(), "plans": details,
	})
	check(t, n.calls.Load() == requests && requests+n.slackPosts.Load() <= 64 && n.forbidden.Load() == 0,
		"native ledger contains unattributed attempts, forbidden traffic or an exceeded cap")
}
