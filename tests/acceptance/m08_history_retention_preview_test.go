//go:build integration

package acceptance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type v23ObjectStoreTripwire struct {
	server *httptest.Server
	armed  atomic.Bool
	calls  atomic.Int64
}

func newV23ObjectStoreTripwire(t *testing.T, services *services) *v23ObjectStoreTripwire {
	t.Helper()
	target, err := url.Parse(services.cfg.Storage.Endpoint)
	ok(t, "parse V23 object-store fixture endpoint", err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
	tripwire := &v23ObjectStoreTripwire{}
	bucketPath := "/" + services.cfg.Storage.Bucket
	rawPrefix := bucketPath + "/" + services.cfg.Storage.Prefix
	archivePrefix := bucketPath + "/" + services.cfg.ArchiveStorage.Prefix
	tripwire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := r.URL.Path == bucketPath ||
			strings.HasPrefix(r.URL.Path, rawPrefix) ||
			strings.HasPrefix(r.URL.Path, archivePrefix)
		if !allowed {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if tripwire.armed.Load() {
			tripwire.calls.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		tripwire.server.CloseClientConnections()
		tripwire.server.Close()
		transport.CloseIdleConnections()
	})
	services.cfg.Storage.Endpoint = tripwire.server.URL
	services.cfg.ArchiveStorage.Endpoint = tripwire.server.URL
	return tripwire
}

func (t *v23ObjectStoreTripwire) arm() {
	t.calls.Store(0)
	t.armed.Store(true)
}

func newV23HistoryHarness(t *testing.T) (*harness, *v23ObjectStoreTripwire) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.BootstrapToken = secret(t)
	key := sha256.Sum256([]byte(secret(t)))
	h.services.cfg.IntegrationEncryptionKey = append([]byte(nil), key[:]...)
	h.services.cfg.PublicOrigin = "https://aspm.test"
	h.services.cfg.LogOutput = io.Discard
	tripwire := newV23ObjectStoreTripwire(t, h.services)
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close V23 history-retention application", h.app.Close())
		}
	})
	h.enroll()
	return h, tripwire
}

type v23HistoryFixture struct {
	policy                  notificationPolicy
	revisionIDs             map[int64]string
	decisionIDs             []string
	evaluatedChangeID       string
	pendingEpochOneChangeID string
	pendingEpochTwoChangeID string
	activePolicyEventID     string
	noDeliveryPolicyEventID string
	activeDeliveryID        string
	payloads                map[string][]byte
	kinds                   map[string]string
	observed                map[string]time.Time
	holds                   map[string]retentionHold
}

func v23ID(value int) string { return fmt.Sprintf("%032x", 0x230000+value) }

func v23TwoFindingReport(t *testing.T, firstMessage string) []byte {
	t.Helper()
	return sarif(t, func(run, result object) {
		var second object
		ok(t, "clone second V23 history finding", json.Unmarshal(encode(t, result), &second))
		second["guid"] = "23232323-2323-4323-8323-232323232323"
		second["message"] = object{"text": "Stable second V23 history finding."}
		result["message"] = object{"text": firstMessage}
		run["results"] = append(run["results"].([]any), second)
	})
}

func v23DecisionArchiveBytes(t *testing.T, h *harness, id string) []byte {
	t.Helper()
	var payload v23FindingDecisionArchivePayload
	var changed, before, after []byte
	var created time.Time
	ok(t, "read finding-decision archive payload", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,finding_id,decision_revision,actor_id,action,rationale,
		changed_fields,before_state,after_state,created_at
		FROM `+notificationTable(h, "finding_decision_events")+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, id).Scan(
		&payload.ID, &payload.WorkspaceID, &payload.FindingID, &payload.DecisionRevision,
		&payload.ActorID, &payload.Action, &payload.Rationale, &changed, &before, &after, &created,
	))
	payload.SchemaVersion, payload.ResourceKind = 1, "finding-decision-event"
	ok(t, "decode finding-decision changed fields", json.Unmarshal(changed, &payload.ChangedFields))
	payload.BeforeState = v23CanonicalJSON(t, before)
	payload.AfterState = v23CanonicalJSON(t, after)
	payload.CreatedAt = v23ArchiveTime(created)
	return v23ArchiveBytes(t, payload)
}

func v23PolicyRevisionArchiveBytes(t *testing.T, h *harness, id string) []byte {
	t.Helper()
	var payload v23NotificationPolicyRevisionArchivePayload
	var created time.Time
	ok(t, "read notification-policy revision archive payload", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,policy_id,name,connection_id,connection_profile,connection_revision,
		enabled,change_kinds,minimum_severity,revision,epoch,actor_id,actor_name,rationale,created_at
		FROM `+notificationTable(h, "notification_policy_revisions")+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, id).Scan(
		&payload.ID, &payload.WorkspaceID, &payload.PolicyID, &payload.Name, &payload.ConnectionID,
		&payload.ConnectionProfile, &payload.ConnectionRevision, &payload.Enabled, &payload.ChangeKinds,
		&payload.MinimumSeverity, &payload.Revision, &payload.Epoch, &payload.ActorID, &payload.ActorName,
		&payload.Rationale, &created,
	))
	payload.SchemaVersion, payload.ResourceKind = 1, "notification-policy-revision"
	payload.CreatedAt = v23ArchiveTime(created)
	return v23ArchiveBytes(t, payload)
}

func v23FindingChangeArchiveBytes(t *testing.T, h *harness, id string) []byte {
	t.Helper()
	var payload v23FindingChangeArchivePayload
	var changeAt time.Time
	var lease *time.Time
	ok(t, "read finding-change archive payload", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,finding_id,policy_epoch,title,severity,asset_name,change_kind,
		change_revision,change_at,state,worker_id,fence,lease_until
		FROM `+notificationTable(h, "finding_change_events")+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, id).Scan(
		&payload.ID, &payload.WorkspaceID, &payload.FindingID, &payload.PolicyEpoch, &payload.Title,
		&payload.Severity, &payload.AssetName, &payload.ChangeKind, &payload.ChangeRevision,
		&changeAt, &payload.State, &payload.WorkerID, &payload.Fence, &lease,
	))
	payload.SchemaVersion, payload.ResourceKind = 1, "finding-change-event"
	payload.ChangeAt = v23ArchiveTime(changeAt)
	if lease != nil {
		value := v23ArchiveTime(*lease)
		payload.LeaseUntil = &value
	}
	return v23ArchiveBytes(t, payload)
}

func v23PolicyEventArchiveBytes(t *testing.T, h *harness, id string) []byte {
	t.Helper()
	var payload v23NotificationPolicyEventArchivePayload
	var created time.Time
	ok(t, "read notification-policy event archive payload", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,policy_id,policy_revision,finding_id,finding_change_revision,
		outcome,delivery_id,created_at
		FROM `+notificationTable(h, "notification_policy_events")+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, id).Scan(
		&payload.ID, &payload.WorkspaceID, &payload.PolicyID, &payload.PolicyRevision,
		&payload.FindingID, &payload.FindingChangeRevision, &payload.Outcome,
		&payload.DeliveryID, &created,
	))
	payload.SchemaVersion, payload.ResourceKind = 1, "notification-policy-event"
	payload.CreatedAt = v23ArchiveTime(created)
	return v23ArchiveBytes(t, payload)
}

func seedV23HistoryRetention(t *testing.T, h *harness, provider *providerTripwire) v23HistoryFixture {
	t.Helper()
	connection := h.createSlackConnection("V23 history destination", "C23001", true)
	firstPolicy := h.createPolicy(policyInput("V23 history policy", connection.ID, true,
		[]string{"new", "changed", "reopened"}, "info", "Approve V23 history fixture notifications."))
	asset := h.asset(h.admin, "V23 history repository", &h.admin.user.ID)
	firstInput := h.input(asset.ID, "sarif", v23TwoFindingReport(t, "Initial V23 history finding."))
	firstInput["scanId"] = "v23-history-initial"
	h.finish(h.upload(firstInput).ID, "succeeded")
	work := h.work(h.admin, "")
	equal(t, "V23 history fixture finding count", len(work), 2)
	worker := h.openPolicyWorker(t, provider)
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "evaluate one V23 history change event", err)
	if !processed || provider.calls.Load() != 0 {
		t.Fatal("V23 history setup must evaluate locally without provider I/O")
	}
	h.clock.Add(int64(time.Second))
	h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+firstPolicy.ID, object{
		"name": "V23 intermediate history policy", "rationale": "Create an unreferenced older V23 policy revision.",
	}, 200)
	h.clock.Add(int64(time.Second))
	currentPolicy := h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+firstPolicy.ID, object{
		"name": "V23 current history policy", "rationale": "Create the current V23 policy revision.",
	}, 200).Policy
	secondInput := h.input(asset.ID, "sarif", v23TwoFindingReport(t, "Changed V23 history finding."))
	secondInput["scanId"] = "v23-history-changed"
	secondInput["sourceScanAt"] = sourceTime.Add(24 * time.Hour)
	secondInput["collectedAt"] = sourceTime.Add(25 * time.Hour)
	h.finish(h.upload(secondInput).ID, "succeeded")
	for index, item := range h.work(h.admin, "") {
		rationale := fmt.Sprintf("Create V23 decision history %d.", index+1)
		if index == 0 {
			rationale += " Preserve café bytes."
		}
		h.json(h.admin, "PATCH", "/api/v1/findings/"+item.ID, object{
			"workflowState": "pending-retest",
			"rationale":     rationale,
		}, 200)
	}

	changes := h.changeEvents()
	if len(changes) != 3 {
		t.Fatalf("V23 history fixture change events=%d, want 3", len(changes))
	}
	var evaluated findingChangeEvent
	pending := map[int64]findingChangeEvent{}
	for _, event := range changes {
		if event.State == "evaluated" {
			evaluated = event
		} else if event.State == "pending" {
			pending[event.PolicyEpoch] = event
		}
	}
	if evaluated.ID == "" || pending[firstPolicy.Epoch].ID == "" || pending[currentPolicy.Epoch].ID == "" {
		t.Fatal("V23 history fixture did not establish evaluated and epoch-scoped pending events")
	}
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_change_events")+`
		SET state='processing',worker_id='v23-history-worker',fence=1,lease_until=$3
		WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, pending[firstPolicy.Epoch].ID, h.services.cfg.Now().Add(time.Hour))
	ok(t, "establish V23 processing change-event authority", err)
	activeEvents := h.policyEvents(h.admin, firstPolicy.ID)
	if len(activeEvents) != 1 || activeEvents[0].DeliveryID == nil || activeEvents[0].Outcome != "queued" {
		t.Fatal("V23 history fixture did not establish one active linked policy event")
	}
	noDeliveryID := v23ID(1)
	_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+notificationTable(h, "notification_policy_events")+`
		(id,workspace_id,policy_id,policy_revision,finding_id,finding_change_revision,outcome,delivery_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,'connection-stale',NULL,$7)`,
		noDeliveryID, h.admin.workspace, firstPolicy.ID, currentPolicy.Revision,
		work[0].ID, int64(23001), h.services.cfg.Now())
	ok(t, "insert no-delivery V23 policy event fixture", err)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rows, err := h.services.db.Query(h.services.ctx, `SELECT id,revision FROM `+
		notificationTable(h, "notification_policy_revisions")+`
		WHERE workspace_id=$1 AND policy_id=$2 ORDER BY revision`, h.admin.workspace, firstPolicy.ID)
	ok(t, "read V23 policy revision identities", err)
	revisionIDs := map[int64]string{}
	for rows.Next() {
		var id string
		var revision int64
		ok(t, "scan V23 policy revision identity", rows.Scan(&id, &revision))
		revisionIDs[revision] = id
	}
	ok(t, "finish V23 policy revision identities", rows.Err())
	rows.Close()
	if len(revisionIDs) != 3 {
		t.Fatal("V23 history fixture did not preserve three policy revisions")
	}
	for revision, id := range revisionIDs {
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+
			notificationTable(h, "notification_policy_revisions")+` SET created_at=$3
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, id, base.Add(time.Duration(revision)*time.Hour))
		ok(t, "age V23 policy revision", err)
	}
	changeTimes := map[string]time.Time{
		evaluated.ID:                    base.Add(3 * time.Hour),
		pending[firstPolicy.Epoch].ID:   base.Add(4 * time.Hour),
		pending[currentPolicy.Epoch].ID: base.Add(5 * time.Hour),
	}
	for id, at := range changeTimes {
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_change_events")+`
			SET change_at=$3 WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, id, at)
		ok(t, "age V23 finding-change event", err)
	}
	policyEventTimes := map[string]time.Time{
		activeEvents[0].ID: base.Add(6 * time.Hour),
		noDeliveryID:       base.Add(7 * time.Hour),
	}
	for id, at := range policyEventTimes {
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "notification_policy_events")+`
			SET created_at=$3 WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, id, at)
		ok(t, "age V23 notification-policy event", err)
	}
	rows, err = h.services.db.Query(h.services.ctx, `SELECT id FROM `+
		notificationTable(h, "finding_decision_events")+`
		WHERE workspace_id=$1 ORDER BY finding_id,decision_revision`, h.admin.workspace)
	ok(t, "read V23 decision event identities", err)
	var decisionIDs []string
	for rows.Next() {
		var id string
		ok(t, "scan V23 decision event identity", rows.Scan(&id))
		decisionIDs = append(decisionIDs, id)
	}
	ok(t, "finish V23 decision event identities", rows.Err())
	rows.Close()
	if len(decisionIDs) != 2 {
		t.Fatal("V23 history fixture did not establish two decision events")
	}
	for index, id := range decisionIDs {
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_decision_events")+`
			SET created_at=$3 WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, id, base.Add(time.Duration(8+index)*time.Hour))
		ok(t, "age V23 finding-decision event", err)
	}

	fixture := v23HistoryFixture{
		policy: currentPolicy, revisionIDs: revisionIDs, decisionIDs: decisionIDs,
		evaluatedChangeID: evaluated.ID, pendingEpochOneChangeID: pending[firstPolicy.Epoch].ID,
		pendingEpochTwoChangeID: pending[currentPolicy.Epoch].ID,
		activePolicyEventID:     activeEvents[0].ID, noDeliveryPolicyEventID: noDeliveryID,
		activeDeliveryID: *activeEvents[0].DeliveryID,
		payloads:         map[string][]byte{}, kinds: map[string]string{}, observed: map[string]time.Time{},
		holds: map[string]retentionHold{},
	}
	for _, id := range decisionIDs {
		fixture.payloads[id] = v23DecisionArchiveBytes(t, h, id)
		fixture.kinds[id] = "finding-decision-event"
		var at time.Time
		ok(t, "read V23 decision observed time", h.services.db.QueryRow(h.services.ctx, `SELECT created_at FROM `+
			notificationTable(h, "finding_decision_events")+` WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, id).Scan(&at))
		fixture.observed[id] = at.UTC()
	}
	for _, id := range []string{revisionIDs[1], revisionIDs[2], revisionIDs[3]} {
		fixture.payloads[id] = v23PolicyRevisionArchiveBytes(t, h, id)
		fixture.kinds[id] = "notification-policy-revision"
		var at time.Time
		ok(t, "read V23 policy revision observed time", h.services.db.QueryRow(h.services.ctx, `SELECT created_at FROM `+
			notificationTable(h, "notification_policy_revisions")+` WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, id).Scan(&at))
		fixture.observed[id] = at.UTC()
	}
	for _, id := range []string{fixture.evaluatedChangeID, fixture.pendingEpochOneChangeID, fixture.pendingEpochTwoChangeID} {
		fixture.payloads[id] = v23FindingChangeArchiveBytes(t, h, id)
		fixture.kinds[id] = "finding-change-event"
		var at time.Time
		ok(t, "read V23 finding-change observed time", h.services.db.QueryRow(h.services.ctx, `SELECT change_at FROM `+
			notificationTable(h, "finding_change_events")+` WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, id).Scan(&at))
		fixture.observed[id] = at.UTC()
	}
	for _, id := range []string{fixture.activePolicyEventID, fixture.noDeliveryPolicyEventID} {
		fixture.payloads[id] = v23PolicyEventArchiveBytes(t, h, id)
		fixture.kinds[id] = "notification-policy-event"
		var at time.Time
		ok(t, "read V23 policy event observed time", h.services.db.QueryRow(h.services.ctx, `SELECT created_at FROM `+
			notificationTable(h, "notification_policy_events")+` WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, id).Scan(&at))
		fixture.observed[id] = at.UTC()
	}
	return fixture
}

func v23RetentionItem(t *testing.T, preview retentionPreview, kind, id string) retentionPreviewItem {
	t.Helper()
	for _, item := range preview.Items {
		if item.ResourceKind == kind && item.ResourceID == id {
			return item
		}
	}
	t.Fatalf("V23 preview omitted %s %s", kind, id)
	return retentionPreviewItem{}
}

func v23Approval(preview retentionPreview, rationale, key string) object {
	return object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale": rationale, "idempotencyKey": key,
	}
}

func TestM08_V23HistoryRetentionPreviewHoldsBytesAndApprovalRemainNonDestructive(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	provider := &providerTripwire{}
	fixture := seedV23HistoryRetention(t, h, provider)
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()

	holdSpecs := []struct {
		kind, id string
	}{
		{"finding-decision-event", fixture.decisionIDs[1]},
		{"notification-policy-revision", fixture.revisionIDs[1]},
		{"finding-change-event", fixture.pendingEpochOneChangeID},
		{"notification-policy-event", fixture.activePolicyEventID},
	}
	for _, spec := range holdSpecs {
		hold := h.json(h.admin, "POST", "/api/v1/retention/holds", object{
			"resourceKind": spec.kind, "resourceId": spec.id,
			"reason": "V23 exact workspace-scoped history hold.",
		}, 201).RetentionHold
		if hold.ResourceKind != spec.kind || hold.ResourceID != spec.id || hold.Revision != 1 {
			t.Fatal("V23 history hold response lost its exact resource identity")
		}
		fixture.holds[spec.kind] = hold
	}
	h.denied(viewer, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "finding-decision-event", "resourceId": fixture.decisionIDs[0], "reason": "Viewer write.",
	}, 403, "forbidden")
	h.denied(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "undeclared-history", "resourceId": fixture.decisionIDs[0], "reason": "Wrong kind.",
	}, 400, "invalid-input")
	h.denied(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "finding-decision-event", "resourceId": "abc", "reason": "Malformed identity.",
	}, 400, "invalid-input")
	h.denied(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "finding-change-event", "resourceId": fixture.decisionIDs[0], "reason": "Wrong table.",
	}, 404, "not-found")
	h.denied(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "notification-policy-event", "resourceId": strings.Repeat("f", 32), "reason": "Missing event.",
	}, 404, "not-found")
	h.denied(foreign, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "finding-decision-event", "resourceId": fixture.decisionIDs[0], "reason": "Foreign event.",
	}, 404, "not-found")
	h.denied(foreign, "POST", "/api/v1/retention/holds/"+fixture.holds["finding-decision-event"].ID+"/releases",
		object{"revision": int64(1), "rationale": "Foreign release."}, 404, "not-found")
	h.denied(h.admin, "POST", "/api/v1/retention/holds/"+strings.Repeat("e", 32)+"/releases",
		object{"revision": int64(1), "rationale": "Missing release."}, 404, "not-found")
	holds := h.json(viewer, "GET", "/api/v1/retention/holds", nil, 200).RetentionHolds
	if len(holds) != len(holdSpecs) {
		t.Fatalf("viewer read %d V23 holds, want %d", len(holds), len(holdSpecs))
	}
	h.denied(viewer, "POST", "/api/v1/retention/holds/"+fixture.holds["finding-decision-event"].ID+"/releases",
		object{"revision": int64(1), "rationale": "Viewer cannot release."}, 403, "forbidden")

	objectStore.arm()
	beforePayloads := map[string]string{}
	for id, payload := range fixture.payloads {
		beforePayloads[id] = v23ArchiveDigest(payload)
	}
	preview := h.json(viewer, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	if preview.State != "ready" || preview.Revision != 1 ||
		preview.ExpiresAt.Sub(preview.CreatedAt) != 15*time.Minute || len(preview.Items) != 10 {
		t.Fatalf("V23 preview binding or bounded item set is incomplete: %#v", preview)
	}
	equal(t, "viewer can read exact V23 preview",
		h.json(viewer, "GET", "/api/v1/retention/previews/"+preview.ID, nil, 200).RetentionPreview, preview)
	equal(t, "V23 summary class order", []string{
		preview.Summaries[0].Class, preview.Summaries[1].Class, preview.Summaries[2].Class,
		preview.Summaries[3].Class, preview.Summaries[4].Class,
	}, []string{"hot-history", "archived-evidence", "raw-report", "audit", "orphan-archive"})
	audit := retentionSummary(t, preview, "audit")
	if audit.Action != "archive-audit" || audit.RetainDays != 730 ||
		audit.TotalCount != 10 || audit.EligibleCount != 4 || audit.ProtectedCount != 6 {
		t.Fatalf("V23 audit summary does not match the exact candidate/protection cohort: %#v", audit)
	}
	equal(t, "held decision protection",
		v23RetentionItem(t, preview, "finding-decision-event", fixture.decisionIDs[1]).ProtectedReasons,
		[]string{"legal-hold"})
	equal(t, "old policy revision protection",
		v23RetentionItem(t, preview, "notification-policy-revision", fixture.revisionIDs[1]).ProtectedReasons,
		[]string{"legal-hold", "pending-policy-evaluation"})
	equal(t, "unreferenced older policy revision eligibility",
		v23RetentionItem(t, preview, "notification-policy-revision", fixture.revisionIDs[2]).ProtectedReasons,
		[]string{})
	equal(t, "current policy revision protection",
		v23RetentionItem(t, preview, "notification-policy-revision", fixture.revisionIDs[3]).ProtectedReasons,
		[]string{"current-policy-revision", "pending-policy-evaluation"})
	equal(t, "held pending change protection",
		v23RetentionItem(t, preview, "finding-change-event", fixture.pendingEpochOneChangeID).ProtectedReasons,
		[]string{"legal-hold", "pending-policy-evaluation"})
	equal(t, "pending change protection",
		v23RetentionItem(t, preview, "finding-change-event", fixture.pendingEpochTwoChangeID).ProtectedReasons,
		[]string{"pending-policy-evaluation"})
	equal(t, "evaluated change eligibility",
		v23RetentionItem(t, preview, "finding-change-event", fixture.evaluatedChangeID).ProtectedReasons,
		[]string{})
	equal(t, "active delivery protection",
		v23RetentionItem(t, preview, "notification-policy-event", fixture.activePolicyEventID).ProtectedReasons,
		[]string{"legal-hold", "active-delivery"})
	equal(t, "no-delivery policy event eligibility",
		v23RetentionItem(t, preview, "notification-policy-event", fixture.noDeliveryPolicyEventID).ProtectedReasons,
		[]string{})

	type ordered struct {
		kind string
		at   time.Time
		id   string
	}
	expectedOrder := make([]ordered, 0, len(fixture.payloads))
	var eligibleBytes int64
	for id, payload := range fixture.payloads {
		item := v23RetentionItem(t, preview, fixture.kinds[id], id)
		if item.Class != "audit" || item.Action != "archive-audit" ||
			item.ObservedAt != fixture.observed[id] || item.SizeBytes != int64(len(payload)) {
			t.Fatalf("V23 preview item lost class, action, time or exact logical bytes: %#v", item)
		}
		if len(item.ProtectedReasons) == 0 {
			eligibleBytes += int64(len(payload))
		}
		expectedOrder = append(expectedOrder, ordered{fixture.kinds[id], fixture.observed[id], id})
	}
	if audit.SizeBytes != eligibleBytes {
		t.Fatalf("V23 audit eligible bytes=%d, want exact logical bytes %d", audit.SizeBytes, eligibleBytes)
	}
	slices.SortFunc(expectedOrder, func(left, right ordered) int {
		if left.kind != right.kind {
			return strings.Compare(left.kind, right.kind)
		}
		if !left.at.Equal(right.at) {
			if left.at.Before(right.at) {
				return -1
			}
			return 1
		}
		return strings.Compare(left.id, right.id)
	})
	for index, want := range expectedOrder {
		got := preview.Items[index]
		if got.ResourceKind != want.kind || got.ObservedAt != want.at || got.ResourceID != want.id {
			t.Fatalf("V23 deterministic audit order item %d=%#v, want %#v", index, got, want)
		}
	}
	h.denied(foreign, "GET", "/api/v1/retention/previews/"+preview.ID, nil, 404, "not-found")
	h.denied(viewer, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
		v23Approval(preview, "Viewer cannot approve V23 history.", "v23-viewer-approval"), 403, "forbidden")
	approval := v23Approval(preview, "Approve the exact V23 history preview.", "v23-history-approval")
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
		approval, 201).RetentionPreview
	if approved.State != "approved" || approved.Revision != 2 {
		t.Fatal("V23 history approval did not return its exact durable receipt")
	}
	equal(t, "V23 exact approval replay",
		h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
			approval, 200).RetentionPreview, approved)
	changedApproval := v23Approval(preview, "Changed V23 replay must conflict.", "v23-history-approval")
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
		changedApproval, 409, "conflict")
	var runsAfterApproval, itemsAfterApproval int
	ok(t, "count V23 runs after approval", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+notificationTable(h, "retention_runs")+` WHERE workspace_id=$1`,
		h.admin.workspace).Scan(&runsAfterApproval))
	ok(t, "count V23 run items after approval", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+notificationTable(h, "retention_run_items")+` WHERE workspace_id=$1`,
		h.admin.workspace).Scan(&itemsAfterApproval))
	equal(t, "V23 approval created no run", runsAfterApproval, 0)
	equal(t, "V23 approval created no run item", itemsAfterApproval, 0)
	for id, before := range beforePayloads {
		var current []byte
		switch fixture.kinds[id] {
		case "finding-decision-event":
			current = v23DecisionArchiveBytes(t, h, id)
		case "notification-policy-revision":
			current = v23PolicyRevisionArchiveBytes(t, h, id)
		case "finding-change-event":
			current = v23FindingChangeArchiveBytes(t, h, id)
		case "notification-policy-event":
			current = v23PolicyEventArchiveBytes(t, h, id)
		}
		if v23ArchiveDigest(current) != before {
			t.Fatalf("V23 preview, approval or execution gate mutated %s", id)
		}
	}
	if provider.calls.Load() != 0 || objectStore.calls.Load() != 0 {
		t.Fatal("V23 preview or approval performed provider or object-store I/O")
	}
}

func TestM08_V23HistoryRetentionApprovalBindsAllMutableAuthority(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	objectStore.arm()
	staleAfter := func(label string, mutate func()) {
		t.Helper()
		preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
		mutate()
		h.denied(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals",
			v23Approval(preview, "Reject stale V23 authority "+label+".", "v23-stale-"+label),
			409, "conflict")
		current := h.json(h.admin, "GET", "/api/v1/retention/previews/"+preview.ID, nil, 200).RetentionPreview
		if current.State != "stale" || current.Revision != preview.Revision+1 {
			t.Fatalf("V23 %s change did not durably stale its preview", label)
		}
	}

	var transient retentionHold
	staleAfter("hold-create", func() {
		transient = h.json(h.admin, "POST", "/api/v1/retention/holds", object{
			"resourceKind": "finding-decision-event", "resourceId": fixture.decisionIDs[0],
			"reason": "Create after V23 preview.",
		}, 201).RetentionHold
	})
	staleAfter("hold-release", func() {
		h.json(h.admin, "POST", "/api/v1/retention/holds/"+transient.ID+"/releases", object{
			"revision": transient.Revision, "rationale": "Release after V23 preview.",
		}, 200)
	})
	staleAfter("policy-revision", func() {
		h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+fixture.policy.ID, object{
			"name": "V23 policy authority changed", "rationale": "Invalidate the prior retention snapshot.",
		}, 200)
	})
	staleAfter("processing-fence", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_change_events")+`
			SET fence=fence+1,lease_until=lease_until+interval '1 minute'
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.pendingEpochOneChangeID)
		ok(t, "change V23 processing fence and lease authority", err)
	})
	staleAfter("pending-evaluated", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_change_events")+`
			SET state='evaluated',worker_id=NULL,lease_until=NULL
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.pendingEpochTwoChangeID)
		ok(t, "transition V23 change event to evaluated", err)
	})
	staleAfter("delivery-dispatching", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_deliveries")+`
			SET state='dispatching',worker_id='v23-delivery-worker',fence=1,
				lease_until=$3,dispatch_started_at=$4
			WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, fixture.activeDeliveryID,
			h.services.cfg.Now().Add(time.Hour), h.services.cfg.Now())
		ok(t, "transition V23 linked delivery to dispatching", err)
	})
	dispatchingPreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "dispatching delivery remains protected",
		v23RetentionItem(t, dispatchingPreview, "notification-policy-event",
			fixture.activePolicyEventID).ProtectedReasons, []string{"active-delivery"})
	staleAfter("delivery-terminal", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+notificationTable(h, "finding_deliveries")+`
			SET state='confirmed',completed_at=$3,worker_id=NULL,lease_until=NULL
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.activeDeliveryID, h.services.cfg.Now())
		ok(t, "transition V23 linked delivery to terminal", err)
	})
	terminalPreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "terminal delivery policy event is eligible",
		v23RetentionItem(t, terminalPreview, "notification-policy-event",
			fixture.activePolicyEventID).ProtectedReasons, []string{})
	insertedID := v23ID(2)
	staleAfter("candidate-insert", func() {
		before := v23ArchiveBytes(t, map[string]any{
			"ownerId": nil, "workflowState": "open", "disposition": "none",
			"acceptedRiskExpiresAt": nil, "dispositionScope": "",
			"suppressionExpiresAt": nil, "dispositionRationale": "",
		})
		after := v23ArchiveBytes(t, map[string]any{
			"ownerId": nil, "workflowState": "pending-retest", "disposition": "none",
			"acceptedRiskExpiresAt": nil, "dispositionScope": "",
			"suppressionExpiresAt": nil, "dispositionRationale": "",
		})
		_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+
			notificationTable(h, "finding_decision_events")+`
			(id,workspace_id,finding_id,decision_revision,actor_id,action,rationale,
			 changed_fields,before_state,after_state,created_at)
			SELECT $1,$2,id,23001,$3,'update','Inserted after preview.',
			 '["workflowState"]'::jsonb,$4::jsonb,$5::jsonb,$6
			FROM `+notificationTable(h, "findings")+` WHERE workspace_id=$2 ORDER BY id LIMIT 1`,
			insertedID, h.admin.workspace, h.admin.user.ID, string(before), string(after),
			time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
		ok(t, "insert V23 retention candidate after preview", err)
	})
	staleAfter("candidate-remove", func() {
		result, err := h.services.db.Exec(h.services.ctx, `DELETE FROM `+
			notificationTable(h, "finding_decision_events")+` WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, insertedID)
		ok(t, "remove V23 retention candidate after preview", err)
		if result.RowsAffected() != 1 {
			t.Fatal("V23 candidate removal fixture did not remove one row")
		}
	})
	staleAfter("decision-payload", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
			notificationTable(h, "finding_decision_events")+`
			SET rationale=rationale||' Changed after preview.'
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.decisionIDs[0])
		ok(t, "change V23 canonical payload after preview", err)
	})
	staleAfter("policy-revision-payload", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
			notificationTable(h, "notification_policy_revisions")+`
			SET rationale=rationale||' Changed after preview.'
			WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fixture.revisionIDs[1])
		ok(t, "change V23 policy revision payload after preview", err)
	})
	staleAfter("finding-change-payload", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
			notificationTable(h, "finding_change_events")+`
			SET title=title||' changed' WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, fixture.evaluatedChangeID)
		ok(t, "change V23 finding-change payload after preview", err)
	})
	staleAfter("policy-event-payload", func() {
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
			notificationTable(h, "notification_policy_events")+`
			SET outcome='invalid-payload' WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, fixture.noDeliveryPolicyEventID)
		ok(t, "change V23 notification-policy event payload after preview", err)
	})
	if objectStore.calls.Load() != 0 {
		t.Fatal("V23 stale approval checks performed object-store I/O")
	}
}

func TestM08_V23HistoryRetentionPreviewSharesTheWholeWorkspaceCap(t *testing.T) {
	h := newHarness(t, true)
	_, _, finding := h.seed()
	table := notificationTable(h, "finding_decision_events")
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin V23 cap fixture", err)
	defer tx.Rollback(h.services.ctx)
	state := `{"ownerId":null,"workflowState":"open","disposition":"none","acceptedRiskExpiresAt":null,"dispositionScope":"","suppressionExpiresAt":null,"dispositionRationale":""}`
	at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 200; index++ {
		_, err = tx.Exec(h.services.ctx, `INSERT INTO `+table+`
			(id,workspace_id,finding_id,decision_revision,actor_id,action,rationale,
			 changed_fields,before_state,after_state,created_at)
			VALUES($1,$2,$3,$4,$5,'update','V23 cap fixture.','[]'::jsonb,$6::jsonb,$6::jsonb,$7)`,
			v23ID(100+index), h.admin.workspace, finding.ID, int64(index+2), h.admin.user.ID, state,
			at.Add(time.Duration(index)*time.Second))
		ok(t, "insert bounded V23 cap candidate", err)
	}
	ok(t, "commit V23 cap fixture", tx.Commit(h.services.ctx))
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	if len(preview.Items) != 200 {
		t.Fatalf("V23 whole-workspace cap preview items=%d, want 200", len(preview.Items))
	}
	_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+table+`
		(id,workspace_id,finding_id,decision_revision,actor_id,action,rationale,
		 changed_fields,before_state,after_state,created_at)
		VALUES($1,$2,$3,202,$4,'update','V23 overflow fixture.','[]'::jsonb,$5::jsonb,$5::jsonb,$6)`,
		v23ID(300), h.admin.workspace, finding.ID, h.admin.user.ID, state, at.Add(201*time.Second))
	ok(t, "insert V23 overflow candidate", err)
	h.denied(h.admin, "POST", "/api/v1/retention/previews", object{}, 413, "too-large")
}
