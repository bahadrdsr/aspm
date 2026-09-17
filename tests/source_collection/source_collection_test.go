//go:build integration

package source_collection

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/jackc/pgx/v5"
)

func TestSourceCollectionOwnedIOPreflight(t *testing.T) {
	f := newFixture(t)
	table := pgx.Identifier{f.database.Schema, "fixture_probe"}.Sanitize()
	_, err := f.db.Exec(f.ctx, "CREATE TABLE "+table+" (value text NOT NULL)")
	must(t, "create owned PG capability probe", err)
	_, err = f.db.Exec(f.ctx, "INSERT INTO "+table+" (value) VALUES ('owned source fixture probe')")
	must(t, "commit owned PG probe", err)
	conn, err := pgx.Connect(f.ctx, f.database.DatabaseURL)
	must(t, "open independent real PG connection", err)
	defer conn.Close(f.ctx)
	var value string
	must(t, "independently read committed probe", conn.QueryRow(f.ctx, "SELECT value FROM "+table).Scan(&value))
	require(t, value == "owned source fixture probe", "PG fixture did not preserve a real commit")
	data := []byte("owned integrity-checked source storage probe\n")
	key := f.selected.Prefix + "fixture/probe/" + strings.TrimPrefix(digest(data), "sha256:")
	_, err = f.adminS3.PutObject(f.ctx, &s3.PutObjectInput{Bucket: aws.String(f.selected.Bucket), Key: aws.String(key), Body: bytes.NewReader(data)})
	must(t, "write only the owned real S3 preflight object", err)
	reader, err := evidence.OpenReader(f.ctx, evidence.Config{Endpoint: f.reader.server.URL, Bucket: f.selected.Bucket,
		Prefix: f.selected.Prefix, Region: f.selected.Region, AccessKey: f.selected.AccessKey, SecretKey: f.selected.SecretKey, Timeout: 3 * time.Second})
	must(t, "open existing no-bucket-probe evidence reader", err)
	defer reader.Close()
	stream, err := reader.Open(f.ctx, "fixture", evidence.Ref{WorkspaceID: "fixture", Bucket: f.selected.Bucket, Key: key, SHA256: digest(data), SizeBytes: int64(len(data))})
	must(t, "read real selected integrity evidence", err)
	got, err := io.ReadAll(stream)
	must(t, "verify original evidence bytes", errors.Join(err, stream.Close()))
	require(t, bytes.Equal(got, data), "existing reader did not preserve/integrity-check the actual S3 bytes")
	t.Log("real owned PG commit/reopen and existing digest-verifying local S3 reader PASS; source business semantics are not exercised by preflight")
}

func TestSourceCollectionConnectionRBACEncryptionAndRequiredCapabilities(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	input := map[string]any{"profile": sourceProfile, "name": "Owned source", "repository": "owned-owner/repository", "token": token, "enabled": true}
	h.remember(token)
	h.json(actor{}, "POST", sourcesPath, input, 401)
	h.json(writer, "POST", sourcesPath, input, 403)
	h.json(viewer, "POST", sourcesPath, input, 403)
	for _, field := range []string{"endpoint", "headers", "requestedBy", "approvalRef"} {
		bad := map[string]any{"profile": sourceProfile, "name": "Owned source", "repository": "owned-owner/repository", "token": token, "enabled": true, field: "not-authority"}
		h.json(h.admin, "POST", sourcesPath, bad, 400)
	}
	h.json(h.admin, "POST", sourcesPath, map[string]any{"profile": sourceProfile, "name": "No explicit flag", "repository": "owned-owner/repository", "token": token}, 400)
	h.json(h.admin, "POST", sourcesPath, map[string]any{"profile": sourceProfile, "name": "Missing token", "repository": "owned-owner/repository", "enabled": true}, 400)
	source := h.source(h.admin, "owned-owner/repository", token)
	domain := "aspm/source-credential/v1\x00github-cloud-app"
	h.assertCredential("source_connections", h.admin.Workspace, source.ID, token, domain)
	list := h.json(viewer, "GET", sourcesPath+"?limit=1", nil, 200)
	require(t, len(list.Items) == 1 && list.Total == 1, "viewer did not receive real nonsecret workspace metadata")
	allowed := map[string]bool{"id": true, "workspaceId": true, "profile": true, "name": true, "repository": true,
		"enabled": true, "credentialConfigured": true, "revision": true, "createdAt": true, "updatedAt": true}
	require(t, len(list.Items[0]) == len(allowed), "source metadata has secret or undeclared fields")
	for key := range list.Items[0] {
		require(t, allowed[key], "source API returned a secret/undeclared field")
	}
	h.json(writer, "PATCH", sourcesPath+"/"+source.ID, map[string]any{"enabled": false}, 403)
	changedToken := secret(t)
	h.remember(changedToken)
	updated := h.json(h.admin, "PATCH", sourcesPath+"/"+source.ID, map[string]any{"name": "Renamed source", "token": changedToken}, 200).Source
	require(t, updated.Revision == source.Revision+1 && updated.Repository == source.Repository && updated.Enabled, "token/name update lost selected source revision or omitted values")
	h.assertCredential("source_connections", h.admin.Workspace, source.ID, changedToken, domain)
	other := h.otherWorkspace()
	h.json(other, "GET", sourcesPath+"/"+source.ID, nil, 404)
	require(t, h.json(other, "GET", sourcesPath, nil, 200).Total == 0, "source metadata crossed a workspace")
	slackToken := secret(t)
	h.remember(slackToken)
	slack := h.json(h.admin, "POST", "/api/v1/integrations/connections", map[string]any{
		"profile": "slack-workspace-bot", "name": "Unchanged Slack AAD", "channel": "C123", "token": slackToken, "enabled": true,
	}, 201)
	h.assertCredential("integration_connections", h.admin.Workspace, slack.Connection.ID, slackToken, "aspm/slack-credential/v1")
	preflightWorker := h.openWorker(h.workerConfig(native, "source-preflight"))
	for _, reason := range []string{"disabled", "revision"} {
		if reason == "revision" {
			h.json(h.admin, "PATCH", sourcesPath+"/"+source.ID, map[string]any{"enabled": true}, 200)
		}
		queued := h.enqueue(h.admin, source.ID, "connection-preflight-"+reason, 202)
		update := map[string]any{"enabled": false}
		if reason == "revision" {
			update = map[string]any{"name": "Changed before collection"}
		}
		h.json(h.admin, "PATCH", sourcesPath+"/"+source.ID, update, 200)
		_, err := preflightWorker.ProcessNext(h.ctx)
		must(t, "settle disabled/revised source before any provider read", err)
		denied := h.collection(h.admin, queued.ID)
		require(t, denied.State == "blocked" && denied.Failure != nil &&
			(denied.Failure.Code == "connection-disabled" || denied.Failure.Code == "connection-changed") &&
			denied.RecordCount == 0 && denied.AssetID == nil && native.count() == 0,
			"current source enabled/revision guard did not prevent provider I/O")
	}
	h.config.Key = nil
	h.reopen()
	h.json(h.admin, "POST", sourcesPath, input, 503)
	h.json(h.admin, "GET", "/api/v1/assets", nil, 200)
	h.config.Key = h.key
	h.config.CollectionStorage = nil
	h.reopen()
	h.enqueue(h.admin, source.ID, "missing-selected-storage", 503)
	for _, missing := range []string{"key", "storage-access", "storage-secret", "client", "insecure-client", "http-gateway"} {
		config := h.workerConfig(native, "missing-capability")
		switch missing {
		case "key":
			config.Key = nil
		case "storage-access":
			config.Storage.AccessKey = ""
		case "storage-secret":
			config.Storage.SecretKey = ""
		case "client":
			config.Client = nil
		case "insecure-client":
			config.Client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: time.Second}
		case "http-gateway":
			config.Endpoint = strings.Replace(native.server.URL, "https:", "http:", 1)
		}
		worker, err := Production.OpenWorker(h.ctx, config)
		require(t, worker == nil && err != nil, "source worker accepted a missing explicit capability")
		h.noSecrets([]byte(err.Error()))
	}
	require(t, native.count() == 0 && h.writer.puts.Load() == 0, "connection/configuration handling performed provider or evidence I/O")
	h.noRawDatabaseOrSecrets()
}

func TestSourceCollectionScopedImmutableQueueReplayAndPaging(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	source := h.source(h.admin, "owned-owner/repository", token)
	second := h.source(h.admin, "owned-owner/second", token)
	page := h.json(viewer, "GET", sourcesPath+"?limit=1", nil, 200)
	require(t, len(page.Items) == 1 && page.Total == 2 && page.NextCursor != nil, "source metadata did not use real bounded continuation")
	next := h.json(viewer, "GET", sourcesPath+"?limit=1&cursor="+*page.NextCursor, nil, 200)
	require(t, len(next.Items) == 1 && next.NextCursor == nil && page.Items[0]["id"] != next.Items[0]["id"], "source list continuation duplicated or lost a row")
	h.enqueue(viewer, source.ID, "read-only", 403)
	for _, key := range []string{"repository", "endpoint", "headers", "workspaceId", "requestedBy", "approvalRef", "profile"} {
		h.json(writer, "POST", sourcesPath+"/"+source.ID+"/collections", map[string]any{"idempotencyKey": "forged-" + key, key: "not-authority"}, 400)
	}
	job := h.enqueue(writer, source.ID, "scoped-immutable", 202)
	require(t, job.ID != "" && job.State == "queued" && !job.Complete && job.WorkspaceID == writer.Workspace &&
		job.SourceID == source.ID && job.ConnectionRevision == source.Revision && job.Repository == source.Repository &&
		job.RequestedBy == writer.ID && job.RecordCount == 0 && job.AssetID == nil, "core did not leave an immutable actor/source/revision-bound queued collection")
	replay := h.enqueue(writer, source.ID, "scoped-immutable", 200)
	require(t, reflect.DeepEqual(job, replay), "same authorized idempotency key did not return the same durable collection")
	h.enqueue(writer, second.ID, "scoped-immutable", 409)
	h.enqueue(h.admin, source.ID, "scoped-immutable", 409)
	other := h.otherWorkspace()
	h.enqueue(other, source.ID, "foreign-source", 404)
	h.json(other, "GET", collectionPath(job.ID), nil, 404)
	h.json(other, "GET", recordsPath(job.ID), nil, 404)
	h.json(h.admin, "PATCH", sourcesPath+"/"+source.ID, map[string]any{"repository": "owned-owner/renamed"}, 200)
	h.enqueue(writer, source.ID, "scoped-immutable", 409)
	stored := h.collection(writer, job.ID)
	require(t, stored.Repository == source.Repository && stored.ConnectionRevision == source.Revision, "source revision silently rewrote immutable queued work")
	h.enqueue(writer, second.ID, "page-1", 202)
	h.enqueue(writer, second.ID, "page-2", 202)
	history := h.json(viewer, "GET", sourcesPath+"/"+second.ID+"/collections?limit=1", nil, 200)
	require(t, history.Total == 2 && len(history.Items) == 1 && history.NextCursor != nil, "collection summaries lack real scoped pagination")
	follow := h.json(viewer, "GET", sourcesPath+"/"+second.ID+"/collections?limit=1&cursor="+*history.NextCursor, nil, 200)
	require(t, follow.NextCursor == nil && len(follow.Items) == 1 && follow.Items[0]["id"] != history.Items[0]["id"], "collection continuation is not durable/scoped")
	h.reopen()
	require(t, h.collection(writer, job.ID).ID == job.ID && native.count() == 0 && h.writer.puts.Load() == 0, "core reopen/queue API dispatched or lost work")
}

func TestSourceCollectionIndependentNativeEvidenceAndAtomicAssetCommit(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	script := native.install("ok", "owned-owner/repository", "420001", "")
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	source := h.source(h.admin, script.repository, token)
	job := h.enqueue(writer, source.ID, "first-native-collection", 202)
	h.reopen()
	config := h.workerConfig(native, "independent")
	gate := newCommitGate(h.table("assets"))
	config.Tracer = gate
	t.Cleanup(gate.allow)
	worker := h.openWorker(config)
	done := make(chan error, 1)
	go func() { _, err := worker.ProcessNext(h.ctx); done <- err }()
	await(t, gate.arrived, "actual finalization transaction")
	require(t, gate.liveTransaction(), "asset insertion did not occur inside the real collection finalization transaction")
	require(t, len(h.assets(h.admin)) == 0, "uncommitted collected asset became visible before snapshot finalization")
	pending := h.collection(writer, job.ID)
	require(t, pending.State == "collecting" && pending.AssetID == nil && pending.RecordCount == 0, "collection metadata committed before the linked asset")
	gate.allow()
	select {
	case err := <-done:
		must(t, "finalize collected asset and snapshot atomically", err)
	case <-time.After(5 * time.Second):
		t.Fatal("collection did not complete after owned finalization hold")
	}
	finished := h.collection(viewer, job.ID)
	require(t, finished.State == "succeeded" && finished.Complete && finished.RecordCount == 3 && finished.AssetID != nil &&
		finished.RepositoryID != nil && *finished.RepositoryID == "420001" && finished.CollectedAt != nil && finished.CompletedAt != nil &&
		len(finished.Gaps) == 0 && finished.Failure == nil, "native selected-feed completion was not durably represented")
	assets := h.assets(h.admin)
	require(t, len(assets) == 1 && assets[0].ID == *finished.AssetID && assets[0].Name == script.repository &&
		assets[0].Kind == "repository" && assets[0].Criticality == "medium" && assets[0].OwnerID == nil &&
		assets[0].Environment == "" && len(assets[0].Tags) == 0, "initial discovered repository asset has invented human ownership/decisions")
	expected := map[string][]byte{"repository:420001": script.rawRepository, "finding:7": script.first, "finding:8": script.second}
	records := h.records(viewer, job.ID)
	require(t, len(records) == 3 && native.count() == 3, "selected native pagination or exact record count was lost")
	for _, item := range records {
		want, ok := expected[item.Kind+":"+item.ExternalID]
		require(t, ok && item.SourceScanAt == nil && item.NativeRunID == "", "alert polling invented a scanner time/run or wrong native identity")
		require(t, item.Evidence.SHA256 == digest(want) && item.Evidence.SizeBytes == int64(len(want)), "record evidence metadata does not match exact native bytes")
		raw := h.request(h.ctx, viewer, "GET", evidencePath(job.ID, item.ID), nil, 200).Body.Bytes()
		require(t, bytes.Equal(raw, want), "authorized evidence endpoint normalized or rewrote native record bytes")
		ref := h.storedRef(item.ID)
		require(t, ref.WorkspaceID == viewer.Workspace && ref.SHA256 == digest(want), "PG did not preserve the trusted scoped evidence reference")
		object, err := h.adminS3.GetObject(h.ctx, &s3.GetObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key)})
		must(t, "read real stored native bytes independently", err)
		stored, err := io.ReadAll(io.LimitReader(object.Body, ref.SizeBytes+1))
		must(t, "complete independent S3 read", errors.Join(err, object.Body.Close()))
		require(t, bytes.Equal(stored, want), "real S3 does not contain the exact native record bytes")
		if item.Kind == "finding" {
			require(t, item.ParentID == "420001" && item.SourceUpdatedAt != nil && item.State != "", "alert parent/native state/update provenance was lost")
			state, severity, location, stamp, page := "open", "high", "src/owned.go:17", "2026-09-17T10:00:00Z", "1"
			if item.ExternalID == "8" {
				state, severity, location, stamp, page = "dismissed", "low", "src/other.go:23", "2026-09-17T11:00:00Z", "2"
			}
			updated, err := time.Parse(time.RFC3339, stamp)
			must(t, "parse fixed source update time", err)
			require(t, item.State == state && item.Severity == severity && item.Location == location && item.SourceUpdatedAt.Equal(updated),
				"native alert state/update/severity/location was reinterpreted")
			rawURL, err := url.Parse(item.RawURL)
			must(t, "inspect preserved native request provenance", err)
			require(t, rawURL.Scheme+"://"+rawURL.Host == native.server.URL &&
				rawURL.Path == "/repos/"+script.repository+"/code-scanning/alerts" && rawURL.Query().Get("page") == page &&
				rawURL.Query().Get("per_page") == "1", "record lost exact selected-feed URL provenance")
		} else {
			require(t, item.RawURL == native.server.URL+"/repos/"+script.repository && item.SourceUpdatedAt == nil,
				"repository poll fabricated scanner/update provenance")
		}
	}
	h.noRawDatabaseOrSecrets("RAW_REPO_ONLY_420001", "RAW_ALERT_A_ONLY", "RAW_ALERT_B_ONLY")
	require(t, h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "source poll created normalized findings")
	for _, table := range []string{"findings", "observations", "imports", "coverage"} {
		var count int
		must(t, "read only existing scanner/normalization metadata", h.db.QueryRow(h.ctx, "SELECT count(*) FROM "+h.table(table)).Scan(&count))
		require(t, count == 0, "source alert polling fabricated normalized findings, scan/import runs or coverage")
	}
	other := h.otherWorkspace()
	h.json(other, "GET", recordsPath(job.ID), nil, 404)
	h.request(h.ctx, other, "GET", evidencePath(job.ID, records[0].ID), nil, 404)
	ref := h.storedRef(records[0].ID)
	corrupt := bytes.Repeat([]byte("x"), int(ref.SizeBytes))
	_, err := h.adminS3.PutObject(h.ctx, &s3.PutObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key), Body: bytes.NewReader(corrupt)})
	must(t, "corrupt only a test-owned storage object for real integrity-negative evidence", err)
	denied := h.request(h.ctx, viewer, "GET", evidencePath(job.ID, records[0].ID), nil, 503)
	require(t, !bytes.Contains(denied.Body.Bytes(), corrupt), "digest-failing evidence escaped as a successful body")
}

func TestSourceCollectionStableAssetPreservesHumanEditsAndRejectsIdentityRebind(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	native.install("ok", "owned-owner/repository", "420001", "")
	source := h.source(h.admin, "owned-owner/repository", token)
	worker := h.openWorker(h.workerConfig(native, "stable-asset"))
	first := h.enqueue(h.admin, source.ID, "first", 202)
	process(t, h.ctx, worker, true)
	assetID := h.collection(h.admin, first.ID).AssetID
	require(t, assetID != nil, "initial collection did not link an asset")
	owner := h.addUser("analyst")
	edited := h.json(h.admin, "PATCH", "/api/v1/assets/"+*assetID, map[string]any{
		"name": "Human repository label", "ownerId": owner.ID, "environment": "production", "criticality": "critical", "tags": []string{"human", "retained"},
	}, 200).Asset
	h.reopen()
	must(t, "close worker before independent repeated collection", worker.Close())
	fresh := h.openWorker(h.workerConfig(native, "stable-reopen"))
	for index, sourceID := range []string{source.ID, h.source(h.admin, "owned-owner/repository", token).ID} {
		native.install("ok", "owned-owner/repository", "420001", "")
		job := h.enqueue(h.admin, sourceID, []string{"repeat", "same-upstream-second-source"}[index], 202)
		process(t, h.ctx, fresh, true)
		value := h.collection(h.admin, job.ID)
		assets := h.assets(h.admin)
		require(t, value.AssetID != nil && *value.AssetID == *assetID && len(assets) == 1 && reflect.DeepEqual(assets[0], edited),
			"repeated/reopened native discovery duplicated the upstream asset or clobbered human decisions")
	}
	h.json(h.admin, "PATCH", sourcesPath+"/"+source.ID, map[string]any{"repository": "owned-owner/renamed"}, 200)
	native.install("ok", "owned-owner/renamed", "420001", "")
	renamed := h.enqueue(h.admin, source.ID, "same-stable-id-renamed-repository", 202)
	process(t, h.ctx, fresh, true)
	renamedResult := h.collection(h.admin, renamed.ID)
	require(t, renamedResult.AssetID != nil && *renamedResult.AssetID == *assetID && len(h.assets(h.admin)) == 1 &&
		reflect.DeepEqual(h.assets(h.admin)[0], edited), "upstream rename with the same repository ID clobbered or duplicated the human asset")
	native.install("ok", "owned-owner/renamed", "999999", "")
	conflict := h.enqueue(h.admin, source.ID, "identity-replaced-upstream", 202)
	process(t, h.ctx, fresh, true)
	failed := h.collection(h.admin, conflict.ID)
	require(t, failed.State == "failed" && !failed.Complete && failed.Failure != nil && failed.Failure.Code == "identity-conflict",
		"pinned source repository identity silently rebound")
	require(t, len(h.assets(h.admin)) == 1 && reflect.DeepEqual(h.assets(h.admin)[0], edited), "conflicting identity changed another linked asset")
	require(t, h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "native alert states created/closed normalized findings")
}

func TestSourceCollectionPartialNativeFailuresAndSafeRetryMetadata(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	script := native.install("partial", "owned-owner/repository", "420001", "")
	source := h.source(h.admin, script.repository, token)
	worker := h.openWorker(h.workerConfig(native, "partial"))
	job := h.enqueue(h.admin, source.ID, "partial", 202)
	process(t, h.ctx, worker, true)
	partial := h.collection(h.admin, job.ID)
	require(t, partial.State == "partial" && !partial.Complete && partial.RecordCount == 2 && partial.AssetID != nil &&
		len(partial.Gaps) != 0 && partial.Failure != nil && partial.Failure.Code == "auth" && partial.Failure.HTTPStatus == 403,
		"partial native records/gaps were discarded or presented as complete")
	for _, item := range h.records(h.admin, job.ID) {
		want := script.rawRepository
		if item.Kind == "finding" {
			want = script.first
		}
		require(t, bytes.Equal(h.request(h.ctx, h.admin, "GET", evidencePath(job.ID, item.ID), nil, 200).Body.Bytes(), want),
			"partial collection lost exact authorized prior evidence")
	}
	for _, row := range []struct {
		mode, code string
		status     int
	}{
		{"auth", "auth", 403}, {"quota", "rate_limited", 403}, {"429", "rate_limited", 429},
		{"scope", "scope", 0}, {"oversize", "limit", 200}, {"redirect", "scope", 307}, {"protocol", "protocol", 200},
	} {
		native.install(row.mode, source.Repository, "420001", "")
		before := native.count()
		failed := h.enqueue(h.admin, source.ID, "failure-"+row.mode, 202)
		process(t, h.ctx, worker, true)
		result := h.collection(h.admin, failed.ID)
		require(t, result.State == "failed" && !result.Complete && result.RecordCount == 0 && result.Failure != nil &&
			result.Failure.Code == row.code && result.Failure.HTTPStatus == row.status && !result.Failure.Retryable,
			"native source failure classification or no-success fallback boundary was lost")
		if row.mode == "429" {
			require(t, result.Failure.RetryAfterSeconds == 7, "actual 429 retry-after was lost")
		}
		if row.mode == "quota" {
			require(t, result.Failure.RetryAfterSeconds >= 7 && result.Failure.RetryAfterSeconds <= 12, "actual 403 quota delay was confused with authorization")
		}
		process(t, h.ctx, worker, false)
		require(t, native.count() == before+1, "terminal source failure triggered hidden native read retry")
	}
	require(t, h.json(h.admin, "GET", "/api/v1/work", nil, 200).Total == 0, "partial/failure collection changed canonical findings")
	h.noRawDatabaseOrSecrets("RAW_REPO_ONLY_420001", "RAW_ALERT_A_ONLY")
}

func TestSourceCollectionCancellationRevocationAndIOPoolAvailability(t *testing.T) {
	for _, mode := range []string{"native-cancel", "native-revoke", "storage-revoke", "storage-source-revision"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			token := secret(t)
			native := newGitHub(t, token)
			hold := "alerts-2"
			if strings.HasPrefix(mode, "storage-") {
				hold = ""
			}
			script := native.install("ok", "owned-owner/repository", "420001", hold)
			writer := h.addUser("analyst")
			source := h.source(h.admin, script.repository, token)
			job := h.enqueue(writer, source.ID, "owned-cancellation", 202)
			worker := h.openWorker(h.workerConfig(native, "cancellation"))
			var storage *storageGate
			if strings.HasPrefix(mode, "storage-") {
				storage = h.writer.holdPut()
				t.Cleanup(storage.allow)
			}
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := worker.ProcessNext(ctx); done <- err }()
			if storage != nil {
				await(t, storage.arrived, "held actual S3 publication")
			} else {
				await(t, script.arrived, "held actual selected GitHub read")
			}
			probe, stop := context.WithTimeout(h.ctx, 750*time.Millisecond)
			must(t, "provider/storage I/O must not retain single worker SQL slot", worker.Ping(probe))
			h.request(probe, h.admin, "GET", "/api/v1/assets", nil, 200)
			if mode == "storage-source-revision" {
				h.request(probe, h.admin, "PATCH", sourcesPath+"/"+source.ID, map[string]string{"name": "Changed during evidence publication"}, 200)
			} else if mode != "native-cancel" {
				h.request(probe, h.admin, "PATCH", "/api/v1/users/"+writer.ID, map[string]string{"role": "viewer"}, 200)
			} else {
				cancel()
			}
			stop()
			if storage != nil {
				await(t, storage.done, "S3 cancellation after actor revocation")
			} else {
				await(t, script.cancelled, "native context cancellation")
			}
			select {
			case err := <-done:
				if err != nil {
					h.noSecrets([]byte(err.Error()))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("collection worker did not stop revoked/canceled I/O")
			}
			value := h.collection(h.admin, job.ID)
			expected, code := "failed", "canceled"
			if mode != "native-cancel" {
				expected, code = "blocked", "authorization-revoked"
			}
			if mode == "storage-source-revision" {
				expected, code = "blocked", "connection-changed"
			}
			require(t, value.State == expected && !value.Complete && value.Failure != nil && value.Failure.Code == code &&
				value.AssetID == nil && value.RecordCount == 0 && len(h.assets(h.admin)) == 0,
				"canceled/revoked collection committed an asset or accepted snapshot")
			fresh := h.openWorker(h.workerConfig(native, "post-cancel"))
			before := native.count()
			process(t, h.ctx, fresh, false)
			require(t, native.count() == before, "old denied collection was resurrected")
		})
	}
}

func TestSourceCollectionExpiredFinalizationCannotCommitAssetOrSnapshot(t *testing.T) {
	h := newHarness(t)
	token := secret(t)
	native := newGitHub(t, token)
	native.install("ok", "owned-owner/repository", "420001", "")
	source := h.source(h.admin, "owned-owner/repository", token)
	job := h.enqueue(h.admin, source.ID, "fenced-finalization", 202)
	gate := newCommitGate(h.table("assets"))
	t.Cleanup(gate.allow)
	config := h.workerConfig(native, "expiring")
	config.LeaseDuration = 750 * time.Millisecond
	config.Tracer = gate
	worker := h.openWorker(config)
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := worker.ProcessNext(ctx); done <- err }()
	await(t, gate.arrived, "owned finalization scheduling hold")
	require(t, gate.liveTransaction() && len(h.assets(h.admin)) == 0, "fencing fixture must hold a real uncommitted asset transaction")
	var started time.Time
	must(t, "read authoritative PG clock", h.db.QueryRow(h.ctx, "SELECT clock_timestamp()").Scan(&started))
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		var expired bool
		must(t, "observe real lease expiry without mutating queue state", h.db.QueryRow(h.ctx,
			"SELECT COALESCE(lease_until<=clock_timestamp(),true) AND clock_timestamp()>=$2 FROM "+h.table("source_collections")+" WHERE id=$1",
			job.ID, started.Add(1500*time.Millisecond)).Scan(&expired))
		if expired {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("owned finalization lease did not expire within its bound")
		case <-ticker.C:
		}
	}
	gate.allow()
	select {
	case err := <-done:
		if err != nil {
			h.noSecrets([]byte(err.Error()))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expired finalizer did not exit")
	}
	require(t, len(h.assets(h.admin)) == 0, "expired/fenced worker committed its provisional repository asset")
	h.reopen()
	recovery := h.openWorker(h.workerConfig(native, "expired-recovery"))
	_, err := recovery.ProcessNext(h.ctx)
	must(t, "terminally settle the real expired collection", err)
	failed := h.collection(h.admin, job.ID)
	require(t, failed.State == "failed" && !failed.Complete && failed.RecordCount == 0 && failed.AssetID == nil && failed.Failure != nil &&
		(failed.Failure.Code == "lease-expired" || failed.Failure.Code == "lease-lost"), "expired lease produced a committed snapshot or hidden retry")
	process(t, h.ctx, recovery, false)
	require(t, native.count() == 3 && len(h.assets(h.admin)) == 0, "fresh worker replayed expired read work or exposed a stale asset")
}
