//go:build integration

package remediation

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func must(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; private values withheld)", operation, err)
	}
}

func check(t *testing.T, condition bool, message string) {
	t.Helper()
	if !condition {
		t.Fatal(message)
	}
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	_, err := rand.Read(value)
	must(t, "generate private synthetic fixture input", err)
	return value
}

func nonce(t *testing.T) string { return hex.EncodeToString(randomBytes(t, 12)) }
func secret(t *testing.T) string {
	return "synthetic-remediation-" + base64.RawURLEncoding.EncodeToString(randomBytes(t, 24))
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	must(t, "encode fixture input", err)
	return data
}

func decodeItem[T any](t *testing.T, value any) T {
	t.Helper()
	var result T
	must(t, "decode returned DTO", json.Unmarshal(encode(t, value), &result))
	return result
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(data)
}
func (l *lockedLog) bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.b.Bytes())
}

type fixture struct {
	t         *testing.T
	ctx       context.Context
	cfg       coreConfig
	db        *pgxpool.Pool
	store     *s3.Client
	log       lockedLog
	secretsMu sync.Mutex
	secrets   []string
	ciphers   []string
	ambient   atomic.Int32
}

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	check(t, value != "", "BLOCKED: required private fixture variable "+name+" is missing")
	return value
}

func ownedURL(t *testing.T, raw string, schemes ...string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	must(t, "parse selected loopback fixture URL", err)
	allowed := false
	for _, scheme := range schemes {
		allowed = allowed || u.Scheme == scheme
	}
	check(t, allowed && u.Hostname() == "127.0.0.1" && u.Port() != "", "only explicitly selected loopback fixtures are permitted")
	return u
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	id := nonce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx}
	f.cfg = coreConfig{
		Database: databaseConfig{
			URL: required(t, "ASPM_REMEDIATION_DATABASE_URL"), Schema: "remediation_" + id,
			ApplicationName: "remediation-" + id, MaxConnections: 1,
		},
		Storage: storageConfig{
			Endpoint: required(t, "ASPM_REMEDIATION_S3_ENDPOINT"), Bucket: required(t, "ASPM_REMEDIATION_S3_BUCKET"),
			AccessKey: required(t, "ASPM_REMEDIATION_S3_ACCESS_KEY"), SecretKey: required(t, "ASPM_REMEDIATION_S3_SECRET_KEY"),
			Prefix: "remediation/" + id + "/", Region: "us-east-1",
		},
		EncryptionKey: randomBytes(t, 32), BootstrapToken: secret(t), PublicOrigin: publicOrigin, LogOutput: &f.log,
	}
	dbURL := ownedURL(t, f.cfg.Database.URL, "postgres", "postgresql")
	check(t, dbURL.User != nil && dbURL.User.Username() != "", "explicit fixture DB identity required")
	password, present := dbURL.User.Password()
	check(t, present && password != "", "explicit fixture DB password required")
	storeURL := ownedURL(t, f.cfg.Storage.Endpoint, "http", "https")
	check(t, storeURL.User == nil && storeURL.RawQuery == "" && storeURL.Fragment == "" &&
		!strings.ContainsAny(f.cfg.Storage.Bucket, "/\\:"), "invalid selected local object-store fixture")
	for _, value := range []string{f.cfg.Database.URL, password, f.cfg.Storage.AccessKey, f.cfg.Storage.SecretKey,
		f.cfg.BootstrapToken, string(f.cfg.EncryptionKey), base64.StdEncoding.EncodeToString(f.cfg.EncryptionKey),
		hex.EncodeToString(f.cfg.EncryptionKey), fmt.Sprint(f.cfg.EncryptionKey)} {
		f.addSecret(value)
	}
	original := http.DefaultTransport
	guard := original.(*http.Transport).Clone()
	guard.Proxy = nil
	guard.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != storeURL.Host {
			f.ambient.Add(1)
			return nil, errors.New("unapproved ambient HTTP destination blocked")
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = guard
	t.Cleanup(func() {
		http.DefaultTransport = original
		guard.CloseIdleConnections()
		check(t, f.ambient.Load() == 0, "production attempted ambient HTTP instead of its selected capabilities")
		f.noSecrets(f.log.bytes())
	})
	pc, err := pgxpool.ParseConfig(f.cfg.Database.URL)
	must(t, "parse real fixture pool", err)
	pc.MaxConns, pc.MinConns, pc.ConnConfig.ConnectTimeout = 2, 0, 3*time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = f.cfg.Database.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pc)
	must(t, "open real fixture PostgreSQL", err)
	t.Cleanup(f.db.Close)
	must(t, "authenticate real fixture PostgreSQL", f.db.Ping(ctx))
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.cfg.Database.Schema}.Sanitize())
	must(t, "create only the task-owned schema", err)
	httpClient := &http.Client{Transport: guard.Clone(), Timeout: 4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects denied") }}
	t.Cleanup(httpClient.CloseIdleConnections)
	st := f.cfg.Storage
	f.store = s3.New(s3.Options{
		Region: st.Region, BaseEndpoint: aws.String(st.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(st.AccessKey, st.SecretKey, ""),
		HTTPClient:  httpClient, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	t.Cleanup(f.cleanup)
	_, err = f.store.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(st.Bucket)})
	must(t, "authenticate the existing loopback fixture bucket", err)
	return f
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if !regexp.MustCompile(`^remediation_[a-f0-9]{24}$`).MatchString(f.cfg.Database.Schema) ||
		f.cfg.Storage.Prefix != "remediation/"+strings.TrimPrefix(f.cfg.Database.Schema, "remediation_")+"/" {
		f.t.Error("refusing cleanup outside the task-owned schema/prefix")
		return
	}
	pages := s3.NewListObjectsV2Paginator(f.store, &s3.ListObjectsV2Input{
		Bucket: aws.String(f.cfg.Storage.Bucket), Prefix: aws.String(f.cfg.Storage.Prefix), MaxKeys: aws.Int32(100),
	})
	for index := 0; pages.HasMorePages() && index < 4; index++ {
		page, err := pages.NextPage(ctx)
		if err != nil {
			f.t.Errorf("owned object cleanup failed (%T; private values withheld)", err)
			break
		}
		for _, item := range page.Contents {
			if !strings.HasPrefix(aws.ToString(item.Key), f.cfg.Storage.Prefix) {
				f.t.Error("refusing object cleanup outside task prefix")
				continue
			}
			_, err = f.store.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(f.cfg.Storage.Bucket), Key: item.Key})
			if err != nil {
				f.t.Errorf("owned object deletion failed (%T; private values withheld)", err)
			}
		}
		if pages.HasMorePages() {
			f.t.Error("owned object cleanup exceeded its four-page bound")
		}
	}
	_, err := f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.cfg.Database.Schema}.Sanitize()+" CASCADE")
	if err != nil {
		f.t.Errorf("owned schema cleanup failed (%T; private values withheld)", err)
	}
}

func (f *fixture) addSecret(value string) {
	if value == "" {
		return
	}
	f.secretsMu.Lock()
	defer f.secretsMu.Unlock()
	f.secrets = append(f.secrets, value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value)))
}

func (f *fixture) noPlaintext(data []byte) {
	f.t.Helper()
	f.secretsMu.Lock()
	defer f.secretsMu.Unlock()
	for _, value := range f.secrets {
		check(f.t, !bytes.Contains(data, []byte(value)), "private fixture material escaped in a response/error/log")
	}
}

func (f *fixture) noSecrets(data []byte) {
	f.t.Helper()
	f.noPlaintext(data)
	f.secretsMu.Lock()
	defer f.secretsMu.Unlock()
	for _, value := range f.ciphers {
		check(f.t, !bytes.Contains(data, []byte(value)), "persisted ciphertext escaped through a response/error/log")
	}
}

func (f *fixture) table(name string) string {
	return pgx.Identifier{f.cfg.Database.Schema, "app_" + name}.Sanitize()
}

type harness struct {
	*fixture
	app        core
	admin      actor
	apiCalls   atomic.Int32
	reportText string
}

func newHarness(t *testing.T, key bool) *harness {
	t.Helper()
	check(t, Production.OpenCore != nil && Production.OpenWorker != nil,
		"RED: real core encryption configuration and independent delivery worker constructors are missing")
	h := &harness{fixture: newFixture(t), reportText: secret(t)}
	if !key {
		h.cfg.EncryptionKey = nil
	}
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			must(t, "close real core before owned cleanup", h.app.Close())
		}
	})
	password := secret(t)
	h.addSecret(password)
	enrolled := h.json(actor{}, "POST", "/api/v1/bootstrap", object{
		"workspaceName": "Synthetic remediation workspace", "name": "Synthetic remediation admin",
		"email": "admin@remediation.invalid", "password": password,
	}, 201, "X-ASPM-Bootstrap-Token", h.cfg.BootstrapToken)
	h.admin = h.login("admin@remediation.invalid", password, enrolled.Workspace.ID)
	return h
}

func (h *harness) open() {
	var err error
	h.app, err = Production.OpenCore(h.ctx, h.cfg)
	must(h.t, "open actual core application", err)
	check(h.t, h.app.Handler != nil && h.app.Close != nil && h.app.ProcessImports != nil, "forwarding binding returned incomplete core")
}
func (h *harness) reopen() {
	must(h.t, "close prior core", h.app.Close())
	h.app = core{}
	h.open()
}

func (h *harness) request(ctx context.Context, who actor, method, target string, body any, want int, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	check(h.t, h.apiCalls.Add(1) <= 160, "bounded remediation API request budget exceeded")
	var data []byte
	if body != nil {
		data = encode(h.t, body)
	}
	request := httptest.NewRequest(method, h.cfg.PublicOrigin+target, bytes.NewReader(data)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.cfg.PublicOrigin)
	if who.Workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.Workspace)
	}
	if who.Cookie != nil {
		request.AddCookie(who.Cookie)
	}
	for index := 0; index < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(response, request)
	if response.Code != want {
		h.t.Fatalf("%s %s returned %d, want %d; response/private data withheld", method, target, response.Code, want)
	}
	h.noSecrets(response.Body.Bytes())
	if strings.HasPrefix(target, connectionsPath) && want >= 200 && want < 300 {
		var envelope map[string]any
		must(h.t, "inspect connection metadata shape", json.Unmarshal(response.Body.Bytes(), &envelope))
		for key := range envelope {
			check(h.t, key == "apiVersion" || key == "dataOrigin" || key == "connection" ||
				key == "items" || key == "total" || key == "nextCursor", "connection envelope exposed an undeclared field")
		}
		allowed := map[string]bool{"id": true, "workspaceId": true, "profile": true, "name": true, "channel": true,
			"enabled": true, "credentialConfigured": true, "revision": true, "createdAt": true, "updatedAt": true}
		validate := func(value any) {
			item, ok := value.(map[string]any)
			check(h.t, ok && len(item) == len(allowed), "connection response is not the exact nonsecret metadata DTO")
			for key := range item {
				check(h.t, allowed[key], "connection response exposed an undeclared/secret field")
			}
		}
		if value, present := envelope["connection"]; present {
			validate(value)
		}
		if items, present := envelope["items"].([]any); present {
			for _, value := range items {
				validate(value)
			}
		}
	}
	return response
}

func (h *harness) json(who actor, method, target string, body any, status int, headers ...string) apiReply {
	h.t.Helper()
	response := h.request(h.ctx, who, method, target, body, status, headers...)
	var reply apiReply
	must(h.t, "decode actual API response", json.Unmarshal(response.Body.Bytes(), &reply))
	check(h.t, reply.APIVersion == apiVersion, "API version mismatch")
	if status >= 400 {
		check(h.t, reply.Error != nil && reply.Error.Code != "" && reply.Error.Message != "" &&
			reply.Error.RequestID != "" && !reply.Error.Retryable, "failure must be nonretryable, versioned and safely identified")
	}
	return reply
}

func (h *harness) login(email, password, workspaceID string) actor {
	h.t.Helper()
	response := h.request(h.ctx, actor{}, "POST", "/api/v1/login", object{"email": email, "password": password}, 200)
	var reply apiReply
	must(h.t, "decode authenticated identity", json.Unmarshal(response.Body.Bytes(), &reply))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "aspm_session" {
			check(h.t, cookie.HttpOnly && cookie.Secure && cookie.Path == "/", "real protected session cookie required")
			h.addSecret(cookie.Value)
			return actor{User: reply.User, Workspace: workspaceID, Cookie: cookie}
		}
	}
	h.t.Fatal("actual login returned no session cookie")
	return actor{}
}

func (h *harness) addUser(role string) actor {
	h.t.Helper()
	password, email := secret(h.t), nonce(h.t)+"@remediation.invalid"
	h.addSecret(password)
	created := h.json(h.admin, "POST", "/api/v1/users", object{
		"name": "Synthetic " + role, "email": email, "password": password, "role": role,
	}, 201)
	user := h.login(email, password, h.admin.Workspace)
	check(h.t, user.User.ID == created.User.ID, "real user identity did not survive login")
	return user
}

func (h *harness) secondWorkspace() actor {
	h.t.Helper()
	reply := h.json(h.admin, "POST", "/api/v1/workspaces", object{"name": "Synthetic other remediation workspace"}, 201)
	who := h.admin
	who.Workspace = reply.Workspace.ID
	check(h.t, who.Workspace != "" && who.Workspace != h.admin.Workspace, "distinct actual workspace required")
	return who
}

func (h *harness) importFinding(who actor, label string) finding {
	h.t.Helper()
	asset := h.json(who, "POST", "/api/v1/assets", object{
		"name": label, "kind": "repository", "environment": "synthetic", "criticality": "medium", "tags": []string{}, "ownerId": nil,
	}, 201)
	raw, err := os.ReadFile(filepath.Join("..", "acceptance", "testdata", "sarif.json"))
	must(h.t, "read unchanged existing SARIF fixture", err)
	var source object
	must(h.t, "decode source fixture privately", json.Unmarshal(raw, &source))
	run := source["runs"].([]any)[0].(map[string]any)
	result := run["results"].([]any)[0].(map[string]any)
	result["message"] = object{"text": h.reportText}
	result["properties"] = object{
		"endpoint": "https://untrusted.synthetic.invalid/never-contact", "deepLink": "https://untrusted.synthetic.invalid/not-authority",
		"headers": object{"Authorization": "scanner-supplied-not-authority"}, "approvalRef": "scanner-not-approval",
	}
	data := encode(h.t, source)
	queued := h.json(who, "POST", "/api/v1/imports", object{
		"apiVersion": apiVersion, "assetId": asset.Asset.ID, "format": "sarif", "report": string(data),
		"sourceId": "synthetic-remediation-source", "scanId": nonce(h.t),
		"scope":        object{"id": "synthetic-remediation-scope", "revision": "1", "branch": "main"},
		"sourceScanAt": "2026-09-01T08:00:00Z", "collectedAt": "2026-09-02T08:00:00Z",
		"sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete",
	}, 202)
	check(h.t, queued.Import.State == "queued", "core must acknowledge actual queued intake")
	must(h.t, "process real PG/S3 source intake", h.app.ProcessImports(h.ctx))
	done := h.json(who, "GET", "/api/v1/imports/"+queued.Import.ID, nil, 200)
	check(h.t, done.Import.State == "succeeded", "real source fixture did not produce a canonical finding")
	work := h.json(who, "GET", "/api/v1/work?q="+url.QueryEscape(label), nil, 200)
	check(h.t, len(work.Items) == 1, "fixture must select one actual canonical finding")
	id := decodeItem[struct{ ID string }](h.t, work.Items[0]).ID
	return h.finding(who, id)
}

func (h *harness) finding(who actor, id string) finding {
	return h.json(who, "GET", "/api/v1/findings/"+id, nil, 200).Finding
}

func (h *harness) createConnection(who actor, name, channel, token string, enabled bool) connection {
	h.t.Helper()
	h.addSecret(token)
	reply := h.json(who, "POST", connectionsPath, object{
		"profile": slackProfile, "name": name, "channel": channel, "token": token, "enabled": enabled,
	}, 201)
	value := reply.Connection
	check(h.t, value.ID != "" && value.WorkspaceID == who.Workspace && value.Profile == slackProfile &&
		value.Name == name && value.Channel == channel && value.Enabled == enabled && value.CredentialConfigured &&
		value.Revision == 1 && !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero(), "invalid canonical nonsecret connection metadata")
	h.credentials(value.ID)
	return value
}

func deliveriesPath(id string) string { return "/api/v1/findings/" + id + "/deliveries" }
func deliveryPath(id string) string   { return "/api/v1/integrations/deliveries/" + id }

func (h *harness) enqueue(who actor, findingID string, connection connection, key string, status int) delivery {
	h.t.Helper()
	return h.json(who, "POST", deliveriesPath(findingID), object{"connectionId": connection.ID, "idempotencyKey": key}, status).Delivery
}
func (h *harness) delivery(who actor, id string) delivery {
	return h.json(who, "GET", deliveryPath(id), nil, 200).Delivery
}

func payload(f finding) notification {
	return notification{Title: f.Title, Body: "Severity: " + f.Severity + "\nAsset: " + f.AssetName,
		DeepLink: publicOrigin + "/#/work?finding=" + url.QueryEscape(f.ID)}
}

func (h *harness) credentials(id string) []byte {
	h.t.Helper()
	var data []byte
	must(h.t, "read actual encrypted credential", h.db.QueryRow(h.ctx,
		"SELECT credential_ciphertext FROM "+h.table("integration_connections")+" WHERE id=$1", id).Scan(&data))
	h.secretsMu.Lock()
	h.ciphers = append(h.ciphers, string(data), hex.EncodeToString(data), base64.StdEncoding.EncodeToString(data), fmt.Sprint(data))
	h.secretsMu.Unlock()
	return data
}

func aad(workspace, id string) []byte {
	return []byte("aspm/slack-credential/v1\x00" + workspace + "\x00" + id)
}

func (h *harness) decryptForAssertion(workspace, id, token string) {
	h.t.Helper()
	data := h.credentials(id)
	block, err := aes.NewCipher(h.cfg.EncryptionKey)
	must(h.t, "construct independent standard AES-256 assertion", err)
	aead, err := cipher.NewGCM(block)
	must(h.t, "construct independent standard GCM assertion", err)
	check(h.t, len(data) > 1+aead.NonceSize()+aead.Overhead() && data[0] == 1, "credential must use the version-1 AES-GCM envelope")
	nonceEnd := 1 + aead.NonceSize()
	plain, err := aead.Open(nil, data[1:nonceEnd], data[nonceEnd:], aad(workspace, id))
	must(h.t, "authenticate persisted credential with exact workspace/connection AAD", err)
	check(h.t, string(plain) == token, "persisted credential does not authenticate to the supplied token")
	_, err = aead.Open(nil, data[1:nonceEnd], data[nonceEnd:], aad(workspace, "different-connection"))
	check(h.t, err != nil, "credential must be cryptographically bound to connection identity")
	_, err = aead.Open(nil, data[1:nonceEnd], data[nonceEnd:], aad("different-workspace", id))
	check(h.t, err != nil, "credential must be cryptographically bound to workspace identity")
}

func (h *harness) noStoredPlaintext() {
	h.t.Helper()
	for _, name := range []string{"integration_connections", "finding_deliveries"} {
		rows, err := h.db.Query(h.ctx, "SELECT to_jsonb(item)::text FROM "+h.table(name)+" item")
		must(h.t, "inspect actual private integration rows", err)
		defer rows.Close()
		for rows.Next() {
			var data string
			must(h.t, "read stored integration payload privately", rows.Scan(&data))
			h.noPlaintext([]byte(data))
		}
		must(h.t, "finish private row inspection", rows.Err())
		rows.Close()
	}
}

func (h *harness) marker(id string) error {
	var state, owner string
	var started, expires *time.Time
	var fence int64
	var live bool
	err := h.db.QueryRow(h.ctx, "SELECT state,worker_id,dispatch_started_at,lease_until,fence,lease_until>clock_timestamp() FROM "+
		h.table("finding_deliveries")+" WHERE id=$1", id).Scan(&state, &owner, &started, &expires, &fence, &live)
	if err != nil {
		return errors.New("dispatch marker is not independently readable")
	}
	if state != "dispatching" || started == nil || expires == nil || owner == "" || fence < 1 || !live {
		return errors.New("native POST arrived before a committed live fenced dispatch-start marker")
	}
	return nil
}

func (h *harness) worker(server *slackServer, name string, lease time.Duration) deliveryWorker {
	h.t.Helper()
	config := h.workerConfig(server, name, lease)
	worker, err := Production.OpenWorker(h.ctx, config)
	must(h.t, "open independent production delivery worker", err)
	check(h.t, worker != nil, "production delivery worker is nil")
	h.t.Cleanup(func() { must(h.t, "close independent delivery worker", worker.Close()) })
	return worker
}

func (h *harness) workerConfig(server *slackServer, name string, lease time.Duration) workerConfig {
	db := h.cfg.Database
	db.ApplicationName += "-" + name
	return workerConfig{Database: db, EncryptionKey: h.cfg.EncryptionKey, WorkerID: name + "-" + nonce(h.t),
		LeaseDuration: lease, SlackEndpoint: server.server.URL, Client: server.client, LogOutput: &h.log}
}

func process(t *testing.T, ctx context.Context, worker deliveryWorker, expected bool) {
	t.Helper()
	processed, err := worker.ProcessNext(ctx)
	must(t, "process one durable delivery step", err)
	check(t, processed == expected, "worker returned an incorrect eligible-work result")
}

func assertFindingUnchanged(t *testing.T, before, after finding) {
	t.Helper()
	check(t, before.WorkflowState == after.WorkflowState && before.SourceState == after.SourceState &&
		before.Disposition == after.Disposition && before.VerifiedResolution == after.VerifiedResolution &&
		before.Evidence == after.Evidence && bytes.Equal(encode(t, before.Notes), encode(t, after.Notes)),
		"delivery changed finding workflow, source verification, risk, evidence or notes")
}

func fingerprint(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
