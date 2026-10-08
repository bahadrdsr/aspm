//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type v24HistoryResource struct {
	kind, directory, id string
	payload             []byte
}

func v24EligibleHistory(fixture v23HistoryFixture) []v24HistoryResource {
	return []v24HistoryResource{
		{
			kind: "finding-decision-event", directory: "finding-decision-events",
			id: fixture.decisionIDs[0], payload: fixture.payloads[fixture.decisionIDs[0]],
		},
		{
			kind: "notification-policy-revision", directory: "notification-policy-revisions",
			id: fixture.revisionIDs[2], payload: fixture.payloads[fixture.revisionIDs[2]],
		},
		{
			kind: "finding-change-event", directory: "finding-change-events",
			id: fixture.evaluatedChangeID, payload: fixture.payloads[fixture.evaluatedChangeID],
		},
		{
			kind: "notification-policy-event", directory: "notification-policy-events",
			id: fixture.noDeliveryPolicyEventID, payload: fixture.payloads[fixture.noDeliveryPolicyEventID],
		},
	}
}

func v24QueueHistory(t *testing.T, h *harness) (retentionPreview, retentionRun, object) {
	t.Helper()
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	approval := v23Approval(preview, "Approve exact V24 history archive execution.", "v24-history-approval")
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
		approval, 201).RetentionPreview
	execution := object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Archive exact approved V23 history bytes through V24.",
		"idempotencyKey": "v24-history-execution",
	}
	queued := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		execution, 202).RetentionRun
	return approved, queued, execution
}

func openV24RetentionWorker(t *testing.T, h *harness, raw, archive StorageConfig,
	maxConnections int32) RetentionWorker {
	t.Helper()
	requireRetentionWorker(t)
	worker, err := Production.OpenRetentionWorker(h.services.ctx, RetentionWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: h.services.cfg.ApplicationName + "-retention", MaxConnections: maxConnections,
		RawStorage: raw, ArchiveStorage: archive, Now: h.services.cfg.Now,
		LogOutput: h.services.cfg.LogOutput, Lease: 2 * time.Second,
	})
	ok(t, "open V24 retention worker", err)
	if worker.ProcessNext == nil || worker.Close == nil {
		t.Fatal("V24 retention worker binding omitted processing or close")
	}
	return worker
}

func v24HistoryTable(kind string) string {
	switch kind {
	case "finding-decision-event":
		return "finding_decision_events"
	case "notification-policy-revision":
		return "notification_policy_revisions"
	case "finding-change-event":
		return "finding_change_events"
	case "notification-policy-event":
		return "notification_policy_events"
	default:
		panic("undeclared V24 history kind")
	}
}

type v24HistoryMetadata struct {
	availability, key, digest string
	size, revision            int64
	archivedAt                *time.Time
}

func v24ReadHistoryMetadata(t *testing.T, h *harness, resource v24HistoryResource) v24HistoryMetadata {
	t.Helper()
	var value v24HistoryMetadata
	ok(t, "read V24 history archive metadata", h.services.db.QueryRow(h.services.ctx, `SELECT
		detail_availability,detail_revision,COALESCE(archive_key,''),
		COALESCE(archive_digest,''),COALESCE(archive_size,0),archived_at
		FROM `+notificationTable(h, v24HistoryTable(resource.kind))+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, resource.id).Scan(
		&value.availability, &value.revision, &value.key, &value.digest, &value.size, &value.archivedAt,
	))
	return value
}

func v24CanonicalHistoryBytes(t *testing.T, h *harness, resource v24HistoryResource) []byte {
	t.Helper()
	switch resource.kind {
	case "finding-decision-event":
		return v23DecisionArchiveBytes(t, h, resource.id)
	case "notification-policy-revision":
		return v23PolicyRevisionArchiveBytes(t, h, resource.id)
	case "finding-change-event":
		return v23FindingChangeArchiveBytes(t, h, resource.id)
	case "notification-policy-event":
		return v23PolicyEventArchiveBytes(t, h, resource.id)
	default:
		t.Fatal("undeclared V24 history kind")
		return nil
	}
}

func v24ArchiveCount(t *testing.T, h *harness, resource v24HistoryResource) int {
	t.Helper()
	prefix := h.services.cfg.ArchiveStorage.Prefix + h.admin.workspace + "/" +
		resource.directory + "/" + resource.id + "/"
	page, err := h.services.s3.ListObjectsV2(h.services.ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(2),
	})
	ok(t, "list bounded V24 history archive objects", err)
	if aws.ToBool(page.IsTruncated) {
		t.Fatal("V24 history archive listing exceeded its bounded identity")
	}
	return len(page.Contents)
}

type v24ArchiveProxy struct {
	server   *httptest.Server
	started  chan struct{}
	release  chan struct{}
	method   string
	fragment string
	once     sync.Once
	mu       sync.Mutex
	calls    []string
}

func newV24ArchiveProxy(t *testing.T, endpoint, method, fragment string) *v24ArchiveProxy {
	t.Helper()
	target, err := url.Parse(endpoint)
	ok(t, "parse V24 archive proxy endpoint", err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.WriteHeader(http.StatusBadGateway)
	}
	value := &v24ArchiveProxy{
		started: make(chan struct{}), release: make(chan struct{}),
		method: method, fragment: fragment,
	}
	value.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value.mu.Lock()
		value.calls = append(value.calls, r.Method+" "+r.URL.Path)
		value.mu.Unlock()
		if r.Method == value.method && strings.Contains(r.URL.Path, value.fragment) {
			value.once.Do(func() { close(value.started) })
			select {
			case <-value.release:
			case <-r.Context().Done():
				http.Error(w, "cancelled V24 archive request", http.StatusGatewayTimeout)
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		select {
		case <-value.release:
		default:
			close(value.release)
		}
		value.server.CloseClientConnections()
		value.server.Close()
		transport.CloseIdleConnections()
	})
	return value
}

func (p *v24ArchiveProxy) unblock() {
	select {
	case <-p.release:
	default:
		close(p.release)
	}
}

func (p *v24ArchiveProxy) count(method, fragment string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, call := range p.calls {
		if strings.HasPrefix(call, method+" ") && strings.Contains(call, fragment) {
			total++
		}
	}
	return total
}

func TestM08_V24HistoryArchiveExecutionPublishesExactBytesWithoutCompaction(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	provider := &providerTripwire{}
	fixture := seedV23HistoryRetention(t, h, provider)
	h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "finding-decision-event", "resourceId": fixture.decisionIDs[1],
		"reason": "Keep one V24 decision history item protected.",
	}, 201)
	eligible := v24EligibleHistory(fixture)
	var decisionFindingID string
	ok(t, "read V24 decision API compatibility identity", h.services.db.QueryRow(h.services.ctx,
		`SELECT finding_id FROM `+notificationTable(h, "finding_decision_events")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.decisionIDs[0]).Scan(&decisionFindingID))
	beforeFinding := append([]byte(nil), h.request(h.admin, "GET",
		"/api/v1/findings/"+decisionFindingID, nil, 200).Body.Bytes()...)
	beforePolicy := append([]byte(nil), h.request(h.admin, "GET",
		notificationPoliciesPath+"/"+fixture.policy.ID, nil, 200).Body.Bytes()...)
	beforeEvents := append([]byte(nil), h.request(h.admin, "GET",
		notificationPoliciesPath+"/"+fixture.policy.ID+"/events?limit=100", nil, 200).Body.Bytes()...)

	approved, queued, execution := v24QueueHistory(t, h)
	if queued.Operation != "apply-preview" || queued.PreviewID == nil || *queued.PreviewID != approved.ID ||
		queued.Total != len(fixture.payloads) || queued.State != "queued" {
		t.Fatalf("V24 did not create one ordinary bounded retention run: %+v", queued)
	}
	replayed := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		execution, 200).RetentionRun
	equal(t, "V24 execution queue exact replay", replayed.ID, queued.ID)
	changed := object{}
	for key, value := range execution {
		changed[key] = value
	}
	changed["rationale"] = "Changed V24 replay must conflict."
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		changed, 409, "conflict")

	objectStore.arm()
	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close V24 history archive worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	if completed.State != "succeeded" || completed.Total != 10 || completed.Succeeded != 4 ||
		completed.Protected != 6 || completed.Missing != 0 || completed.Corrupt != 0 ||
		completed.Failed != 0 || completed.CompletedAt == nil {
		t.Fatalf("V24 history execution truth is incomplete: %+v", completed)
	}

	for _, resource := range eligible {
		item := findRetentionRunItem(t, completed, resource.kind, resource.id, "archive-audit")
		if item.State != "succeeded" || item.Outcome != "archived" {
			t.Fatalf("V24 %s did not settle archived success: %+v", resource.kind, item)
		}
		key, data := retentionArchiveObject(t, h, resource.directory, resource.id)
		wantKey := h.services.cfg.ArchiveStorage.Prefix + h.admin.workspace + "/" +
			resource.directory + "/" + resource.id + "/" +
			strings.TrimPrefix(v23ArchiveDigest(resource.payload), "sha256:")
		if key != wantKey || !bytes.Equal(data, resource.payload) {
			t.Fatalf("V24 %s object identity or exact V23 bytes changed", resource.kind)
		}
		metadata := v24ReadHistoryMetadata(t, h, resource)
		if metadata.availability != "archived" || metadata.revision != 2 ||
			metadata.key != wantKey || metadata.digest != v23ArchiveDigest(resource.payload) ||
			metadata.size != int64(len(resource.payload)) || metadata.archivedAt == nil {
			t.Fatalf("V24 %s durable archive metadata is incomplete: %+v", resource.kind, metadata)
		}
		if got := v24CanonicalHistoryBytes(t, h, resource); !bytes.Equal(got, resource.payload) {
			t.Fatalf("V24 %s compacted or rewrote an original event payload", resource.kind)
		}
		var state, kind, id, digest string
		var size int64
		ok(t, "read V24 referenced publication", h.services.db.QueryRow(h.services.ctx, `SELECT
			state,resource_kind,resource_id,object_digest,object_size
			FROM `+notificationTable(h, "archive_publications")+`
			WHERE workspace_id=$1 AND object_key=$2`, h.admin.workspace, wantKey).Scan(
			&state, &kind, &id, &digest, &size,
		))
		if state != "referenced" || kind != resource.kind || id != resource.id ||
			digest != metadata.digest || size != metadata.size {
			t.Fatalf("V24 %s publication ledger lost singular identity or reference authority", resource.kind)
		}
	}

	protected := map[string][]string{
		fixture.decisionIDs[1]:          {"legal-hold"},
		fixture.revisionIDs[1]:          {"pending-policy-evaluation"},
		fixture.revisionIDs[3]:          {"current-policy-revision", "pending-policy-evaluation"},
		fixture.pendingEpochOneChangeID: {"pending-policy-evaluation"},
		fixture.pendingEpochTwoChangeID: {"pending-policy-evaluation"},
		fixture.activePolicyEventID:     {"active-delivery"},
	}
	for id, reasons := range protected {
		kind := fixture.kinds[id]
		item := findRetentionRunItem(t, completed, kind, id, "archive-audit")
		if item.State != "protected" || item.Outcome != "protected" ||
			!reflectReasons(item.ProtectedReasons, reasons) {
			t.Fatalf("V24 protected %s %s truth changed: %+v", kind, id, item)
		}
		var referenced int
		ok(t, "count protected V24 referenced publications", h.services.db.QueryRow(h.services.ctx, `SELECT count(*)
			FROM `+notificationTable(h, "archive_publications")+`
			WHERE workspace_id=$1 AND resource_kind=$2 AND resource_id=$3 AND state='referenced'`,
			h.admin.workspace, kind, id).Scan(&referenced))
		if referenced != 0 {
			t.Fatalf("V24 protected %s %s created a referenced archive", kind, id)
		}
	}

	fresh := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	for _, resource := range eligible {
		for _, item := range fresh.Items {
			if item.ResourceKind == resource.kind && item.ResourceID == resource.id {
				t.Fatalf("V24 later preview selected already archived %s %s", resource.kind, resource.id)
			}
		}
	}
	equal(t, "V24 later preview retains only protected history candidates", len(fresh.Items), 6)
	if !bytes.Equal(beforeFinding, h.request(h.admin, "GET",
		"/api/v1/findings/"+decisionFindingID, nil, 200).Body.Bytes()) ||
		!bytes.Equal(beforePolicy, h.request(h.admin, "GET",
			notificationPoliciesPath+"/"+fixture.policy.ID, nil, 200).Body.Bytes()) ||
		!bytes.Equal(beforeEvents, h.request(h.admin, "GET",
			notificationPoliciesPath+"/"+fixture.policy.ID+"/events?limit=100", nil, 200).Body.Bytes()) {
		t.Fatal("V24 changed existing decision or notification API bytes")
	}
	if provider.calls.Load() != 0 {
		t.Fatal("V24 history archive execution performed provider I/O")
	}
	if objectStore.calls.Load() < int64(len(eligible)*3) {
		t.Fatal("V24 history archive execution omitted exact PUT, HEAD or read verification")
	}
}

func reflectReasons(got, want []string) bool {
	if want == nil {
		want = []string{}
	}
	return slices.Equal(got, want)
}

func TestM08_V24HistoryArchiveRechecksAuthorityAfterHeldObjectIO(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*testing.T, *harness, v24HistoryResource)
		reason []string
	}{
		{
			name: "legal-hold",
			mutate: func(t *testing.T, h *harness, resource v24HistoryResource) {
				h.json(h.admin, "POST", "/api/v1/retention/holds", object{
					"resourceKind": resource.kind, "resourceId": resource.id,
					"reason": "Protect V24 history while its immutable object write is held.",
				}, 201)
			},
			reason: []string{"legal-hold"},
		},
		{
			name: "payload-state-changed",
			mutate: func(t *testing.T, h *harness, resource v24HistoryResource) {
				_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
					notificationTable(h, "finding_change_events")+`
					SET title=title||' changed during V24 object I/O'
					WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, resource.id)
				ok(t, "change V24 history payload during held object I/O", err)
			},
			reason: []string{"state-changed"},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, _ := newV23HistoryHarness(t)
			fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
			target := v24EligibleHistory(fixture)[2]
			_, queued, _ := v24QueueHistory(t, h)
			itemsTable := notificationTable(h, "retention_run_items")
			result, err := h.services.db.Exec(h.services.ctx, `UPDATE `+itemsTable+`
				SET state='protected',outcome='fixture-precompleted',
					protected_reasons='[]'::jsonb,started_at=clock_timestamp(),
					completed_at=clock_timestamp()
				WHERE workspace_id=$1 AND run_id=$2 AND NOT
					(resource_kind=$3 AND resource_id=$4 AND action='archive-audit')`,
				h.admin.workspace, queued.ID, target.kind, target.id)
			ok(t, "precomplete unrelated V24 history items", err)
			if result.RowsAffected() != int64(queued.Total-1) {
				t.Fatalf("V24 held-I/O fixture precompleted %d items, want %d",
					result.RowsAffected(), queued.Total-1)
			}

			archive := h.services.cfg.ArchiveStorage
			fragment := "/" + target.directory + "/" + target.id + "/"
			proxy := newV24ArchiveProxy(t, archive.Endpoint, http.MethodPut, fragment)
			raw := h.services.cfg.Storage
			raw.Endpoint = proxy.server.URL
			archive.Endpoint = proxy.server.URL
			worker := openV24RetentionWorker(t, h, raw, archive, 1)
			probe := openV24RetentionWorker(t, h, raw, archive, 1)
			type workerResult struct {
				worked bool
				err    error
			}
			done := make(chan workerResult, 1)
			go func() {
				worked, processErr := worker.ProcessNext(h.services.ctx)
				done <- workerResult{worked: worked, err: processErr}
			}()
			select {
			case <-proxy.started:
			case <-time.After(20 * time.Second):
				t.Fatal("V24 worker did not reach the held history PUT")
			}

			var publicationState, publicationKind string
			ok(t, "read committed V24 publication intent before PUT", h.services.db.QueryRow(h.services.ctx, `SELECT
				state,resource_kind FROM `+notificationTable(h, "archive_publications")+`
				WHERE workspace_id=$1 AND resource_id=$2`, h.admin.workspace, target.id).Scan(
				&publicationState, &publicationKind,
			))
			if publicationState != "publishing" || publicationKind != target.kind {
				t.Fatal("V24 did not commit singular publication intent before object I/O")
			}
			var openTransactions int
			ok(t, "inspect V24 worker transaction state during held S3 PUT", h.services.db.QueryRow(h.services.ctx, `SELECT count(*)
				FROM pg_stat_activity WHERE application_name=$1 AND xact_start IS NOT NULL`,
				h.services.cfg.ApplicationName+"-retention").Scan(&openTransactions))
			if openTransactions != 0 {
				t.Fatal("V24 worker retained a SQL transaction during held S3 PUT")
			}
			time.Sleep(3 * time.Second)
			probeContext, cancelProbe := context.WithTimeout(h.services.ctx, 2*time.Second)
			probeWorked, probeErr := probe.ProcessNext(probeContext)
			cancelProbe()
			ok(t, "probe competing V24 worker during held S3 PUT", probeErr)
			if probeWorked {
				t.Fatal("V24 held S3 PUT monopolized SQL and lost its renewed run lease")
			}

			scenario.mutate(t, h, target)
			proxy.unblock()
			select {
			case value := <-done:
				if !value.worked || value.err != nil {
					t.Fatalf("V24 held-I/O worker result=%+v", value)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("V24 held-I/O worker did not settle")
			}
			ok(t, "close V24 held-I/O worker", worker.Close())
			ok(t, "close V24 held-I/O probe worker", probe.Close())
			completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
			item := findRetentionRunItem(t, completed, target.kind, target.id, "archive-audit")
			if item.State != "protected" || !slices.Equal(item.ProtectedReasons, scenario.reason) {
				t.Fatalf("V24 held-I/O authority change was not settled truthfully: %+v", item)
			}
			metadata := v24ReadHistoryMetadata(t, h, target)
			if metadata.availability != "available" || metadata.revision != 1 ||
				metadata.key != "" || metadata.digest != "" || metadata.size != 0 ||
				metadata.archivedAt != nil {
				t.Fatalf("V24 attached an object after authority changed: %+v", metadata)
			}
			var state string
			ok(t, "read V24 orphan publication after authority change", h.services.db.QueryRow(h.services.ctx, `SELECT
				state FROM `+notificationTable(h, "archive_publications")+`
				WHERE workspace_id=$1 AND resource_id=$2`, h.admin.workspace, target.id).Scan(&state))
			if state != "orphan" || v24ArchiveCount(t, h, target) != 1 ||
				proxy.count(http.MethodPut, fragment) != 1 {
				t.Fatal("V24 did not retain exactly one orphan-ledgered object for reconciliation")
			}
		})
	}
}

func TestM08_V24HistoryArchiveReplayDoesNotPutTwiceAndReferencesAreNotOrphans(t *testing.T) {
	h, _ := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	target := v24EligibleHistory(fixture)[2]
	_, queued, _ := v24QueueHistory(t, h)
	itemsTable := notificationTable(h, "retention_run_items")
	result, err := h.services.db.Exec(h.services.ctx, `UPDATE `+itemsTable+`
		SET state='protected',outcome='fixture-precompleted',protected_reasons='[]'::jsonb,
			started_at=clock_timestamp(),completed_at=clock_timestamp()
		WHERE workspace_id=$1 AND run_id=$2 AND NOT
			(resource_kind=$3 AND resource_id=$4 AND action='archive-audit')`,
		h.admin.workspace, queued.ID, target.kind, target.id)
	ok(t, "precomplete unrelated V24 replay items", err)
	if result.RowsAffected() != int64(queued.Total-1) {
		t.Fatal("V24 replay fixture did not isolate one history item")
	}

	archive := h.services.cfg.ArchiveStorage
	fragment := "/" + target.directory + "/" + target.id + "/"
	proxy := newV24ArchiveProxy(t, archive.Endpoint, "", "")
	raw := h.services.cfg.Storage
	raw.Endpoint = proxy.server.URL
	archive.Endpoint = proxy.server.URL
	first := openRetentionWorkerWithStorage(t, h, raw, archive)
	drainRetention(t, h, first)
	ok(t, "close first V24 replay worker", first.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	equal(t, "first V24 archive settled", findRetentionRunItem(
		t, completed, target.kind, target.id, "archive-audit").Outcome, "archived")
	metadata := v24ReadHistoryMetadata(t, h, target)
	if proxy.count(http.MethodPut, fragment) != 1 || metadata.revision != 2 {
		t.Fatal("first V24 archive did not perform exactly one PUT and one metadata transition")
	}

	runsTable := notificationTable(h, "retention_runs")
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+runsTable+`
		SET state='queued',completed_at=NULL,worker_id=NULL,lease_until=NULL,
			available_at=clock_timestamp(),attempts=0
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, queued.ID)
	ok(t, "stage V24 durable archived replay run", err)
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+itemsTable+`
		SET state='queued',outcome='',protected_reasons='[]'::jsonb,attempts=0,
			started_at=NULL,completed_at=NULL
		WHERE workspace_id=$1 AND run_id=$2 AND resource_kind=$3 AND resource_id=$4`,
		h.admin.workspace, queued.ID, target.kind, target.id)
	ok(t, "stage V24 durable archived replay item", err)
	second := openRetentionWorkerWithStorage(t, h, raw, archive)
	drainRetention(t, h, second)
	ok(t, "close second V24 replay worker", second.Close())
	replayed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	item := findRetentionRunItem(t, replayed, target.kind, target.id, "archive-audit")
	if item.State != "succeeded" || item.Outcome != "already-archived" ||
		proxy.count(http.MethodPut, fragment) != 1 ||
		v24ReadHistoryMetadata(t, h, target).revision != metadata.revision {
		t.Fatalf("V24 archived replay rewrote its immutable object or metadata: %+v", item)
	}

	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "archive_publications")+`
		SET state='orphan',referenced_at=NULL,updated_at=$3,revision=revision+1
		WHERE workspace_id=$1 AND object_key=$2`, h.admin.workspace, metadata.key,
		h.services.cfg.Now().Add(-25*time.Hour))
	ok(t, "stage old V24 referenced publication as orphan candidate", err)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	referenced := false
	for _, candidate := range preview.Items {
		if candidate.ResourceKind == "archive-object" && candidate.ObjectKey != nil &&
			*candidate.ObjectKey == metadata.key {
			referenced = true
			if !slices.Equal(candidate.ProtectedReasons, []string{"archive-reference"}) {
				t.Fatal("V24 orphan preview did not protect the live history archive_key reference")
			}
		}
	}
	if !referenced {
		t.Fatal("V24 orphan preview omitted the protected live history archive_key reference")
	}
}

func TestM08_V24HistoryArchiveResumesVerifiedCrashPublicationWithoutSecondPut(t *testing.T) {
	h, _ := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	target := v24EligibleHistory(fixture)[0]
	_, queued, _ := v24QueueHistory(t, h)
	itemsTable := notificationTable(h, "retention_run_items")
	result, err := h.services.db.Exec(h.services.ctx, `UPDATE `+itemsTable+`
			SET state='protected',outcome='fixture-precompleted',protected_reasons='[]'::jsonb,
				started_at=clock_timestamp(),completed_at=clock_timestamp()
			WHERE workspace_id=$1 AND run_id=$2 AND NOT
				(resource_kind=$3 AND resource_id=$4 AND action='archive-audit')`,
		h.admin.workspace, queued.ID, target.kind, target.id)
	ok(t, "precomplete unrelated V24 crash-recovery items", err)
	if result.RowsAffected() != int64(queued.Total-1) {
		t.Fatal("V24 crash-recovery fixture did not isolate one history item")
	}

	digest := v23ArchiveDigest(target.payload)
	key := h.services.cfg.ArchiveStorage.Prefix + h.admin.workspace + "/" +
		target.directory + "/" + target.id + "/" + strings.TrimPrefix(digest, "sha256:")
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(target.payload), ContentLength: aws.Int64(int64(len(target.payload))),
		ContentType: aws.String("application/json"),
		Metadata: map[string]string{
			"sha256": strings.TrimPrefix(digest, "sha256:"),
		},
	})
	ok(t, "stage exact verified V24 crash publication object", err)
	now := h.services.cfg.Now()
	_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+
		notificationTable(h, "archive_publications")+`
			(id,workspace_id,object_key,resource_kind,resource_id,object_digest,object_size,
			 state,revision,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,'publishing',1,$8,$8)`,
		v23ID(900), h.admin.workspace, key, target.kind, target.id, digest, len(target.payload), now)
	ok(t, "stage committed V24 publication intent after simulated crash", err)

	archive := h.services.cfg.ArchiveStorage
	fragment := "/" + target.directory + "/" + target.id + "/"
	proxy := newV24ArchiveProxy(t, archive.Endpoint, "", "")
	raw := h.services.cfg.Storage
	raw.Endpoint = proxy.server.URL
	archive.Endpoint = proxy.server.URL
	worker := openRetentionWorkerWithStorage(t, h, raw, archive)
	drainRetention(t, h, worker)
	ok(t, "close V24 crash-recovery worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	item := findRetentionRunItem(t, completed, target.kind, target.id, "archive-audit")
	metadata := v24ReadHistoryMetadata(t, h, target)
	if item.State != "succeeded" || item.Outcome != "archived" ||
		proxy.count(http.MethodPut, fragment) != 0 ||
		metadata.availability != "archived" || metadata.key != key ||
		metadata.digest != digest || metadata.size != int64(len(target.payload)) {
		t.Fatalf("V24 did not resume the verified crash publication without a second PUT: item=%+v metadata=%+v",
			item, metadata)
	}
	var state string
	ok(t, "read resumed V24 crash publication state", h.services.db.QueryRow(h.services.ctx, `SELECT state
			FROM `+notificationTable(h, "archive_publications")+`
			WHERE workspace_id=$1 AND object_key=$2`, h.admin.workspace, key).Scan(&state))
	if state != "referenced" {
		t.Fatal("V24 verified crash publication did not become referenced")
	}
}
