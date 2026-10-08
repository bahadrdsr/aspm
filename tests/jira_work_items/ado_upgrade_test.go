//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

func TestADOA5ActualPublishedV11ToAdditiveV12(t *testing.T) {
	h := newADO(t, false)
	same(t, "published V11 must start with an empty owned schema", h.relations(), []string{})
	build := h.adoOpenPublished()
	h.enroll()
	viewer := h.user("viewer")
	finding := h.seed(h.admin, "Published V11 ADO upgrade")

	githubToken := secret(t)
	h.native.allowGitHub(githubToken)
	github := h.adoJSON(h.admin, "POST", adoSources, object{
		"profile": "github-cloud-app", "name": "Published selected GitHub", "repository": "owned/legacy",
		"enabled": true, "token": githubToken,
	}, 201).Source
	h.adoEncrypted(github, githubToken)
	complete := h.adoJSON(h.admin, "POST", adoSources+"/"+github.ID+"/collections",
		object{"idempotencyKey": "published-completed-github"}, 202).Collection
	must(t, "run the actual published GitHub collection worker", build.control("collect"))
	complete = h.adoJob(viewer, complete.ID)
	check(t, complete.State == "succeeded" && complete.Complete && complete.RecordCount == 1 &&
		complete.AssetID != nil && h.native.calls.Load() == 2, "published V11 did not create actual durable GitHub evidence")
	oldRecords := h.adoRecords(viewer, complete.ID)
	check(t, len(oldRecords) == 1 && oldRecords[0].Kind == "repository", "published V11 source record missing")
	oldEvidence, _ := h.request(h.ctx, viewer, "GET", adoEvidencePath(complete.ID, oldRecords[0].ID), nil, 200)
	check(t, bytes.Equal(oldEvidence, h.native.githubRepositoryBody), "published evidence did not originate from actual native TLS")
	pending := h.adoJSON(h.admin, "POST", adoSources+"/"+github.ID+"/collections",
		object{"idempotencyKey": "published-queued-github"}, 202).Collection
	legacyQueuePath := adoSources + "/" + github.ID + "/collections"
	same(t, "published queued replay did not preserve the old body", h.adoJSON(h.admin, "POST", legacyQueuePath,
		object{"idempotencyKey": "published-queued-github"}, 200).Collection, pending)

	slackToken, jiraToken, workflowSig := secret(t), secret(t), secret(t)
	for _, value := range []string{slackToken, jiraToken, workflowSig} {
		h.remember(value)
	}
	slack := h.json(h.admin, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Published Slack", "channel": "C123", "enabled": true, "token": slackToken,
	}, 201).Connection
	slackJob := h.json(h.admin, "POST", historyPath(finding.ID), object{
		"connectionId": slack.ID, "idempotencyKey": "published-slack",
	}, 202).Delivery
	jira := h.connection(h.admin, jiraTarget{
		CredentialType: "oauth2-bearer", CloudID: cloudID, APIBase: h.native.server.URL + "/ex/jira/" + cloudID,
		SiteOrigin: siteOrigin, Project: "SYN", IssueType: "10001",
		FieldMappings: map[string]string{"customfield_10010": "asset.name", "customfield_10011": "finding.severity"},
	}, jiraToken)
	jiraJob := h.queue(h.admin, h.preview(h.admin, finding.ID, jira), "published-jira", 202)
	workflowURL := h.native.server.URL + "/owned-standard-workflow?sig=" + workflowSig
	h.remember(workflowURL)
	teamsBody, _ := h.request(h.ctx, h.admin, "POST", connectionsPath, object{
		"profile": "teams-workflows-channel", "name": "Published Teams config only", "enabled": true,
		"workflowUrl": workflowURL, "teams": object{"channelType": "standard", "ownershipAcknowledged": true},
	}, 201)
	teams := decoded[reply](t, teamsBody).Connection
	view := h.json(viewer, "POST", "/api/v1/work/views", object{
		"name": "Published V11 preference", "query": "Published V11", "sort": "severity",
	}, 201).View
	providerKey := secret(t)
	h.remember(providerKey)
	profile := h.json(h.admin, "POST", "/api/v1/ai/profiles", object{
		"name": "Published advisory", "family": "openai", "endpoint": h.native.server.URL + "/v1",
		"model": "operator-reviewed-model", "enabled": true, "structuredOutput": true, "apiKey": providerKey,
	}, 201).Profile
	policy := h.json(h.admin, "PATCH", "/api/v1/ai/policy", object{"mode": "approved-hosted"}, 200).Policy
	grant := h.json(h.admin, "POST", "/api/v1/ai/grants", object{
		"profileId": profile.ID, "profileRevision": profile.Revision, "policyRevision": policy.Revision,
		"destination": profile.Endpoint, "task": "finding-validity", "dataClass": "finding-evidence",
		"expiresAt": time.Now().UTC().Add(time.Hour),
	}, 201).Grant
	previewBody, _ := h.request(h.ctx, h.admin, "POST", "/api/v1/findings/"+finding.ID+"/assessment-previews", object{
		"observationId": finding.Observations[0].ID, "profileId": profile.ID, "grantId": grant.ID,
		"context": "Explicitly reviewed published legacy context.", "reviewed": true,
	}, 201)
	preview := decoded[map[string]json.RawMessage](t, previewBody)["preview"]
	previewID := decoded[struct{ ID string }](t, preview).ID
	assessment := h.json(h.admin, "POST", "/api/v1/findings/"+finding.ID+"/assessments", object{
		"previewId": previewID, "idempotencyKey": "published-ai", "consent": true,
	}, 202).Assessment
	viewID, assessmentID := decoded[struct{ ID string }](t, view).ID, decoded[struct{ ID string }](t, assessment).ID

	paths := []string{adoSources, adoSources + "/" + github.ID, adoSources + "/" + github.ID + "/collections",
		adoCollectionPath(complete.ID), adoCollectionPath(pending.ID), adoRecordsPath(complete.ID),
		connectionsPath, connectionsPath + "/" + slack.ID, connectionsPath + "/" + jira.ID, connectionsPath + "/" + teams.ID,
		deliveryPath(slackJob.ID), deliveryPath(jiraJob.ID), "/api/v1/work/views/" + viewID,
		"/api/v1/ai/assessments/" + assessmentID}
	apiBefore := map[string]object{}
	for _, path := range paths {
		data, _ := h.request(h.ctx, viewer, "GET", path, nil, 200)
		apiBefore[path] = decoded[object](t, data)
	}
	check(t, h.native.calls.Load() == 2, "legacy non-source configuration/preview/queue started native calls")
	must(t, "close exact published V11 before current migration", h.closeCore())
	oldLedger := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	same(t, "historical executable did not create authentic V11", h.ledger(), oldLedger)
	names := h.rows(`SELECT substr(c.relname,5) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind='r' AND c.relname<>'app_schema_versions' ORDER BY c.relname`, h.cfg.Schema)
	before, definitions, relations := h.snapshot(names), h.definitions(names), h.relations()
	for _, table := range []string{"users", "sessions", "memberships", "assets", "imports", "findings", "observations", "notes",
		"source_connections", "source_collections", "source_collection_records", "source_repository_assets",
		"integration_connections", "finding_deliveries", "work_views", "ai_profiles", "ai_policies", "ai_egress_grants",
		"assessment_previews", "assessment_jobs"} {
		check(t, len(before[table]) > 0, "claimed historical data was not API/native-worker produced by actual V11")
	}
	same(t, "producer changed an historical V1-V11 SQL literal",
		adoHistoricalMigrations(t, filepath.Join("..", "..")), adoHistoricalMigrations(t, filepath.Dir(build.Executable)))

	h.open()
	currentLedger := append(append([]string{}, oldLedger...), "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25")
	same(t, "current app failed to add exact V12/V13/V14/V15/V16/V17/V18/V19/V20/V21/V22/V23/V24/V25 migrations", h.ledger(), currentLedger)
	sourcecompat.ValidateCurrentCatalog(t, h.v21Definitions())
	same(t, "V25 changed the relation/index set",
		sourcecompat.ProjectRelationsV25(t, relations, h.relations()), relations)
	same(t, "V25 contains a missing or unapproved catalog delta",
		sourcecompat.ProjectCurrent(t, definitions, h.definitions(names)), definitions)
	same(t, "V25 changed complete historical business rows outside exact additions",
		sourcecompat.ProjectRowsCurrent(t, before, h.snapshot(names)), before)
	same(t, "V21 did not backfill the authentic queued Jira effect",
		h.rows("SELECT connection_id||'|'||finding_id||'|'||delivery_id FROM "+h.table("jira_finding_effects")+
			" WHERE workspace_id=$1 ORDER BY connection_id,finding_id", h.admin.Workspace),
		[]string{jira.ID + "|" + finding.ID + "|" + jiraJob.ID})
	for _, path := range paths {
		body, _ := h.request(h.ctx, viewer, "GET", path, nil, 200)
		same(t, "V25 changed published unselected API keys/values", decoded[object](t, body), apiBefore[path])
	}
	h.adoEncrypted(github, githubToken)
	same(t, "current queued GitHub replay changed its historical body/binding", h.adoJSON(h.admin, "POST", legacyQueuePath,
		object{"idempotencyKey": "published-queued-github"}, 200).Collection, pending)
	same(t, "current completed GitHub replay changed its historical body/binding", h.adoJSON(h.admin, "POST", legacyQueuePath,
		object{"idempotencyKey": "published-completed-github"}, 200).Collection, complete)
	h.json(h.admin, "POST", adoBridgePath(complete.ID, oldRecords[0].ID), adoBridgeInput(), 404)
	assertFindingUnchanged(t, finding, h.finding(viewer, finding.ID))
	afterDefinitions := h.definitions(names)
	h.reopen()
	same(t, "reopen repeated/omitted V19", h.ledger(), currentLedger)
	same(t, "reopen changed migrated definitions", h.definitions(names), afterDefinitions)
	sourcecompat.ValidateCurrentCatalog(t, h.v21Definitions())
	same(t, "reopen changed old business rows", sourcecompat.ProjectRowsCurrent(t, before, h.snapshot(names)), before)

	worker := h.adoWorker(h.adoWorkerConfig())
	adoStep(t, h.ctx, worker, true)
	legacyDone := h.adoJob(viewer, pending.ID)
	check(t, legacyDone.State == "succeeded" && legacyDone.Complete && legacyDone.AssetID != nil &&
		*legacyDone.AssetID == *complete.AssetID && h.native.calls.Load() == 4,
		"current worker broke a published queued GitHub source collection")
	data, _ := h.request(h.ctx, viewer, "GET", adoEvidencePath(complete.ID, oldRecords[0].ID), nil, 200)
	check(t, bytes.Equal(data, oldEvidence), "current worker/migration changed historical source evidence")
	same(t, "current worker changed accepted historical collection DTO", h.adoJob(viewer, complete.ID), complete)
	target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
	source := h.adoSource(h.admin, target, token)
	h.native.arm(target, selection, token, adoSARIF(false, false), "", "")
	job := h.adoQueue(h.admin, source.ID, selection, "after-published-upgrade", 202)
	adoStep(t, h.ctx, worker, true)
	check(t, h.adoJob(h.admin, job.ID).State == "succeeded" && h.native.calls.Load() == 8,
		"ADO collection is not usable after authentic V11 upgrade")
	report := h.adoReport(h.admin, job.ID)
	accepted := h.json(h.admin, "POST", adoBridgePath(job.ID, report.ID), adoBridgeInput(), 202).Import
	h.adoIngest()
	check(t, h.json(h.admin, "GET", "/api/v1/imports/"+accepted.ID, nil, 200).Import.State == "succeeded",
		"collected SARIF cannot reach existing ingestion after authentic upgrade")
	h.reopen()
	migratedFinding := h.finding(viewer, finding.ID)
	check(t, migratedFinding.DecisionRevision == 1 && migratedFinding.EvidenceRevision == 1 &&
		migratedFinding.ChangeRevision == 1 && migratedFinding.ChangeKind == "unchanged" &&
		migratedFinding.ChangeAt == nil &&
		migratedFinding.Correlation == nil, "V19 finding defaults or inactive correlation projection changed")
	expectedFinding := finding
	expectedFinding.DecisionRevision, expectedFinding.EvidenceRevision, expectedFinding.ChangeRevision = 1, 1, 1
	expectedFinding.ChangeKind = "unchanged"
	expectedFinding.Observations = append([]app.Observation(nil), finding.Observations...)
	for index := range expectedFinding.Observations {
		expectedFinding.Observations[index].EvidenceAvailability = "available"
		expectedFinding.Observations[index].ChangeKind = "unchanged"
		expectedFinding.Observations[index].ChangeReasons = []string{}
	}
	same(t, "upgrade/new ADO path altered historical human/source finding state", migratedFinding, expectedFinding)
	check(t, h.json(viewer, "GET", "/api/v1/work", nil, 200).Total == 2, "post-upgrade canonical Work lost historical/new finding")
	t.Logf("V11->V22 genuine API/native data preservation; published SQL=%d, parent SQL=%d; S3 traced once by parent object forwarders",
		h.publishedQueries.Load(), h.queries.calls.Load())
}
