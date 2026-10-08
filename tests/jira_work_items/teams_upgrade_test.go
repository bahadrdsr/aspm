//go:build integration && teams_workflows

package jira_work_items

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/tests/internal/deliverycompat"
	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

func (h *harness) teamsV10Data(names []string) map[string][]string {
	h.t.Helper()
	result := map[string][]string{}
	for _, name := range names {
		projection := "to_jsonb(v)"
		if name == "integration_connections" || name == "finding_deliveries" {
			same(h.t, "legacy rows acquired Teams metadata",
				h.rows("SELECT id FROM "+h.table(name)+" WHERE teams_target IS NOT NULL"), []string{})
			projection += "-'teams_target'"
		}
		result[name] = h.rows("SELECT (" + projection + ")::text FROM " + h.table(name) + " v ORDER BY (" + projection + ")::text")
	}
	return result
}

func TestTeamsT4ActualPublishedV10PreservationAndV11Reopen(t *testing.T) {
	h, n := &harness{fixture: newFixture(t)}, newTeamsServer(t)
	same(t, "published V10 must start in an empty owned schema", h.relations(), []string{})
	build := h.openTeamsPublished()
	h.enroll()
	viewer := h.user("viewer")
	f := h.seed(h.admin, "Published V10 Teams migration")
	jiraToken := secret(t)
	jira := h.connection(h.admin, n.common.target(), jiraToken)
	jiraPreview := h.preview(h.admin, f.ID, jira)
	h.assertEncrypted(jira, jiraToken)
	confirmedJob := h.queue(h.admin, jiraPreview, "published-confirmed-jira", 202)
	oldNative := n.common.arm(jiraToken, "ok", "ok", func() error { return h.marker(confirmedJob.ID) })
	h.publishedTeamsBaselineWorker(build, n.common)
	assertADF(t, awaitCall(t, oldNative.postArrived), confirmedJob.Payload)
	oldConfirmed := h.delivery(viewer, confirmedJob.ID)
	check(t, oldConfirmed.State == "confirmed" && oldConfirmed.Receipt != nil &&
		oldConfirmed.Receipt.RemoteID == "SYN-42" && oldConfirmed.CreateAttemptedAt != nil,
		"published V10 did not create an actual native receipt and committed marker")
	oldConfirmedRow := h.snapshotJob(confirmedJob.ID)
	slackToken := secret(t)
	h.remember(slackToken)
	slack := h.json(h.admin, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Published V10 Slack", "channel": "C123",
		"enabled": true, "token": slackToken,
	}, 201).Connection
	slackInput := object{"connectionId": slack.ID, "idempotencyKey": "published-queued-slack"}
	slackJob := h.json(h.admin, "POST", historyPath(f.ID), slackInput, 202).Delivery
	jiraJob := h.queue(h.admin, jiraPreview, "published-queued-jira", 202)
	view := h.json(viewer, "POST", "/api/v1/work/views", object{
		"name": "Published V10 view", "query": "Published V10", "sort": "severity",
	}, 201).View
	providerKey := secret(t)
	h.remember(providerKey)
	profile := h.json(h.admin, "POST", "/api/v1/ai/profiles", object{
		"name": "Owned published advisory", "family": "openai", "endpoint": n.common.trap.URL + "/v1",
		"model": "operator-reviewed-model", "enabled": true, "structuredOutput": true, "apiKey": providerKey,
	}, 201).Profile
	policy := h.json(h.admin, "PATCH", "/api/v1/ai/policy", object{"mode": "approved-hosted"}, 200).Policy
	grant := h.json(h.admin, "POST", "/api/v1/ai/grants", object{
		"profileId": profile.ID, "profileRevision": profile.Revision, "policyRevision": policy.Revision,
		"destination": profile.Endpoint, "task": "finding-validity", "dataClass": "finding-evidence",
		"expiresAt": time.Now().UTC().Add(time.Hour),
	}, 201).Grant
	body, _ := h.request(h.ctx, h.admin, "POST", "/api/v1/findings/"+f.ID+"/assessment-previews", object{
		"observationId": f.Observations[0].ID, "profileId": profile.ID, "grantId": grant.ID,
		"context": "Owned historical assessment context only.", "reviewed": true,
	}, 201)
	assessmentPreview := decoded[object](t, body)["preview"].(map[string]any)["id"].(string)
	assessment := h.json(h.admin, "POST", "/api/v1/findings/"+f.ID+"/assessments", object{
		"previewId": assessmentPreview, "idempotencyKey": "published-v10-assessment", "consent": true,
	}, 202).Assessment

	resources := []struct {
		method, path string
		input        any
		before       object
	}{
		{"GET", connectionsPath + "/" + slack.ID, nil, nil},
		{"GET", connectionsPath + "/" + jira.ID, nil, nil},
		{"GET", deliveryPath(confirmedJob.ID), nil, nil},
		{"GET", deliveryPath(slackJob.ID), nil, nil},
		{"GET", deliveryPath(jiraJob.ID), nil, nil},
		{"GET", connectionsPath, nil, nil},
		{"GET", jiraListPath(connectionsPath), nil, nil},
		{"POST", previewPath(f.ID), object{"connectionId": jira.ID}, nil},
		{"POST", historyPath(f.ID), slackInput, nil},
		{"POST", historyPath(f.ID), queueInput(jiraPreview, "published-confirmed-jira"), nil},
	}
	for i := range resources {
		r := &resources[i]
		body, _ := h.request(h.ctx, h.admin, r.method, r.path, r.input, 200)
		r.before = decoded[object](t, body)
	}
	must(t, "close exact published V10 before migration", h.closeCore())
	oldLedger := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	same(t, "published executable did not create the authentic V10 ledger", h.ledger(), oldLedger)
	names := h.rows(`SELECT substr(c.relname,5) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind='r' AND c.relname<>'app_schema_versions' ORDER BY c.relname`, h.cfg.Schema)
	before, definitions, relations := h.snapshot(names), h.definitions(names), h.relations()
	for _, table := range []string{"users", "sessions", "memberships", "assets", "imports", "findings", "observations", "notes",
		"integration_connections", "finding_deliveries", "work_views", "ai_profiles", "ai_policies", "ai_egress_grants",
		"assessment_previews", "assessment_jobs"} {
		check(t, len(before[table]) > 0, "historical business data was not actually API-created by published V10")
	}
	check(t, n.calls.Load() == 0 && n.common.calls.Load() == 2 && n.common.slackPosts.Load() == 0,
		"published setup made an undeclared native call")
	same(t, "historical migration literal changed inside an otherwise mutable producer file",
		teamsHistoricalMigrations(t, filepath.Join("..", "..")),
		teamsHistoricalMigrations(t, filepath.Dir(build.Executable)))
	h.open()
	currentLedger := append(append([]string{}, oldLedger...), "11", "12", "13", "14", "15", "16", "17", "18", "19", "20")
	same(t, "current Open did not add V11/V12/V13/V14/V15/V16/V17/V18/V19 exactly once over real populated V10", h.ledger(), currentLedger)
	same(t, "V20 changed the relation/index set",
		sourcecompat.ProjectRelationsV20(t, relations, h.relations()), relations)
	same(t, "V20 must validate the complete exact DDL delta before projection",
		deliverycompat.ProjectCurrentV10(t, definitions, h.definitions(names)), definitions)
	same(t, "V20 changed old bytes outside the exact additions",
		sourcecompat.ProjectRowsCurrent(t, before, h.teamsV10Data(names)), before)
	for _, r := range resources {
		body, _ := h.request(h.ctx, h.admin, r.method, r.path, r.input, 200)
		same(t, "V11 changed published Slack/Jira DTO/queue/preview/digest bytes", decoded[object](t, body), r.before)
	}
	assertFindingUnchanged(t, f, h.finding(viewer, f.ID))
	viewID := decoded[struct{ ID string }](t, view).ID
	same(t, "V11 changed the published saved preference", h.json(viewer, "GET", "/api/v1/work/views/"+viewID, nil, 200).View, view)
	assessmentID := decoded[struct{ ID string }](t, assessment).ID
	same(t, "V11 changed the published queued advisory", h.json(viewer, "GET", "/api/v1/ai/assessments/"+assessmentID, nil, 200).Assessment, assessment)
	afterDefinitions := h.definitions(names)
	h.reopen()
	same(t, "V20 reopen changed the integer ledger", h.ledger(), currentLedger)
	same(t, "V20 reopen changed definitions", h.definitions(names), afterDefinitions)
	same(t, "V20 reopen changed historical business bytes",
		sourcecompat.ProjectRowsCurrent(t, before, h.teamsV10Data(names)), before)

	value := n.workflowURL()
	c := h.teamsConnection(h.admin, value)
	job := h.teamsQueue(h.admin, h.teamsPreview(h.admin, f.ID, c), "teams-after-v10", 202)
	for _, row := range []ownedRow{
		{"integration_connections", h.admin.Workspace, slack.ID}, {"finding_deliveries", h.admin.Workspace, slackJob.ID},
		{"integration_connections", h.admin.Workspace, jira.ID}, {"finding_deliveries", h.admin.Workspace, confirmedJob.ID},
		{"integration_connections", h.admin.Workspace, c.ID}, {"finding_deliveries", h.admin.Workspace, job.ID},
	} {
		h.mutateOwned(row, "profile=profile,channel=channel,jira_target=jira_target,teams_target=teams_target", nil, false, false)
	}
	for _, row := range []ownedRow{
		{"integration_connections", h.admin.Workspace, c.ID}, {"finding_deliveries", h.admin.Workspace, job.ID},
	} {
		for _, assignment := range []string{
			"teams_target=NULL", "teams_target='null'::jsonb", "teams_target='[]'::jsonb",
			"channel='C123'", "jira_target='{}'::jsonb", "profile='unknown-profile'",
			"profile='slack-workspace-bot',channel='C123'", "profile='jira-cloud-v3',jira_target='{}'::jsonb",
		} {
			h.mutateOwned(row, assignment, nil, true, false)
		}
	}
	h.mutateOwned(ownedRow{"integration_connections", h.admin.Workspace, slack.ID}, "teams_target='{}'::jsonb", nil, true, false)
	h.mutateOwned(ownedRow{"finding_deliveries", h.admin.Workspace, confirmedJob.ID}, "teams_target='{}'::jsonb", nil, true, false)
	slackArrival := n.common.allowSlack(slackToken, "C123")
	jiraArrival := n.common.arm(jiraToken, "ok", "ok", func() error { return h.marker(jiraJob.ID) })
	w := h.worker(n.common, "upgraded-three-profile-worker", 4*time.Second)
	process(t, h.ctx, w, true)
	slackCall := awaitCall(t, slackArrival)
	check(t, slackCall.Body["text"] == slackJob.Payload.Title+"\n"+slackJob.Payload.Body+"\n"+slackJob.Payload.DeepLink &&
		slackCall.Body["parse"] == "none" && slackCall.Body["unfurl_links"] == false && slackCall.Body["unfurl_media"] == false,
		"current worker changed legacy Slack payload behavior")
	slackDone := h.delivery(viewer, slackJob.ID)
	check(t, slackDone.State == "confirmed" && slackDone.Receipt != nil && slackDone.Receipt.RemoteID == "C123:1789560000.123456",
		"current worker could not use the preserved Slack credential/intent")
	process(t, h.ctx, w, true)
	assertADF(t, awaitCall(t, jiraArrival.postArrived), jiraJob.Payload)
	jiraDone := h.delivery(viewer, jiraJob.ID)
	check(t, jiraDone.State == "confirmed" && jiraDone.Receipt != nil &&
		jiraDone.Receipt.RemoteURL == siteOrigin+"/browse/SYN-42", "current worker changed legacy Jira receipt semantics")
	p := n.arm(value, "accepted", func() error { return h.marker(job.ID) })
	process(t, h.ctx, w, true)
	assertTeamsCard(t, awaitCall(t, p.arrived), job.Payload)
	accepted := h.teamsDelivery(viewer, job.ID)
	check(t, accepted.State == "accepted" && accepted.Receipt == nil && accepted.OutboundAttemptedAt != nil,
		"new Teams flow is not usable through the migrated common worker")
	process(t, h.ctx, w, false)
	same(t, "typed probes or new dispatch rewrote the published native receipt",
		h.rows("SELECT (to_jsonb(v)-'teams_target')::text FROM "+h.table("finding_deliveries")+
			" v WHERE workspace_id=$1 AND id=$2", h.admin.Workspace, confirmedJob.ID), oldConfirmedRow)
	same(t, "migration/new profile changed historical connection bytes",
		h.rows("SELECT (to_jsonb(v)-'teams_target')::text FROM "+h.table("integration_connections")+
			" v WHERE id=ANY($1::text[]) ORDER BY (to_jsonb(v)-'teams_target')::text", []string{slack.ID, jira.ID}),
		before["integration_connections"])
	unrelated := []string{}
	for _, name := range names {
		if name != "integration_connections" && name != "finding_deliveries" {
			unrelated = append(unrelated, name)
		}
	}
	unrelatedBefore := map[string][]string{}
	for _, name := range unrelated {
		unrelatedBefore[name] = before[name]
	}
	for name, rows := range sourcecompat.ProjectRowsCurrent(t, unrelatedBefore, h.snapshot(unrelated)) {
		same(t, "delivery changed unrelated historical business rows in "+name, rows, before[name])
	}
	h.reopen()
	same(t, "accepted Teams history did not survive migrated reopen", h.teamsDelivery(viewer, job.ID), accepted)
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(viewer, f.ID))
}
