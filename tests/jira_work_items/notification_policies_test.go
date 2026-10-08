//go:build integration

package jira_work_items

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

const jiraNotificationPoliciesPath = "/api/v1/integrations/notification-policies"

type jiraNotificationPolicy struct {
	ID, WorkspaceID, ConnectionID, ConnectionProfile string
	Revision, Epoch, ConnectionRevision              int64
}

type jiraNotificationEvent struct {
	ID, PolicyID, FindingID, Outcome      string
	PolicyRevision, FindingChangeRevision int64
	DeliveryID                            *string
}

type jiraNotificationReply struct {
	APIVersion string
	Policy     jiraNotificationPolicy
	Items      []json.RawMessage
	Total      int
	NextCursor *string
	Error      *app.Failure
}

func (h *harness) notificationRequest(who actor, method, path string, input any, status int) jiraNotificationReply {
	h.t.Helper()
	body, _ := h.request(h.ctx, who, method, path, input, status)
	value := decoded[jiraNotificationReply](h.t, body)
	check(h.t, value.APIVersion == app.APIVersion, "notification policy response lost the API version")
	if status >= 400 {
		check(h.t, value.Error != nil && value.Error.Code != "" && !value.Error.Retryable,
			"notification policy denial is not explicit and nonretryable")
	}
	return value
}

func (h *harness) createJiraNotificationPolicy(connection connection) jiraNotificationPolicy {
	h.t.Helper()
	policy := h.notificationRequest(h.admin, "POST", jiraNotificationPoliciesPath, object{
		"name": "Owned Jira changed findings", "connectionId": connection.ID, "enabled": true,
		"changeKinds": []string{"changed"}, "minimumSeverity": "medium",
		"rationale": "Approve one Jira intent for each changed canonical finding.",
	}, 201).Policy
	check(h.t, policy.ID != "" && policy.WorkspaceID == h.admin.Workspace &&
		policy.ConnectionID == connection.ID && policy.ConnectionProfile == jiraProfile &&
		policy.ConnectionRevision == connection.Revision && policy.Revision == 1 && policy.Epoch > 0,
		"Jira policy did not snapshot its approved native connection")
	return policy
}

func (h *harness) notificationEvents(who actor, policyID string) []jiraNotificationEvent {
	h.t.Helper()
	reply := h.notificationRequest(who, "GET",
		jiraNotificationPoliciesPath+"/"+policyID+"/events?limit=100", nil, 200)
	check(h.t, reply.Total == len(reply.Items) && reply.NextCursor == nil,
		"small Jira notification event history must be complete")
	result := make([]jiraNotificationEvent, 0, len(reply.Items))
	for _, raw := range reply.Items {
		result = append(result, decoded[jiraNotificationEvent](h.t, raw))
	}
	return result
}

func (h *harness) changedJiraScan(who actor, finding app.Finding, at time.Time) app.Finding {
	h.t.Helper()
	source := object{"version": "2.1.0", "runs": []any{object{
		"tool": object{"driver": object{"name": "Owned Jira SARIF", "rules": []any{object{
			"id": "JIRA-SYN-1", "shortDescription": object{"text": finding.Title},
			"fullDescription": object{"text": "CHANGED-SOURCE-DESCRIPTION-NOT-APPROVED"},
		}}}},
		"results": []any{object{
			"ruleId": "JIRA-SYN-1", "level": "warning",
			"message": object{"text": "Changed canonical Jira duplicate-prevention evidence."},
			"properties": object{
				"endpoint":      "https://changed-not-approved.invalid",
				"approvalRef":   "changed-scanner-not-approval",
				"Authorization": "changed-scanner-not-a-credential",
			},
		}},
	}}}
	queued := h.json(who, "POST", "/api/v1/imports", object{
		"apiVersion": app.APIVersion, "assetId": finding.AssetID, "format": "sarif",
		"report": string(encoded(h.t, source)), "sourceId": "owned-jira-source", "scanId": nonce(h.t),
		"scope":        object{"id": finding.AssetID, "revision": "1", "branch": "main"},
		"sourceScanAt": at, "collectedAt": at.Add(time.Second),
		"sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete",
	}, 202).Import
	must(h.t, "process authoritative changed Jira scan", h.imports(h.ctx))
	done := h.json(who, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import
	check(h.t, done.State == "succeeded" && done.ObservationCount == 1,
		"changed Jira scan did not finish authoritatively")
	return h.finding(who, finding.ID)
}

func TestJiraV21ManualEffectKeyPreventsSecondTicketWithoutChangingOtherProfiles(t *testing.T) {
	h, native := newHarness(t), newJira(t)
	finding := h.seed(h.admin, "Jira manual duplicate prevention")
	connection := h.connection(h.admin, native.target(), secret(t))
	preview := h.preview(h.admin, finding.ID, connection)
	first := h.queue(h.admin, preview, "jira-effect-first", 202)
	replay := h.queue(h.admin, preview, "jira-effect-first", 200)
	same(t, "same Jira manual idempotency key did not return the original delivery", replay, first)
	h.request(h.ctx, h.admin, "POST", historyPath(finding.ID),
		queueInput(preview, "jira-effect-second"), 409)
	for _, state := range []string{"dispatching", "confirmed", "uncertain", "blocked", "failed", "rate-limited"} {
		if state == "dispatching" {
			_, err := h.db.Exec(h.ctx, "UPDATE "+h.table("finding_deliveries")+`
				SET state='dispatching',dispatch_started_at=clock_timestamp(),completed_at=NULL,
					worker_id='owned-v21-state',fence=fence+1,
					lease_until=clock_timestamp()+interval '1 minute',receipt=NULL,failure=NULL
				WHERE workspace_id=$1 AND id=$2`, h.admin.Workspace, first.ID)
			must(t, "set owned prior Jira dispatching intent", err)
		} else {
			_, err := h.db.Exec(h.ctx, "UPDATE "+h.table("finding_deliveries")+`
				SET state=$3,dispatch_started_at=COALESCE(dispatch_started_at,clock_timestamp()),
					completed_at=clock_timestamp(),worker_id=NULL,lease_until=NULL,receipt=NULL,failure=NULL
				WHERE workspace_id=$1 AND id=$2`, h.admin.Workspace, first.ID, state)
			must(t, "set owned prior Jira terminal intent", err)
		}
		h.request(h.ctx, h.admin, "POST", historyPath(finding.ID),
			queueInput(preview, "jira-effect-state-"+state), 409)
	}
	rows := h.rows("SELECT id FROM "+h.table("finding_deliveries")+
		" WHERE workspace_id=$1 AND connection_id=$2 AND finding_id=$3 ORDER BY id",
		h.admin.Workspace, connection.ID, finding.ID)
	same(t, "different Jira manual key created a second outbox row", rows, []string{first.ID})
	effects := h.rows("SELECT connection_id||'|'||finding_id||'|'||delivery_id FROM "+
		h.table("jira_finding_effects")+" WHERE workspace_id=$1", h.admin.Workspace)
	same(t, "manual Jira intent did not reserve the shared durable effect key", effects,
		[]string{connection.ID + "|" + finding.ID + "|" + first.ID})

	otherConnection := h.connection(h.admin, native.target(), secret(t))
	other := h.queue(h.admin, h.preview(h.admin, finding.ID, otherConnection), "jira-effect-other-connection", 202)
	check(t, other.ID != first.ID, "Jira effect key incorrectly crossed native connections")
	_, firstSlack := h.slackSelection(h.admin, finding.ID, "jira-effect-slack-one")
	_, secondSlack := h.slackSelection(h.admin, finding.ID, "jira-effect-slack-two")
	check(t, firstSlack["id"] != secondSlack["id"], "V21 changed Slack manual idempotency behavior")
}

func TestJiraV21PolicyEvaluationRecordsDuplicateAgainstPriorIntent(t *testing.T) {
	h, native := newHarness(t), newJira(t)
	finding := h.seed(h.admin, "Jira automatic duplicate prevention")
	same(t, "pre-policy authoritative import created a policy event",
		h.rows("SELECT change_kind FROM "+h.table("finding_change_events")+
			" WHERE workspace_id=$1 ORDER BY change_revision,finding_id,id", h.admin.Workspace), []string{})
	connection := h.connection(h.admin, native.target(), secret(t))
	manual := h.queue(h.admin, h.preview(h.admin, finding.ID, connection), "jira-prior-terminal", 202)
	_, err := h.db.Exec(h.ctx, "UPDATE "+h.table("finding_deliveries")+`
		SET state='failed',completed_at=clock_timestamp(),
			failure='{"code":"synthetic-terminal","nativeCode":"","httpStatus":0,
				"retryAfterSeconds":0,"retryable":false}'::jsonb
		WHERE workspace_id=$1 AND id=$2 AND state='queued'`, h.admin.Workspace, manual.ID)
	must(t, "settle only the owned prior Jira intent for duplicate evaluation", err)
	policy := h.createJiraNotificationPolicy(connection)
	changed := h.changedJiraScan(h.admin, finding, time.Now().UTC().Add(time.Minute))
	check(t, changed.ChangeKind == "changed" && changed.ChangeRevision > finding.ChangeRevision,
		"fixture did not create one authoritative changed finding revision")
	same(t, "policy-approved changed scan did not create the only evaluable event",
		h.rows("SELECT change_kind FROM "+h.table("finding_change_events")+
			" WHERE workspace_id=$1 ORDER BY change_revision,finding_id,id", h.admin.Workspace),
		[]string{"changed"})

	worker := h.worker(native, "jira-policy-duplicate", 4*time.Second)
	process(t, h.ctx, worker, true)
	process(t, h.ctx, worker, false)
	check(t, native.calls.Load() == 0 && native.slackPosts.Load() == 0,
		"policy evaluation or duplicate prevention performed provider I/O")
	events := h.notificationEvents(h.admin, policy.ID)
	if len(events) != 1 || events[0].Outcome != "duplicate-ticket" ||
		events[0].PolicyID != policy.ID || events[0].PolicyRevision != 1 ||
		events[0].FindingID != finding.ID || events[0].FindingChangeRevision != changed.ChangeRevision ||
		events[0].DeliveryID == nil || *events[0].DeliveryID != manual.ID {
		t.Fatalf("prior Jira intent did not produce the exact immutable duplicate outcome: %#v", events)
	}
	rows := h.rows("SELECT id FROM "+h.table("finding_deliveries")+
		" WHERE workspace_id=$1 AND connection_id=$2 AND finding_id=$3 ORDER BY id",
		h.admin.Workspace, connection.ID, finding.ID)
	same(t, "automatic duplicate prevention created another Jira outbox row", rows, []string{manual.ID})
	effects := h.rows("SELECT delivery_id FROM "+h.table("jira_finding_effects")+
		" WHERE workspace_id=$1 AND connection_id=$2 AND finding_id=$3",
		h.admin.Workspace, connection.ID, finding.ID)
	same(t, "automatic duplicate evaluation changed the serialized Jira effect", effects, []string{manual.ID})

	var trigger, policyID *string
	var policyRevision, changeRevision *int64
	must(t, "read unchanged prior manual delivery trigger", h.db.QueryRow(h.ctx, "SELECT trigger_kind,policy_id,"+
		"policy_revision,finding_change_revision FROM "+h.table("finding_deliveries")+
		" WHERE workspace_id=$1 AND id=$2", h.admin.Workspace, manual.ID).
		Scan(&trigger, &policyID, &policyRevision, &changeRevision))
	check(t, trigger != nil && *trigger == "manual" && policyID == nil && policyRevision == nil && changeRevision == nil,
		"V21 rewrote a prior manual Jira delivery as an automatic intent")
}
