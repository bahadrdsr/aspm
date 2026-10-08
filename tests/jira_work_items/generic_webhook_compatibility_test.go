//go:build integration

package jira_work_items

import (
	"testing"
	"time"
)

func TestJiraV22KeepsManualEffectAndNotificationPolicyJiraOnly(t *testing.T) {
	h, native := newHarness(t), newJira(t)
	finding := h.seed(h.admin, "V22 Jira compatibility finding")
	connection := h.connection(h.admin, native.target(), secret(t))
	preview := h.preview(h.admin, finding.ID, connection)
	manual := h.queue(h.admin, preview, "v22-jira-manual", 202)
	policy := h.createJiraNotificationPolicy(connection)
	changed := h.changedJiraScan(h.admin, finding, time.Now().UTC().Add(time.Minute))

	var connectionWebhook, deliveryWebhook any
	must(t, "read V22 Jira null target compatibility", h.db.QueryRow(h.ctx,
		"SELECT c.webhook_target,d.webhook_target FROM "+h.table("integration_connections")+" c JOIN "+
			h.table("finding_deliveries")+" d ON d.workspace_id=c.workspace_id AND d.connection_id=c.id "+
			"WHERE c.workspace_id=$1 AND c.id=$2 AND d.id=$3",
		h.admin.Workspace, connection.ID, manual.ID).Scan(&connectionWebhook, &deliveryWebhook))
	check(t, connectionWebhook == nil && deliveryWebhook == nil,
		"V22 attached generic webhook metadata to Jira rows")

	_, err := h.db.Exec(h.ctx, "UPDATE "+h.table("finding_deliveries")+`
		SET state='failed',completed_at=clock_timestamp(),
			failure='{"code":"synthetic-terminal","nativeCode":"","httpStatus":0,
				"retryAfterSeconds":0,"retryable":false}'::jsonb
		WHERE workspace_id=$1 AND id=$2 AND state='queued'`, h.admin.Workspace, manual.ID)
	must(t, "settle only the owned Jira compatibility intent", err)
	worker := h.worker(native, "v22-jira-policy", 4*time.Second)
	process(t, h.ctx, worker, true)
	events := h.notificationEvents(h.admin, policy.ID)
	if len(events) != 1 || events[0].Outcome != "duplicate-ticket" ||
		events[0].DeliveryID == nil || *events[0].DeliveryID != manual.ID ||
		events[0].FindingChangeRevision != changed.ChangeRevision {
		t.Fatalf("V22 changed Jira notification-policy effect semantics: %#v", events)
	}
	same(t, "V22 changed the shared Jira finding effect",
		h.rows("SELECT delivery_id FROM "+h.table("jira_finding_effects")+
			" WHERE workspace_id=$1 AND connection_id=$2 AND finding_id=$3",
			h.admin.Workspace, connection.ID, finding.ID), []string{manual.ID})
	check(t, native.calls.Load() == 0,
		"V22 Jira policy evaluation performed provider I/O or fell through to generic webhook transport")
}
