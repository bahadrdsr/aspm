//go:build integration

package remediation

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

func TestRemediationOwnedRuntimePreflight(t *testing.T) {
	f := newFixture(t)
	table := pgx.Identifier{f.cfg.Database.Schema, "fixture_probe"}.Sanitize()
	_, err := f.db.Exec(f.ctx, "CREATE TABLE "+table+" (value text NOT NULL)")
	must(t, "create owned PG capability probe", err)
	_, err = f.db.Exec(f.ctx, "INSERT INTO "+table+" (value) VALUES ($1)", "owned committed fixture probe")
	must(t, "commit owned PG capability probe", err)
	independent, err := pgx.Connect(f.ctx, f.cfg.Database.URL)
	must(t, "open independent PG fixture connection", err)
	defer independent.Close(f.ctx)
	var got string
	must(t, "read committed PG probe through independent connection", independent.QueryRow(f.ctx, "SELECT value FROM "+table).Scan(&got))
	check(t, got == "owned committed fixture probe", "real PG probe did not persist")
	key, value := f.cfg.Storage.Prefix+"probe", []byte("owned local source-intake fixture")
	_, err = f.store.PutObject(f.ctx, &s3.PutObjectInput{Bucket: aws.String(f.cfg.Storage.Bucket), Key: aws.String(key), Body: bytes.NewReader(value)})
	must(t, "probe only the owned existing local S3 prefix", err)
	t.Log("real loopback PG commit/reopen and local S3 owned-prefix preflight completed; no application delivery semantics executed")
}

func TestRemediationConnectionsRBACRedactionAndAuthenticatedEncryption(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	token := secret(t)
	h.addSecret(token)
	input := object{"profile": slackProfile, "name": "Synthetic channel", "channel": "C123", "token": token, "enabled": true}
	h.json(actor{}, "POST", connectionsPath, input, 401)
	h.json(writer, "POST", connectionsPath, input, 403)
	h.json(viewer, "POST", connectionsPath, input, 403)
	for _, invalid := range []object{
		{"profile": slackProfile, "name": "Synthetic", "channel": "#general", "token": token, "enabled": true},
		{"profile": "jira-cloud-v3", "name": "Synthetic", "channel": "C123", "token": token, "enabled": true},
		{"profile": slackProfile, "name": "Synthetic", "channel": "C123", "token": token},
		{"profile": slackProfile, "name": "Synthetic", "channel": "C123", "token": token, "enabled": true, "endpoint": native.server.URL},
		{"profile": slackProfile, "name": "Synthetic", "channel": "C123", "token": token, "enabled": true, "headers": object{"Authorization": token}},
	} {
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
	}
	first := h.createConnection(h.admin, "First synthetic Slack destination", "C123", token, true)
	second := h.createConnection(h.admin, "Second synthetic Slack destination", "G456", token, false)
	h.decryptForAssertion(h.admin.Workspace, first.ID, token)
	h.decryptForAssertion(h.admin.Workspace, second.ID, token)
	check(t, !bytes.Equal(h.credentials(first.ID), h.credentials(second.ID)), "same token reused a credential ciphertext/nonce")
	list := h.json(viewer, "GET", connectionsPath, nil, 200)
	check(t, len(list.Items) == 2 && list.Total == 2 && list.NextCursor == nil, "members must see the actual small nonsecret connection list")
	allowed := []string{"channel", "createdAt", "credentialConfigured", "enabled", "id", "name", "profile", "revision", "updatedAt", "workspaceId"}
	sort.Strings(allowed)
	for _, item := range list.Items {
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		check(t, reflect.DeepEqual(keys, allowed), "connection metadata exposed or invented fields, possibly secret material")
	}
	h.json(viewer, "GET", connectionsPath+"/"+first.ID, nil, 200)
	h.json(writer, "PATCH", connectionsPath+"/"+first.ID, object{"enabled": false}, 403)
	rotated := secret(t)
	h.addSecret(rotated)
	updated := h.json(h.admin, "PATCH", connectionsPath+"/"+first.ID,
		object{"name": "Renamed synthetic destination", "token": rotated, "enabled": false}, 200).Connection
	check(t, updated.Revision == first.Revision+1 && !updated.Enabled && updated.CredentialConfigured &&
		updated.Channel == first.Channel, "selected connection updates must preserve omitted fields and advance revision")
	h.decryptForAssertion(h.admin.Workspace, first.ID, rotated)
	other := h.secondWorkspace()
	h.json(other, "GET", connectionsPath+"/"+first.ID, nil, 404)
	check(t, len(h.json(other, "GET", connectionsPath, nil, 200).Items) == 0, "connection list crossed a workspace boundary")
	f := h.importFinding(h.admin, "No automatic notification on import")
	check(t, native.count() == 0, "connection creation/update or import dispatched a native notification")
	active := h.json(h.admin, "PATCH", connectionsPath+"/"+first.ID, object{"enabled": true}, 200).Connection
	job := h.enqueue(h.admin, f.ID, active, "credential-identity-negative", 202)
	// Only an owned credential is corrupted; no queue state or receipt is fabricated.
	_, err := h.db.Exec(h.ctx, "UPDATE "+h.table("integration_connections")+" SET credential_ciphertext=$2 WHERE id=$1 AND workspace_id=$3",
		first.ID, h.credentials(second.ID), h.admin.Workspace)
	must(t, "substitute an owned credential for the authenticated-identity negative", err)
	worker := h.worker(native, "credential-identity", 4*time.Second)
	process(t, h.ctx, worker, true)
	blocked := h.delivery(h.admin, job.ID)
	check(t, blocked.State == "blocked" && blocked.Failure != nil && blocked.Failure.Code == "credential-unavailable" &&
		blocked.DispatchStartedAt == nil && native.count() == 0, "credential bound to another connection reached native dispatch")
	h.noStoredPlaintext()
	catalog := h.json(h.admin, "GET", "/api/v1/integrations/catalog", nil, 200)
	check(t, len(catalog.Items) == 8, "the existing eight-family roadmap changed")
	for _, item := range catalog.Items {
		verification := decodeItem[struct{ State string }](t, item["liveVerification"])
		check(t, verification.State != "passed", "owned protocol fixtures cannot confer live-vendor certification")
	}
}

func TestRemediationImmutableScopedEnqueueAndIdempotency(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	first := h.importFinding(h.admin, "Immutable source asset")
	second := h.importFinding(h.admin, "Second scoped source asset")
	other := h.secondWorkspace()
	foreign := h.importFinding(other, "Foreign source asset")
	c := h.createConnection(h.admin, "Owned destination", "C123", secret(t), true)
	otherConnection := h.createConnection(other, "Other workspace destination", "G456", secret(t), true)
	h.enqueue(viewer, first.ID, c, "viewer-denied", 403)
	h.enqueue(h.admin, first.ID, otherConnection, "foreign-connection", 404)
	h.enqueue(h.admin, foreign.ID, c, "foreign-finding", 404)
	for key, value := range (object{
		"requestedBy": h.admin.User.ID, "approvalRef": "client-forged-approval", "workspaceId": other.Workspace,
		"endpoint": native.server.URL, "deepLink": "https://untrusted.synthetic.invalid", "prior": object{"state": "confirmed"},
		"payload": object{"title": "caller-supplied replacement"}, "headers": object{"Authorization": "caller-forged"},
	}) {
		body := object{"connectionId": c.ID, "idempotencyKey": "forged-" + key, key: value}
		h.json(writer, "POST", deliveriesPath(first.ID), body, 400)
	}
	d := h.enqueue(writer, first.ID, c, "durable-key", 202)
	check(t, d.ID != "" && d.State == "queued" && d.WorkspaceID == writer.Workspace &&
		d.FindingID == first.ID && d.ConnectionID == c.ID && d.ConnectionRevision == c.Revision &&
		d.Profile == slackProfile && d.Channel == c.Channel && d.RequestedBy == writer.User.ID &&
		d.Payload == payload(first) && d.DispatchStartedAt == nil && d.Receipt == nil && d.Failure == nil,
		"enqueue did not persist the exact server-derived immutable authorization/payload")
	replay := h.enqueue(writer, first.ID, c, "durable-key", 200)
	check(t, reflect.DeepEqual(replay, d), "identical enqueue did not replay the same durable job")
	h.enqueue(writer, second.ID, c, "durable-key", 409)
	h.enqueue(h.admin, first.ID, c, "durable-key", 409)
	h.json(other, "GET", deliveryPath(d.ID), nil, 404)
	check(t, len(h.json(viewer, "GET", deliveriesPath(first.ID), nil, 200).Items) == 1, "member delivery history is not scoped/persisted")
	h.json(h.admin, "PATCH", "/api/v1/assets/"+first.AssetID, object{"name": "Changed after explicit authorization"}, 200)
	h.enqueue(writer, first.ID, c, "durable-key", 409)
	check(t, h.delivery(h.admin, d.ID).Payload == d.Payload, "a source refresh rewrote an immutable delivery payload")
	h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"channel": "G789"}, 200)
	h.enqueue(writer, first.ID, c, "durable-key", 409)
	check(t, h.delivery(h.admin, d.ID).ConnectionRevision == c.Revision, "connection revision silently moved an existing intent")
	disabled := h.createConnection(h.admin, "Disabled destination", "C987", secret(t), false)
	h.enqueue(h.admin, second.ID, disabled, "disabled", 409)
	check(t, native.count() == 0, "enqueue or idempotency checks performed native I/O")
	h.noStoredPlaintext()
}

func TestRemediationIndependentNativeSlackReceiptAndFreshReplay(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	f := h.importFinding(h.admin, "Canonical Slack source asset")
	token := secret(t)
	c := h.createConnection(h.admin, "Selected native Slack destination", "G456", token, true)
	d := h.enqueue(h.admin, f.ID, c, "confirmed-intent", 202)
	check(t, native.count() == 0 && h.delivery(h.admin, d.ID).State == "queued", "API dispatched instead of leaving durable queued work")
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": "Asset renamed after enqueue"}, 200)
	h.reopen()
	plan := native.plan(token, c.Channel, "ok", func() error { return h.marker(d.ID) })
	worker := h.worker(native, "independent", 4*time.Second)
	process(t, h.ctx, worker, true)
	assertSlack(t, awaitCall(t, plan), c.Channel, d.Payload, h.reportText)
	done := h.delivery(h.admin, d.ID)
	check(t, done.State == "confirmed" && done.Receipt != nil &&
		done.Receipt.RemoteID == c.Channel+":1789560000.123456" && done.DispatchStartedAt != nil &&
		done.CompletedAt != nil && done.Failure == nil, "actual native acknowledgement was not durably preserved")
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
	must(t, "close first worker before fresh replay", worker.Close())
	h.reopen()
	fresh := h.worker(native, "fresh", 4*time.Second)
	process(t, h.ctx, fresh, false)
	process(t, h.ctx, fresh, false)
	// The original binding is immutable; restore the asset through its API before replaying the same key.
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": f.AssetName}, 200)
	replayed := h.enqueue(h.admin, f.ID, c, "confirmed-intent", 200)
	check(t, replayed.ID == done.ID && replayed.State == "confirmed" && reflect.DeepEqual(replayed.Receipt, done.Receipt),
		"confirmed receipt did not survive fresh core/worker opens")
	check(t, native.count() == 1, "durable confirmed replay caused a duplicate native write")
	h.noStoredPlaintext()
}

func TestRemediationPredispatchActorAndConnectionDenials(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	f := h.importFinding(h.admin, "Authorization source asset")
	writer := h.addUser("analyst")
	c := h.createConnection(h.admin, "Revocation destination", "C123", secret(t), true)
	d := h.enqueue(writer, f.ID, c, "actor-revoked", 202)
	h.json(h.admin, "PATCH", "/api/v1/users/"+writer.User.ID, object{"role": "viewer"}, 200)
	worker := h.worker(native, "authorization", 4*time.Second)
	_, err := worker.ProcessNext(h.ctx)
	must(t, "settle revoked intent without native I/O", err)
	blocked := h.delivery(h.admin, d.ID)
	check(t, blocked.State == "blocked" && blocked.Failure != nil && blocked.Failure.Code == "authorization-revoked" &&
		blocked.DispatchStartedAt == nil, "current actor write revocation did not block dispatch")
	for _, kind := range []string{"disable", "revision"} {
		conn := h.createConnection(h.admin, "Connection guard "+kind, "G456", secret(t), true)
		job := h.enqueue(h.admin, f.ID, conn, "connection-"+kind, 202)
		update := object{"enabled": false}
		if kind == "revision" {
			update = object{"channel": "C789"}
		}
		h.json(h.admin, "PATCH", connectionsPath+"/"+conn.ID, update, 200)
		_, err = worker.ProcessNext(h.ctx)
		must(t, "settle changed connection without native I/O", err)
		value := h.delivery(h.admin, job.ID)
		check(t, value.State == "blocked" && value.Failure != nil &&
			(value.Failure.Code == "connection-disabled" || value.Failure.Code == "connection-changed") &&
			value.DispatchStartedAt == nil, "disabled/revised connection did not block its old intent")
	}
	h.json(h.admin, "PATCH", "/api/v1/users/"+writer.User.ID, object{"role": "analyst"}, 200)
	fresh := h.worker(native, "no-resurrection", 4*time.Second)
	process(t, h.ctx, fresh, false)
	check(t, native.count() == 0, "pre-dispatch denial or later regrant sent an old notification")
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func TestRemediationConcurrentOwnershipCancellationAndAvailableSQLPool(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	f := h.importFinding(h.admin, "Concurrent source asset")
	token := secret(t)
	c := h.createConnection(h.admin, "Concurrent destination", "C123", token, true)
	d := h.enqueue(h.admin, f.ID, c, "concurrent-intent", 202)
	first, second := h.worker(native, "owner-a", 4*time.Second), h.worker(native, "owner-b", 4*time.Second)
	plan := native.plan(token, c.Channel, "held", func() error { return h.marker(d.ID) })
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := first.ProcessNext(ctx); done <- err }()
	awaitCall(t, plan)
	check(t, h.delivery(h.admin, d.ID).State == "dispatching", "held native request lacks durable dispatching state")
	probe, stop := context.WithTimeout(h.ctx, 750*time.Millisecond)
	must(t, "held provider HTTP must not retain the single worker SQL connection", first.Ping(probe))
	process(t, probe, second, false)
	h.request(probe, h.admin, "GET", "/api/v1/work", nil, 200)
	h.request(probe, h.admin, "GET", connectionsPath, nil, 200)
	stop()
	check(t, native.count() == 1, "concurrent worker repeated a claimed native write")
	cancel()
	select {
	case err := <-done:
		check(t, err == nil || errors.Is(err, context.Canceled), "worker cancellation returned an unexpected infrastructure error")
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not honor context cancellation of native I/O")
	}
	select {
	case <-plan.cancelled:
	case <-time.After(time.Second):
		t.Fatal("owned TLS request did not observe client cancellation")
	}
	uncertain := h.delivery(h.admin, d.ID)
	check(t, uncertain.State == "uncertain" && uncertain.Receipt == nil && uncertain.DispatchStartedAt != nil,
		"possible native write became queued/failed-successfully instead of uncertain")
	process(t, h.ctx, second, false)
	check(t, native.count() == 1, "canceled possible write became eligible for blind replay")
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func TestRemediationRealWorkerCrashRecoversDispatchAsUncertain(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	f := h.importFinding(h.admin, "Crash recovery source asset")
	token := secret(t)
	c := h.createConnection(h.admin, "Crash destination", "G456", token, true)
	d := h.enqueue(h.admin, f.ID, c, "crash-intent", 202)
	plan := native.plan(token, c.Channel, "held", func() error { return h.marker(d.ID) })
	child := startWorkerProcess(h, native, 750*time.Millisecond)
	awaitCall(t, plan)
	before := h.delivery(h.admin, d.ID)
	check(t, before.State == "dispatching" && before.DispatchStartedAt != nil, "real child did not commit dispatch before native POST")
	child.kill(t)
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		must(t, "read actual owned lease expiry", h.db.QueryRow(h.ctx,
			"SELECT lease_until<=clock_timestamp() FROM "+h.table("finding_deliveries")+" WHERE id=$1", d.ID).Scan(&expired))
		if expired {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("crashed worker lease did not expire within its configured bound")
		case <-ticker.C:
		}
	}
	h.reopen()
	recovery := h.worker(native, "recovery", time.Second)
	process(t, h.ctx, recovery, true)
	after := h.delivery(h.admin, d.ID)
	check(t, after.State == "uncertain" && after.Failure != nil && after.Failure.Code == "uncertain" &&
		after.Receipt == nil && after.DispatchStartedAt != nil, "crashed dispatch was requeued or fabricated a receipt")
	check(t, native.count() == 1, "lease recovery resent a possibly accepted native write")
	must(t, "close recovery worker", recovery.Close())
	fresh := h.worker(native, "post-recovery", time.Second)
	process(t, h.ctx, fresh, false)
	process(t, h.ctx, fresh, false)
	check(t, native.count() == 1, "fresh worker forgot durable uncertainty")
}

func TestRemediationNativeFailuresAndRetryAfterNeverAutoRetry(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	f := h.importFinding(h.admin, "Native failure source asset")
	token := secret(t)
	c := h.createConnection(h.admin, "Native failure destination", "C123", token, true)
	worker := h.worker(native, "native-errors", 4*time.Second)
	for _, row := range []struct {
		mode, state, code, native string
		status                    int
		retry                     int64
	}{
		{"auth", "failed", "auth", "invalid_auth", 200, 0},
		{"channel", "failed", "unavailable", "channel_not_found", 200, 0},
		{"429", "rate-limited", "rate_limited", "ratelimited", 429, 7},
		{"500", "uncertain", "uncertain", "service_unavailable", 503, 0},
		{"malformed", "uncertain", "uncertain", "", 200, 0},
		{"missing", "uncertain", "uncertain", "", 200, 0},
		{"drop", "uncertain", "uncertain", "", 0, 0},
		{"redirect", "blocked", "scope", "", 307, 0},
	} {
		t.Run(row.mode, func(t *testing.T) {
			parent := h.fixture.t
			h.fixture.t = t
			defer func() { h.fixture.t = parent }()
			d := h.enqueue(h.admin, f.ID, c, "native-"+row.mode, 202)
			prior := native.count()
			plan := native.plan(token, c.Channel, row.mode, func() error { return h.marker(d.ID) })
			start := time.Now()
			process(t, h.ctx, worker, true)
			awaitCall(t, plan)
			actual := h.delivery(h.admin, d.ID)
			check(t, actual.State == row.state && actual.Receipt == nil && actual.Failure != nil &&
				actual.Failure.Code == row.code && actual.Failure.NativeCode == row.native &&
				actual.Failure.HTTPStatus == row.status && actual.Failure.RetryAfterSeconds == row.retry &&
				!actual.Failure.Retryable, "genuine native failure/uncertainty/retry-after mapping was lost")
			process(t, h.ctx, worker, false)
			process(t, h.ctx, worker, false)
			check(t, native.count() == prior+1 && time.Since(start) < 2*time.Second,
				"native error triggered automatic retry/backoff/sleep instead of durable terminal data")
			h.json(h.admin, "POST", deliveryPath(d.ID)+"/retry", object{}, 404)
			h.noStoredPlaintext()
		})
	}
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func TestRemediationExplicitOptionalKeyAndMinimalWorkerRole(t *testing.T) {
	h, native := newHarness(t, false), newSlack(t)
	token := secret(t)
	h.addSecret(token)
	rejected := h.json(h.admin, "POST", connectionsPath, object{
		"profile": slackProfile, "name": "Keyless connection", "channel": "C123", "token": token, "enabled": true,
	}, 503)
	check(t, rejected.Error.Code == "unavailable", "missing integration key must fail clearly, not derive another secret")
	h.json(h.admin, "GET", "/api/v1/session", nil, 200)
	h.importFinding(h.admin, "Unrelated keyless intake still works")
	for _, key := range [][]byte{nil, randomBytes(t, 31), randomBytes(t, 33)} {
		if len(key) != 0 {
			h.addSecret(string(key))
			h.addSecret(base64.StdEncoding.EncodeToString(key))
		}
		config := h.workerConfig(native, "invalid-key", time.Second)
		config.EncryptionKey = key
		worker, err := Production.OpenWorker(h.ctx, config)
		check(t, worker == nil && err != nil, "worker startup accepted a missing/invalid explicit encryption key")
		h.noSecrets([]byte(err.Error()))
	}
	setCoreEnvironment(t, h.cfg)
	check(t, Production.EnvironmentKey != nil && Production.RunCore != nil, "real core service configuration bindings are missing")
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", "")
	value, err := Production.EnvironmentKey()
	must(t, "parse unrelated core with no optional integration key", err)
	check(t, len(value) == 0, "core derived an integration key from other fixture credentials")
	for _, encoded := range []string{"not-base64", base64.StdEncoding.EncodeToString(randomBytes(t, 31)), base64.StdEncoding.EncodeToString(randomBytes(t, 33))} {
		t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", encoded)
		_, err = Production.EnvironmentKey()
		check(t, err != nil, "core accepted a malformed explicitly supplied integration key")
	}
	key := randomBytes(t, 32)
	h.addSecret(string(key))
	h.addSecret(base64.StdEncoding.EncodeToString(key))
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
	value, err = Production.EnvironmentKey()
	must(t, "parse the explicit independent integration key", err)
	check(t, bytes.Equal(value, key), "core configuration did not preserve the explicit key")
	h.cfg.EncryptionKey = key
	runRealCoreConnection(t, h, token)
	clearStorageEnvironment(t)
	for _, kind := range []string{"missing-client", "unsigned-gateway"} {
		bad := h.workerConfig(native, kind, time.Second)
		if kind == "missing-client" {
			bad.Client = nil
		} else {
			bad.SlackEndpoint = strings.Replace(native.server.URL, "https://", "http://", 1)
		}
		worker, openErr := Production.OpenWorker(h.ctx, bad)
		check(t, worker == nil && openErr != nil, "worker accepted an absent approved transport or unsigned gateway")
	}
	config := h.workerConfig(native, "minimal-role", time.Second)
	config.SlackEndpoint = ""
	worker, err := Production.OpenWorker(h.ctx, config)
	must(t, "open database/key/approved-client-only worker without S3/bootstrap", err)
	check(t, worker != nil, "minimal worker constructor returned nil")
	defer worker.Close()
	_, handler := worker.(http.Handler)
	check(t, !handler, "delivery worker exposed the core/auth HTTP handler")
	valueOf := reflect.Indirect(reflect.ValueOf(worker))
	check(t, valueOf.Kind() != reflect.Struct || !valueOf.FieldByName("Handler").IsValid(), "delivery worker carries the application handler")
	process(t, h.ctx, worker, false)
	check(t, native.count() == 0, "configuration/connection creation/minimal startup performed a native write")
}

func setCoreEnvironment(t *testing.T, config coreConfig) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_") && !strings.HasPrefix(name, "ASPM_REMEDIATION_") {
			t.Setenv(name, "")
		}
	}
	for name, value := range map[string]string{
		"ASPM_DATABASE_URL": config.Database.URL, "ASPM_SCHEMA": config.Database.Schema, "ASPM_DB_MAX_CONNECTIONS": "3",
		"ASPM_S3_ENDPOINT": config.Storage.Endpoint, "ASPM_S3_BUCKET": config.Storage.Bucket,
		"ASPM_S3_PREFIX": config.Storage.Prefix, "ASPM_S3_REGION": config.Storage.Region,
		"ASPM_S3_ACCESS_KEY": config.Storage.AccessKey, "ASPM_S3_SECRET_KEY": config.Storage.SecretKey,
		"ASPM_PUBLIC_ORIGIN": config.PublicOrigin,
	} {
		t.Setenv(name, value)
	}
}

func clearStorageEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "ASPM_S3_") || strings.HasPrefix(name, "ASPM_REMEDIATION_S3_") ||
			strings.HasPrefix(name, "AWS_") || strings.Contains(name, "BOOTSTRAP_TOKEN") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func runRealCoreConnection(t *testing.T, h *harness, token string) {
	t.Helper()
	must(t, "close direct core before real service-role reopen", h.app.Close())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned core-role address", err)
	address := listener.Addr().String()
	must(t, "release address for actual core listener", listener.Close())
	assets := filepath.Join(required(t, "ASPM_REMEDIATION_ARTIFACT_DIR"), "owned-empty-ui-"+nonce(t))
	must(t, "create owned empty UI directory for API-only service probe", os.MkdirAll(assets, 0700))
	t.Cleanup(func() { _ = os.Remove(assets) })
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Production.RunCore(ctx, h.cfg, address, assets) }()
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("owned core role did not stop during cleanup")
			}
		}
	}()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			return nil, errors.New("core role probe cannot dial another destination")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, requestErr := client.Get("http://" + address + "/healthz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case err = <-done:
			stopped = true
			must(t, "actual core exited before readiness", err)
			t.Fatal("actual core exited before readiness")
		case <-deadline.C:
			t.Fatal("actual core role failed to become ready")
		case <-ticker.C:
		}
	}
	target, err := url.Parse("http://" + address)
	must(t, "parse owned forwarding-only core target", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	h.app.Handler = proxy
	h.createConnection(h.admin, "Actual service configured key", "C123", token, true)
	h.decryptForAssertion(h.admin.Workspace,
		decodeItem[connection](t, h.json(h.admin, "GET", connectionsPath, nil, 200).Items[0]).ID, token)
	cancel()
	select {
	case err = <-done:
		stopped = true
		check(t, err == nil || errors.Is(err, context.Canceled), "actual core role shutdown failed")
	case <-time.After(5 * time.Second):
		t.Fatal("actual core role did not stop within its bound")
	}
	h.app.Handler = nil
}
