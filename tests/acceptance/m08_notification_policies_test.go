//go:build integration

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const notificationPoliciesPath = "/api/v1/integrations/notification-policies"

type policyConnection struct {
	ID, Name, Profile string
	Revision          int64
	Enabled, Current  bool
}

type notificationPolicy struct {
	ID, WorkspaceID, Name, ConnectionID, ConnectionProfile, MinimumSeverity string
	ApprovedBy, ApprovedByName, Rationale                                   string
	ConnectionRevision, Revision, Epoch                                     int64
	Enabled                                                                 bool
	ChangeKinds                                                             []string
	Connection                                                              policyConnection
	CreatedAt, UpdatedAt                                                    time.Time
}

type notificationPolicyEvent struct {
	ID, WorkspaceID, PolicyID, FindingID, Outcome string
	PolicyRevision, FindingChangeRevision         int64
	DeliveryID                                    *string
	CreatedAt                                     time.Time
}

type nativeConnection struct {
	ID, WorkspaceID, Profile, Name, Channel string
	Enabled, CredentialConfigured           bool
	Revision                                int64
	CreatedAt, UpdatedAt                    time.Time
}

type automaticDelivery struct {
	ID, WorkspaceID, FindingID, ConnectionID, Profile, Channel, RequestedBy, State string
	TriggerKind, PolicyID                                                          string
	ConnectionRevision, PolicyRevision, FindingChangeRevision                      int64
	Payload                                                                        struct {
		Title, Body, DeepLink string
		Fields                map[string]string
	}
	CreatedAt                      time.Time
	DispatchStartedAt, CompletedAt *time.Time
	Receipt, Failure               any
}

type notificationReply struct {
	APIVersion string
	Error      *apiFailure
	Policy     notificationPolicy
	Connection nativeConnection
	Delivery   automaticDelivery
	Items      []json.RawMessage
	Total      int
	NextCursor *string
}

type findingChangeEvent struct {
	ID, WorkspaceID, FindingID, Title, Severity, AssetName, ChangeKind, State string
	PolicyEpoch, ChangeRevision, Fence                                        int64
	ChangeAt                                                                  time.Time
	WorkerID                                                                  *string
	LeaseUntil                                                                *time.Time
}

type providerTripwire struct{ calls atomic.Int32 }

func (p *providerTripwire) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls.Add(1)
	return nil, errors.New("provider I/O is forbidden during policy evaluation")
}

func newNotificationHarness(t *testing.T) *harness {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 10, 8, 3, 30, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.BootstrapToken = secret(t)
	key := sha256.Sum256([]byte(secret(t)))
	h.services.cfg.IntegrationEncryptionKey = append([]byte(nil), key[:]...)
	h.services.cfg.PublicOrigin = "https://aspm.test"
	h.services.cfg.LogOutput = io.Discard
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close notification-policy application before owned-resource cleanup", h.app.Close())
		}
	})
	h.enroll()
	return h
}

func (h *harness) notificationJSON(a actor, method, path string, body any, want int) notificationReply {
	h.t.Helper()
	var data []byte
	if body != nil {
		data = encode(h.t, body)
	}
	response := h.request(a, method, path, data, want)
	var reply notificationReply
	ok(h.t, "decode notification-policy response", json.Unmarshal(response.Body.Bytes(), &reply))
	equal(h.t, "notification-policy API version", reply.APIVersion, apiVersion)
	if want >= 400 && (reply.Error == nil || reply.Error.Code == "" || reply.Error.RequestID == "" || reply.Error.Retryable) {
		h.t.Fatal("notification-policy rejection must be explicit, identified and nonretryable")
	}
	return reply
}

func (h *harness) createSlackConnection(name, channel string, enabled bool) nativeConnection {
	h.t.Helper()
	token := secret(h.t)
	reply := h.notificationJSON(h.admin, "POST", "/api/v1/integrations/connections", object{
		"profile": "slack-workspace-bot", "name": name, "channel": channel, "token": token, "enabled": enabled,
	}, 201)
	value := reply.Connection
	if value.ID == "" || value.WorkspaceID != h.admin.workspace || value.Profile != "slack-workspace-bot" ||
		value.Name != name || value.Channel != channel || value.Enabled != enabled ||
		!value.CredentialConfigured || value.Revision != 1 {
		h.t.Fatal("notification-policy fixture did not create an exact native Slack connection")
	}
	return value
}

func policyInput(name, connectionID string, enabled bool, kinds []string, severity, rationale string) object {
	return object{
		"name": name, "connectionId": connectionID, "enabled": enabled, "changeKinds": kinds,
		"minimumSeverity": severity, "rationale": rationale,
	}
}

func (h *harness) createPolicy(input object) notificationPolicy {
	h.t.Helper()
	return h.notificationJSON(h.admin, "POST", notificationPoliciesPath, input, 201).Policy
}

func decodePolicyItems(t *testing.T, reply notificationReply) []notificationPolicy {
	t.Helper()
	result := make([]notificationPolicy, 0, len(reply.Items))
	for _, raw := range reply.Items {
		var policy notificationPolicy
		ok(t, "decode notification policy item", json.Unmarshal(raw, &policy))
		result = append(result, policy)
	}
	return result
}

func decodePolicyEvents(t *testing.T, reply notificationReply) []notificationPolicyEvent {
	t.Helper()
	result := make([]notificationPolicyEvent, 0, len(reply.Items))
	for _, raw := range reply.Items {
		var event notificationPolicyEvent
		ok(t, "decode notification policy event", json.Unmarshal(raw, &event))
		result = append(result, event)
	}
	return result
}

func (h *harness) policyEvents(a actor, id string) []notificationPolicyEvent {
	h.t.Helper()
	reply := h.notificationJSON(a, "GET", notificationPoliciesPath+"/"+id+"/events?limit=100", nil, 200)
	if reply.NextCursor != nil || reply.Total != len(reply.Items) {
		h.t.Fatal("small notification-policy event history must have an exact total and no next page")
	}
	return decodePolicyEvents(h.t, reply)
}

func notificationTable(h *harness, name string) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_" + name}.Sanitize()
}

func (h *harness) changeEvents() []findingChangeEvent {
	h.t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT id,workspace_id,finding_id,policy_epoch,title,severity,
		asset_name,change_kind,change_revision,change_at,state,worker_id,fence,lease_until
		FROM `+notificationTable(h, "finding_change_events")+` ORDER BY change_revision,finding_id,id`)
	ok(h.t, "read durable finding change events", err)
	defer rows.Close()
	var result []findingChangeEvent
	for rows.Next() {
		var event findingChangeEvent
		ok(h.t, "scan durable finding change event", rows.Scan(
			&event.ID, &event.WorkspaceID, &event.FindingID, &event.PolicyEpoch, &event.Title, &event.Severity,
			&event.AssetName, &event.ChangeKind, &event.ChangeRevision, &event.ChangeAt, &event.State,
			&event.WorkerID, &event.Fence, &event.LeaseUntil,
		))
		result = append(result, event)
	}
	ok(h.t, "finish durable finding change event read", rows.Err())
	return result
}

func (h *harness) openPolicyWorker(t *testing.T, tripwire *providerTripwire) DeliveryWorker {
	t.Helper()
	requireDeliveryWorker(t)
	worker, err := Production.OpenDeliveryWorker(h.services.ctx, DeliveryWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: h.services.cfg.ApplicationName + "-notification-policy",
		MaxConnections:  1, EncryptionKey: h.services.cfg.IntegrationEncryptionKey,
		WorkerID: "notification-policy-" + nonce(t), LeaseDuration: 2 * time.Second,
		PublicOrigin: h.services.cfg.PublicOrigin, SlackEndpoint: "https://127.0.0.1:44443",
		Client:    &http.Client{Transport: tripwire, Timeout: time.Second},
		LogOutput: h.services.cfg.LogOutput, QueryTracer: h.services.cfg.QueryTracer,
	})
	ok(t, "open independent delivery worker for policy evaluation", err)
	if worker.ProcessNext == nil || worker.Close == nil {
		t.Fatal("delivery worker binding omitted processing or close")
	}
	t.Cleanup(func() { ok(t, "close notification-policy delivery worker", worker.Close()) })
	return worker
}

func TestM08_AuthoritativeScanBeforeFirstPolicyCreatesNoPolicyWork(t *testing.T) {
	h := newNotificationHarness(t)
	var epoch int64
	ok(t, "read initial workspace policy epoch", h.services.db.QueryRow(h.services.ctx,
		`SELECT notification_policy_epoch FROM `+notificationTable(h, "workspaces")+` WHERE id=$1`,
		h.admin.workspace).Scan(&epoch))
	equal(t, "initial workspace policy epoch", epoch, int64(0))
	asset := h.asset(h.admin, "Pre-policy event repository", &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["scanId"] = "pre-policy-authoritative-scan"
	h.finish(h.upload(input).ID, "succeeded")
	equal(t, "epoch-zero authoritative scan creates no policy event", len(h.changeEvents()), 0)
	tripwire := &providerTripwire{}
	worker := h.openPolicyWorker(t, tripwire)
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "observe empty pre-policy worker queue", err)
	if processed || tripwire.calls.Load() != 0 {
		t.Fatal("epoch-zero authoritative scan created worker-visible policy work or provider I/O")
	}
}

func TestM08_NotificationPoliciesAreAdminApprovedVersionedAndBounded(t *testing.T) {
	h := newNotificationHarness(t)
	connection := h.createSlackConnection("Primary policy destination", "C123", true)
	analyst, viewer := h.addUser(h.admin, "analyst"), h.addUser(h.admin, "viewer")
	base := policyInput("High severity lifecycle changes", connection.ID, true,
		[]string{"new", "changed", "reopened"}, "high", "Approve explicit high severity lifecycle notifications.")

	h.notificationJSON(actor{}, "GET", notificationPoliciesPath, nil, 401)
	empty := h.notificationJSON(viewer, "GET", notificationPoliciesPath, nil, 200)
	equal(t, "empty viewer policy total", empty.Total, 0)
	for _, who := range []actor{analyst, viewer} {
		reply := h.notificationJSON(who, "POST", notificationPoliciesPath, base, 403)
		equal(t, "non-admin create denial", reply.Error.Code, "forbidden")
	}
	foreign := h.addWorkspace()
	foreignConnection := func() nativeConnection {
		token := secret(t)
		return h.notificationJSON(foreign, "POST", "/api/v1/integrations/connections", object{
			"profile": "slack-workspace-bot", "name": "Foreign policy destination",
			"channel": "G456", "token": token, "enabled": true,
		}, 201).Connection
	}()
	foreignBody := policyInput("Foreign connection", foreignConnection.ID, true, []string{"new"}, "info", "Must stay scoped.")
	reply := h.notificationJSON(h.admin, "POST", notificationPoliciesPath, foreignBody, 404)
	equal(t, "foreign connection denial", reply.Error.Code, "not-found")

	for _, invalid := range []object{
		policyInput("", connection.ID, true, []string{"new"}, "high", "Nonblank name required."),
		policyInput(strings.Repeat("x", 257), connection.ID, true, []string{"new"}, "high", "Bounded name required."),
		policyInput("NUL\x00name", connection.ID, true, []string{"new"}, "high", "NUL-free name required."),
		policyInput("Blank rationale", connection.ID, true, []string{"new"}, "high", " \t "),
		policyInput("Long rationale", connection.ID, true, []string{"new"}, "high", strings.Repeat("r", 8193)),
		policyInput("No changes", connection.ID, true, []string{}, "high", "Select a change kind."),
		policyInput("Duplicate changes", connection.ID, true, []string{"new", "new"}, "high", "No duplicate change kinds."),
		policyInput("Unknown change", connection.ID, true, []string{"resolved"}, "high", "Only canonical meaningful changes."),
		policyInput("Unknown severity", connection.ID, true, []string{"new"}, "urgent", "Only bounded severities."),
	} {
		h.notificationJSON(h.admin, "POST", notificationPoliciesPath, invalid, 400)
	}
	for _, field := range []string{"profile", "connectionRevision", "actor", "epoch", "payload", "endpoint", "headers", "secret"} {
		input := policyInput("No caller authority "+field, connection.ID, true, []string{"new"}, "high", "Reject caller authority.")
		input[field] = "caller-controlled"
		h.notificationJSON(h.admin, "POST", notificationPoliciesPath, input, 400)
	}

	policy := h.createPolicy(base)
	if policy.ID == "" || policy.WorkspaceID != h.admin.workspace || policy.Name != base["name"] ||
		policy.ConnectionID != connection.ID || policy.ConnectionProfile != connection.Profile ||
		policy.ConnectionRevision != connection.Revision || !policy.Enabled ||
		!reflect.DeepEqual(policy.ChangeKinds, []string{"new", "changed", "reopened"}) ||
		policy.MinimumSeverity != "high" || policy.Revision != 1 || policy.Epoch != 1 ||
		policy.ApprovedBy != h.admin.user.ID || policy.ApprovedByName != h.admin.user.Name ||
		policy.Rationale != base["rationale"] || policy.CreatedAt.IsZero() || policy.UpdatedAt.IsZero() {
		t.Fatalf("created notification policy lost its approved complete snapshot: %#v", policy)
	}
	if policy.Connection.ID != connection.ID || policy.Connection.Name != connection.Name ||
		policy.Connection.Profile != connection.Profile || policy.Connection.Revision != connection.Revision ||
		!policy.Connection.Enabled || !policy.Connection.Current {
		t.Fatal("policy response omitted current native connection metadata")
	}
	list := h.notificationJSON(viewer, "GET", notificationPoliciesPath, nil, 200)
	equal(t, "viewer policy total", list.Total, 1)
	equal(t, "viewer policy list", decodePolicyItems(t, list), []notificationPolicy{policy})
	equal(t, "viewer policy detail", h.notificationJSON(viewer, "GET",
		notificationPoliciesPath+"/"+policy.ID, nil, 200).Policy, policy)
	equal(t, "empty policy history", len(h.policyEvents(viewer, policy.ID)), 0)
	h.notificationJSON(viewer, "PATCH", notificationPoliciesPath+"/"+policy.ID, object{
		"name": "Viewer cannot rename", "rationale": "Viewer write must fail.",
	}, 403)
	h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+policy.ID,
		object{"rationale": "A rationale alone is not a policy change."}, 400)
	h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+policy.ID,
		object{"name": policy.Name, "rationale": "Equal values are a no-op."}, 409)
	h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+policy.ID,
		object{"name": "Missing rationale"}, 400)
	for _, field := range []string{"profile", "connectionRevision", "actor", "epoch", "payload", "endpoint", "headers", "secret"} {
		h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+policy.ID, object{
			"name": "Reject " + field, "rationale": "Reject caller authority.", field: "caller-controlled",
		}, 400)
	}

	h.clock.Add(int64(time.Second))
	updated := h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+policy.ID, object{
		"name": "Medium new and reopened changes", "changeKinds": []string{"new", "reopened"},
		"minimumSeverity": "medium", "rationale": "Expand the reviewed bounded trigger set.",
	}, 200).Policy
	if updated.Revision != 2 || updated.Epoch != 2 || updated.Name != "Medium new and reopened changes" ||
		!reflect.DeepEqual(updated.ChangeKinds, []string{"new", "reopened"}) ||
		updated.MinimumSeverity != "medium" || updated.Rationale != "Expand the reviewed bounded trigger set." ||
		updated.ApprovedBy != h.admin.user.ID || !updated.UpdatedAt.After(policy.UpdatedAt) {
		t.Fatalf("real policy update did not create its next approved revision: %#v", updated)
	}
	rows, err := h.services.db.Query(h.services.ctx, `SELECT revision,epoch,name,connection_id,connection_profile,
		connection_revision,enabled,change_kinds,minimum_severity,actor_id,actor_name,rationale,created_at
		FROM `+notificationTable(h, "notification_policy_revisions")+`
		WHERE workspace_id=$1 AND policy_id=$2 ORDER BY revision`, h.admin.workspace, policy.ID)
	ok(t, "read immutable policy revisions", err)
	defer rows.Close()
	type revision struct {
		Revision, Epoch, ConnectionRevision                                        int64
		Name, ConnectionID, ConnectionProfile, MinimumSeverity, ActorID, ActorName string
		Rationale                                                                  string
		Enabled                                                                    bool
		ChangeKinds                                                                []string
		CreatedAt                                                                  time.Time
	}
	var revisions []revision
	for rows.Next() {
		var value revision
		ok(t, "scan immutable policy revision", rows.Scan(
			&value.Revision, &value.Epoch, &value.Name, &value.ConnectionID, &value.ConnectionProfile,
			&value.ConnectionRevision, &value.Enabled, &value.ChangeKinds, &value.MinimumSeverity,
			&value.ActorID, &value.ActorName, &value.Rationale, &value.CreatedAt,
		))
		revisions = append(revisions, value)
	}
	ok(t, "finish immutable policy revision read", rows.Err())
	if len(revisions) != 2 || revisions[0].Revision != 1 || revisions[0].Epoch != 1 ||
		revisions[0].Name != policy.Name || revisions[0].Rationale != policy.Rationale ||
		revisions[1].Revision != 2 || revisions[1].Epoch != 2 || revisions[1].Name != updated.Name {
		t.Fatalf("policy update rewrote or omitted immutable history: %#v", revisions)
	}

	for index := 0; index < 31; index++ {
		created := h.createPolicy(policyInput(
			"Bounded policy "+time.Unix(int64(index), 0).UTC().Format("150405"),
			connection.ID, index%2 == 0, []string{"new"}, "info", "Fill the explicit workspace policy bound.",
		))
		equal(t, "workspace policy epoch increments", created.Epoch, int64(index+3))
	}
	full := h.notificationJSON(viewer, "GET", notificationPoliciesPath, nil, 200)
	if full.Total != 32 || len(full.Items) != 32 || full.NextCursor != nil {
		t.Fatal("workspace policy cap must remain an exact small collection")
	}
	overflow := h.notificationJSON(h.admin, "POST", notificationPoliciesPath,
		policyInput("Thirty third policy", connection.ID, true, []string{"new"}, "info", "Cap must reject this policy."), 409)
	equal(t, "policy cap conflict", overflow.Error.Code, "conflict")
	var epoch int64
	ok(t, "read workspace policy epoch", h.services.db.QueryRow(h.services.ctx,
		`SELECT notification_policy_epoch FROM `+notificationTable(h, "workspaces")+` WHERE id=$1`,
		h.admin.workspace).Scan(&epoch))
	equal(t, "workspace policy epoch", epoch, int64(33))

	rotated := h.notificationJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"enabled": false}, 200).Connection
	equal(t, "connection revision advanced", rotated.Revision, int64(2))
	current := h.notificationJSON(viewer, "GET", notificationPoliciesPath+"/"+policy.ID, nil, 200).Policy
	if current.Connection.Current || current.Connection.Enabled || current.Connection.Revision != rotated.Revision ||
		current.ConnectionRevision != connection.Revision || current.ConnectionProfile != connection.Profile {
		t.Fatal("policy did not preserve its snapshot while reporting the current disabled connection")
	}
	other := h.addWorkspace()
	h.notificationJSON(other, "GET", notificationPoliciesPath+"/"+policy.ID, nil, 404)
}

func TestM08_OnlyAuthoritativeMeaningfulScansCreateDurablePolicyEvents(t *testing.T) {
	h := newNotificationHarness(t)
	connection := h.createSlackConnection("Durable event destination", "G456", true)
	policy := h.createPolicy(policyInput("All meaningful changes", connection.ID, true,
		[]string{"new", "changed", "reopened"}, "info", "Approve all bounded meaningful change kinds."))
	asset := h.asset(h.admin, "Policy event repository", &h.admin.user.ID)
	run := func(scanID string, at *time.Time, report []byte, change func(object)) imported {
		input := h.input(asset.ID, "sarif", report)
		collectedAt := sourceTime.Add(200 * time.Hour)
		if at != nil {
			collectedAt = at.Add(time.Hour)
		}
		input["scanId"], input["collectedAt"] = scanID, collectedAt
		input["sourceScanAt"] = at
		if change != nil {
			change(input)
		}
		return h.finish(h.upload(input).ID, "succeeded")
	}
	initialAt := sourceTime
	run("policy-event-new", &initialAt, fixture(t, "sarif.json"), nil)
	work := h.work(h.admin, "")
	equal(t, "one event finding", len(work), 1)
	finding := h.finding(h.admin, work[0].ID)
	events := h.changeEvents()
	if len(events) != 1 || events[0].WorkspaceID != h.admin.workspace || events[0].FindingID != finding.ID ||
		events[0].PolicyEpoch != policy.Epoch || events[0].Title != finding.Title ||
		events[0].Severity != finding.Severity || events[0].AssetName != finding.AssetName ||
		events[0].ChangeKind != "new" || events[0].ChangeRevision != finding.ChangeRevision ||
		events[0].ChangeAt.IsZero() || events[0].State != "pending" ||
		events[0].WorkerID != nil || events[0].Fence != 0 || events[0].LeaseUntil != nil {
		t.Fatalf("initial authoritative scan did not create one bounded pending event: %#v", events)
	}
	equal(t, "pending event has no policy outcome", len(h.policyEvents(h.admin, policy.ID)), 0)
	var deliveries int
	ok(t, "count no inline policy deliveries", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+notificationTable(h, "finding_deliveries")+`
		WHERE workspace_id=$1 AND trigger_kind='notification-policy'`, h.admin.workspace).Scan(&deliveries))
	equal(t, "scan performed no inline delivery creation", deliveries, 0)

	unchangedAt := sourceTime.Add(24 * time.Hour)
	run("policy-event-unchanged", &unchangedAt, fixture(t, "sarif.json"), nil)
	equal(t, "unchanged scan created no event", len(h.changeEvents()), 1)
	failedInput := h.input(asset.ID, "sarif", []byte(`{"version":"2.1.0","runs":[`))
	failedInput["scanId"], failedInput["sourceScanAt"], failedInput["collectedAt"] =
		"policy-event-parse-failure", sourceTime.Add(36*time.Hour), sourceTime.Add(37*time.Hour)
	h.finish(h.upload(failedInput).ID, "failed")
	equal(t, "failed import created no event", len(h.changeEvents()), 1)
	for index, scenario := range []struct {
		name   string
		change func(object)
	}{
		{"failed", func(value object) { value["sourceStatus"], value["completeness"] = "failed", "unknown" }},
		{"partial", func(value object) { value["completeness"] = "partial" }},
		{"delta", func(value object) { value["scanKind"] = "delta" }},
		{"unknown-time", func(value object) { value["sourceScanAt"] = nil }},
	} {
		at := sourceTime.Add(time.Duration(48+index) * time.Hour)
		nonAuthoritativeReport := sarif(t, func(_ object, result object) {
			result["message"] = object{"text": "Non-authoritative " + scenario.name + " policy evidence."}
		})
		run("policy-event-"+scenario.name, &at, nonAuthoritativeReport, scenario.change)
		equal(t, scenario.name+" observation created no policy event", len(h.changeEvents()), 1)
	}
	changedReport := sarif(t, func(_ object, result object) {
		result["message"] = object{"text": "Changed policy-safe canonical evidence."}
	})
	changedAt := sourceTime.Add(96 * time.Hour)
	run("policy-event-changed", &changedAt, changedReport, nil)
	changed := h.finding(h.admin, finding.ID)
	events = h.changeEvents()
	if len(events) != 2 || events[1].ChangeKind != "changed" ||
		events[1].ChangeRevision != changed.ChangeRevision || events[1].PolicyEpoch != policy.Epoch {
		t.Fatalf("authoritative changed scan did not create its exact next event: %#v", events)
	}
	empty := sarif(t, func(run object, _ object) { run["results"] = []any{} })
	resolvedAt := sourceTime.Add(120 * time.Hour)
	run("policy-event-inferred-resolution", &resolvedAt, empty, nil)
	equal(t, "inferred resolution created no policy event", len(h.changeEvents()), 2)
	reopenedAt := sourceTime.Add(144 * time.Hour)
	reopenedInput := h.input(asset.ID, "sarif", changedReport)
	reopenedInput["scanId"], reopenedInput["sourceScanAt"], reopenedInput["collectedAt"] =
		"policy-event-reopened", reopenedAt, reopenedAt.Add(time.Hour)
	reopenedRun := h.finish(h.upload(reopenedInput).ID, "succeeded")
	reopened := h.finding(h.admin, finding.ID)
	events = h.changeEvents()
	if len(events) != 3 || events[2].ChangeKind != "reopened" ||
		events[2].ChangeRevision != reopened.ChangeRevision || events[2].PolicyEpoch != policy.Epoch {
		t.Fatalf("authoritative reappearance did not create one reopened event: %#v", events)
	}
	replay := h.finish(h.upload(reopenedInput).ID, "succeeded")
	equal(t, "identical scan replay retained run identity", replay.RunID, reopenedRun.RunID)
	equal(t, "scan replay created no duplicate event", len(h.changeEvents()), 3)
	historicalAt := sourceTime.Add(110 * time.Hour)
	run("policy-event-historical", &historicalAt, sarif(t, func(_ object, result object) {
		result["message"] = object{"text": "Historical out-of-order evidence."}
	}), nil)
	equal(t, "historical observation created no event", len(h.changeEvents()), 3)
	for _, event := range h.changeEvents() {
		if event.Title == "" || len([]byte(event.Title)) > 1024 || event.AssetName == "" ||
			len([]byte(event.AssetName)) > 256 || event.PolicyEpoch != policy.Epoch {
			t.Fatal("durable change event snapshot is not bounded and epoch-scoped")
		}
	}
	equal(t, "missing delivery worker leaves all events pending", []string{
		h.changeEvents()[0].State, h.changeEvents()[1].State, h.changeEvents()[2].State,
	}, []string{"pending", "pending", "pending"})
	equal(t, "successful imports remain independent of delivery", h.finding(h.admin, finding.ID).ID, finding.ID)
}

func TestM08_PolicyEvaluationUsesEventEpochAndQueuesNoProviderIO(t *testing.T) {
	h := newNotificationHarness(t)
	connection := h.createSlackConnection("Epoch-scoped destination", "C789", true)
	firstPolicy := h.createPolicy(policyInput("Original matching policy", connection.ID, true,
		[]string{"new"}, "medium", "Approve the original event epoch behavior."))
	asset := h.asset(h.admin, "Epoch policy repository", &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["scanId"] = "epoch-policy-new"
	h.finish(h.upload(input).ID, "succeeded")
	finding := h.finding(h.admin, h.work(h.admin, "")[0].ID)
	event := h.changeEvents()[0]
	secondPolicy := h.createPolicy(policyInput("Later policy must not apply", connection.ID, true,
		[]string{"new"}, "info", "Created after the durable event."))
	disabled := h.notificationJSON(h.admin, "PATCH", notificationPoliciesPath+"/"+firstPolicy.ID, object{
		"enabled": false, "rationale": "Disable only for later event epochs.",
	}, 200).Policy
	if event.PolicyEpoch != firstPolicy.Epoch || secondPolicy.Epoch <= event.PolicyEpoch ||
		disabled.Epoch <= secondPolicy.Epoch || disabled.Revision != 2 {
		t.Fatal("fixture did not establish distinct event and later policy epochs")
	}

	tripwire := &providerTripwire{}
	worker := h.openPolicyWorker(t, tripwire)
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "evaluate one durable policy event", err)
	if !processed || tripwire.calls.Load() != 0 {
		t.Fatal("policy evaluation must commit outbox state without provider I/O")
	}
	firstEvents := h.policyEvents(h.admin, firstPolicy.ID)
	if len(firstEvents) != 1 || firstEvents[0].Outcome != "queued" ||
		firstEvents[0].PolicyRevision != 1 || firstEvents[0].FindingID != finding.ID ||
		firstEvents[0].FindingChangeRevision != event.ChangeRevision || firstEvents[0].DeliveryID == nil {
		t.Fatalf("event epoch did not select the latest approved revision available at that epoch: %#v", firstEvents)
	}
	equal(t, "later policy has no retroactive event", len(h.policyEvents(h.admin, secondPolicy.ID)), 0)
	deliveryID := *firstEvents[0].DeliveryID
	response := h.request(h.admin, "GET", "/api/v1/integrations/deliveries/"+deliveryID, nil, 200)
	var deliveryEnvelope notificationReply
	ok(t, "decode automatic finding delivery", json.Unmarshal(response.Body.Bytes(), &deliveryEnvelope))
	delivery := deliveryEnvelope.Delivery
	if delivery.ID != deliveryID || delivery.State != "queued" || delivery.WorkspaceID != h.admin.workspace ||
		delivery.FindingID != finding.ID || delivery.ConnectionID != connection.ID ||
		delivery.ConnectionRevision != connection.Revision || delivery.Profile != connection.Profile ||
		delivery.RequestedBy != h.admin.user.ID || delivery.TriggerKind != "notification-policy" ||
		delivery.PolicyID != firstPolicy.ID || delivery.PolicyRevision != 1 ||
		delivery.FindingChangeRevision != event.ChangeRevision || delivery.DispatchStartedAt != nil ||
		delivery.CompletedAt != nil || delivery.Receipt != nil || delivery.Failure != nil {
		t.Fatalf("policy evaluation did not create one normal immutable queued delivery: %#v", delivery)
	}
	if delivery.Payload.Title != finding.Title ||
		delivery.Payload.Body != "Severity: "+finding.Severity+"\nAsset: "+finding.AssetName+"\nChange: new" ||
		delivery.Payload.DeepLink != "https://aspm.test/#/work?finding="+finding.ID ||
		len(delivery.Payload.Fields) != 0 {
		t.Fatalf("automatic payload exceeded or omitted its bounded server-derived fields: %#v", delivery.Payload)
	}
	for _, forbidden := range []string{
		finding.Evidence.Text, finding.Description, finding.Remediation,
		string(encode(t, finding.Observations[0].Unmapped)),
		h.services.cfg.Storage.AccessKey, h.services.cfg.Storage.SecretKey,
		"<script", "javascript:",
	} {
		if forbidden != "" && bytes.Contains(response.Body.Bytes(), []byte(forbidden)) {
			t.Fatal("automatic payload exposed evidence, notes, remediation, unmapped data, credentials or executable content")
		}
	}
	var key, trigger, policyID string
	var policyRevision, changeRevision int64
	ok(t, "read policy delivery binding", h.services.db.QueryRow(h.services.ctx, `SELECT idempotency_key,trigger_kind,
		policy_id,policy_revision,finding_change_revision FROM `+notificationTable(h, "finding_deliveries")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, deliveryID).
		Scan(&key, &trigger, &policyID, &policyRevision, &changeRevision))
	equal(t, "stable policy idempotency key", key,
		"notification-policy:"+firstPolicy.ID+":"+finding.ID+":"+strconv.FormatInt(event.ChangeRevision, 10))
	equal(t, "stored policy trigger", trigger, "notification-policy")
	equal(t, "stored policy ID", policyID, firstPolicy.ID)
	equal(t, "stored policy revision", policyRevision, int64(1))
	equal(t, "stored finding change revision", changeRevision, event.ChangeRevision)
	equal(t, "evaluated event state", h.changeEvents()[0].State, "evaluated")
	equal(t, "provider remained untouched after reads", tripwire.calls.Load(), int32(0))
	after := h.finding(h.admin, finding.ID)
	if after.WorkflowState != finding.WorkflowState || after.Disposition != finding.Disposition ||
		after.SourceState != finding.SourceState || after.VerifiedResolution != finding.VerifiedResolution ||
		after.DecisionRevision != finding.DecisionRevision || after.EvidenceRevision != finding.EvidenceRevision ||
		!reflect.DeepEqual(after.Notes, finding.Notes) {
		t.Fatal("automatic policy evaluation changed finding workflow, risk, source or verification state")
	}
}

func TestM08_PolicyEvaluationRecordsStaleAndSkipsNonmatches(t *testing.T) {
	h := newNotificationHarness(t)
	connection := h.createSlackConnection("Stale policy destination", "G987", true)
	matching := h.createPolicy(policyInput("Matching stale policy", connection.ID, true,
		[]string{"new"}, "medium", "This policy matches before the connection changes."))
	severityMiss := h.createPolicy(policyInput("Critical only", connection.ID, true,
		[]string{"new"}, "critical", "Medium events must not match."))
	kindMiss := h.createPolicy(policyInput("Reopened only", connection.ID, true,
		[]string{"reopened"}, "info", "New events must not match."))
	asset := h.asset(h.admin, "Stale policy repository", &h.admin.user.ID)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["scanId"] = "stale-policy-new"
	h.finish(h.upload(input).ID, "succeeded")
	finding := h.finding(h.admin, h.work(h.admin, "")[0].ID)
	event := h.changeEvents()[0]
	rotated := h.notificationJSON(h.admin, "PATCH", "/api/v1/integrations/connections/"+connection.ID,
		object{"name": "Changed after event"}, 200).Connection
	if rotated.Revision == connection.Revision {
		t.Fatal("fixture did not advance the selected connection revision")
	}
	tripwire := &providerTripwire{}
	worker := h.openPolicyWorker(t, tripwire)
	processed, err := worker.ProcessNext(h.services.ctx)
	ok(t, "evaluate stale policy event", err)
	if !processed || tripwire.calls.Load() != 0 {
		t.Fatal("stale policy evaluation performed provider I/O or found no durable event")
	}
	secondAsset := h.asset(h.admin, "Second stale policy repository", &h.admin.user.ID)
	secondInput := h.input(secondAsset.ID, "sarif", fixture(t, "sarif.json"))
	secondInput["scanId"], secondInput["sourceId"] = "stale-policy-second-new", "stale-policy-second-source"
	h.finish(h.upload(secondInput).ID, "succeeded")
	processed, err = worker.ProcessNext(h.services.ctx)
	ok(t, "evaluate second stale policy event", err)
	if !processed || tripwire.calls.Load() != 0 {
		t.Fatal("second stale policy evaluation performed provider I/O or found no durable event")
	}
	history := h.policyEvents(h.admin, matching.ID)
	if len(history) != 2 {
		t.Fatalf("stale policy history omitted an evaluated event: %#v", history)
	}
	for _, outcome := range history {
		if outcome.Outcome != "connection-stale" || outcome.DeliveryID != nil {
			t.Fatalf("stale connection did not produce immutable no-delivery outcomes: %#v", history)
		}
	}
	firstFound := false
	for _, outcome := range history {
		firstFound = firstFound || outcome.FindingID == finding.ID &&
			outcome.FindingChangeRevision == event.ChangeRevision
	}
	if !firstFound {
		t.Fatalf("stale connection did not produce one immutable no-delivery outcome: %#v", history)
	}
	firstPage := h.notificationJSON(h.admin, "GET",
		notificationPoliciesPath+"/"+matching.ID+"/events?limit=1", nil, 200)
	if firstPage.Total != 2 || len(firstPage.Items) != 1 || firstPage.NextCursor == nil {
		t.Fatal("first policy-event page omitted its stable total or continuation")
	}
	firstEvent := decodePolicyEvents(t, firstPage)[0]
	equal(t, "policy event cursor", *firstPage.NextCursor, firstEvent.ID)
	secondPage := h.notificationJSON(h.admin, "GET",
		notificationPoliciesPath+"/"+matching.ID+"/events?limit=1&cursor="+firstEvent.ID, nil, 200)
	secondEvents := decodePolicyEvents(t, secondPage)
	if secondPage.Total != 2 || len(secondEvents) != 1 || secondPage.NextCursor != nil ||
		secondEvents[0].ID <= firstEvent.ID {
		t.Fatal("policy-event history is not an ascending immutable-ID page")
	}
	equal(t, "severity nonmatch creates no outcome", len(h.policyEvents(h.admin, severityMiss.ID)), 0)
	equal(t, "change-kind nonmatch creates no outcome", len(h.policyEvents(h.admin, kindMiss.ID)), 0)
	var count int
	ok(t, "count stale policy deliveries", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+notificationTable(h, "finding_deliveries")+`
		WHERE workspace_id=$1 AND trigger_kind='notification-policy'`, h.admin.workspace).Scan(&count))
	equal(t, "stale policy creates no outbox row", count, 0)
}
