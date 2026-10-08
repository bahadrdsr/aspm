//go:build integration

package remediation

import (
	"reflect"
	"testing"
	"time"
)

func TestRemediationV22KeepsSlackDTOQueueAndWorkerSemanticsByteCompatible(t *testing.T) {
	h, native := newHarness(t, true), newSlack(t)
	finding := h.importFinding(h.admin, "V22 native Slack compatibility")
	token := secret(t)
	connection := h.createConnection(h.admin, "V22 unchanged Slack destination", "C123", token, true)
	queued := h.enqueue(h.admin, finding.ID, connection, "v22-native-slack", 202)
	var connectionWebhook, deliveryWebhook any
	must(t, "read V22 native null target compatibility", h.db.QueryRow(h.ctx,
		"SELECT c.webhook_target,d.webhook_target FROM "+h.table("integration_connections")+" c JOIN "+
			h.table("finding_deliveries")+" d ON d.workspace_id=c.workspace_id AND d.connection_id=c.id "+
			"WHERE c.workspace_id=$1 AND c.id=$2 AND d.id=$3",
		h.admin.Workspace, connection.ID, queued.ID).Scan(&connectionWebhook, &deliveryWebhook))
	check(t, connectionWebhook == nil && deliveryWebhook == nil,
		"V22 attached generic webhook metadata to an existing Slack connection or delivery")

	body := h.request(h.ctx, h.admin, "GET", connectionsPath+"/"+connection.ID, nil, 200).Body.Bytes()
	deliveryBody := h.request(h.ctx, h.admin, "GET", deliveryPath(queued.ID), nil, 200).Body.Bytes()
	check(t, !contains(body, "webhook") && !contains(deliveryBody, "webhook"),
		"V22 changed native Slack response bytes by adding webhook-only fields")

	plan := native.plan(token, connection.Channel, "ok", func() error { return h.marker(queued.ID) })
	worker := h.worker(native, "v22-native-slack", 4*time.Second)
	process(t, h.ctx, worker, true)
	assertSlack(t, awaitCall(t, plan), connection.Channel, queued.Payload, h.reportText)
	confirmed := h.delivery(h.admin, queued.ID)
	check(t, confirmed.State == "confirmed" && confirmed.Receipt != nil &&
		reflect.DeepEqual(confirmed.Payload, queued.Payload),
		"V22 changed native Slack dispatch, receipt or immutable payload semantics")
	catalog := h.json(h.admin, "GET", "/api/v1/integrations/catalog", nil, 200)
	check(t, len(catalog.Items) == 8, "V22 generic webhook changed the exact native catalog count")
}

func contains(data []byte, value string) bool {
	for index := 0; index+len(value) <= len(data); index++ {
		if string(data[index:index+len(value)]) == value {
			return true
		}
	}
	return false
}
