//go:build integration

package jira_work_items

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/tests/internal/deliverycompat"
)

const publishedCommit = "4a54db75046f5910705007d800fa34496adc22d8"

type publishedReceipt struct {
	Origin, Executable, ExecutableSHA256, HelperSHA256 string
	BusinessSQLSeeds, ProviderCalls, DataExecution     bool
	ExitCode                                           int
	Files                                              []struct{ Path, SHA256, GitBlob string }
}

func (h *harness) openPublished() {
	h.t.Helper()
	path := os.Getenv("ASPM_JIRA_V9_RECEIPT")
	check(h.t, path != "", "BLOCKED: exact pinned published V9 build receipt required")
	data, err := os.ReadFile(path)
	must(h.t, "read pinned V9 build receipt", err)
	receipt := decoded[publishedReceipt](h.t, data)
	check(h.t, receipt.Origin == publishedCommit && receipt.ExitCode == 0 && len(receipt.Files) > 0 &&
		len(receipt.Files) <= 120 && !receipt.BusinessSQLSeeds && !receipt.ProviderCalls && !receipt.DataExecution,
		"historical fixture is not an exact bounded published production build")
	executable, err := os.ReadFile(receipt.Executable)
	must(h.t, "verify pinned executable", err)
	check(h.t, digest(executable) == receipt.ExecutableSHA256, "published V9 binary drifted")
	helper, err := os.ReadFile(filepath.Join("testdata", "published-core.main.go.txt"))
	must(h.t, "verify HTTP-only published helper", err)
	check(h.t, digest(helper) == receipt.HelperSHA256, "published V9 helper drifted")
	for _, entry := range receipt.Files {
		check(h.t, !filepath.IsAbs(entry.Path) && !strings.Contains(entry.Path, "..") && entry.GitBlob != "",
			"pinned source manifest omitted exact git identity")
		data, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.Executable), entry.Path))
		must(h.t, "verify private pinned source copy", err)
		check(h.t, digest(data) == entry.SHA256, "pinned published source drifted")
	}
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	h.t.Cleanup(cancel)
	command := exec.CommandContext(ctx, receipt.Executable)
	command.Env = safeChildEnvironment()
	command.Stderr = &h.log
	stdin, err := command.StdinPipe()
	must(h.t, "open private published control input", err)
	stdout, err := command.StdoutPipe()
	must(h.t, "open bounded published control output", err)
	must(h.t, "start exact published V9 HTTP core", command.Start())
	done := make(chan error, 1)
	var waitOnce sync.Once
	startWait := func() { waitOnce.Do(func() { go func() { done <- command.Wait() }() }) }
	h.t.Cleanup(func() {
		_ = command.Process.Kill()
		cancel()
		startWait()
	})
	input := struct {
		DatabaseURL, Schema, ApplicationName, BootstrapToken, AssessmentScope string
		StorageEndpoint, Bucket, Prefix, AccessKey, SecretKey                 string
		EncryptionKey                                                         []byte
	}{h.cfg.DatabaseURL, h.cfg.Schema, h.cfg.ApplicationName + "-v9", h.cfg.BootstrapToken, h.cfg.AssessmentScope,
		h.cfg.Storage.Endpoint, h.cfg.Storage.Bucket, h.cfg.Storage.Prefix, h.cfg.Storage.AccessKey,
		h.cfg.Storage.SecretKey, h.cfg.IntegrationEncryptionKey}
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(io.LimitReader(stdout, 16<<10))
	must(h.t, "send private exact published configuration", encoder.Encode(input))
	var ready struct{ Address string }
	must(h.t, "read actual published HTTP readiness", decoder.Decode(&ready))
	check(h.t, strings.HasPrefix(ready.Address, "http://127.0.0.1:"), "published listener was not owned loopback")
	h.base, h.client = ready.Address, directClient(h.t, ready.Address)
	control := func(operation string) error {
		if encoder.Encode(object{"operation": operation}) != nil {
			return errors.New("published private control write failed")
		}
		var result struct {
			OK              bool
			Queries, Writes int32
		}
		if decoder.Decode(&result) != nil || !result.OK {
			return errors.New("published private control outcome failed")
		}
		h.publishedQueries.Store(result.Queries)
		h.publishedWrites.Store(result.Writes)
		return nil
	}
	h.imports = func(context.Context) error { return control("imports") }
	var once sync.Once
	var closeErr error
	h.closeCore = func() error {
		once.Do(func() {
			closeErr = control("close")
			_ = stdin.Close()
			startWait()
			select {
			case err := <-done:
				if closeErr == nil {
					closeErr = err
				}
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				closeErr = errors.New("published shutdown bound exceeded")
			}
			cancel()
		})
		return closeErr
	}
	closePublished := h.closeCore
	h.t.Cleanup(func() { must(h.t, "close exact published HTTP fixture", closePublished()) })
}

func (f *fixture) snapshot(names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		result[name] = f.rows("SELECT to_jsonb(v)::text FROM " + f.table(name) + " v ORDER BY to_jsonb(v)::text")
	}
	return result
}
func (f *fixture) legacyData(names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		projection := "to_jsonb(v)"
		if name == "integration_connections" {
			same(f.t, "old Slack connections acquired Jira targets",
				f.rows("SELECT id FROM "+f.table(name)+" WHERE jira_target IS NOT NULL"), []string{})
			projection += "-'jira_target'"
		}
		if name == "finding_deliveries" {
			same(f.t, "old Slack deliveries acquired Jira targets/attempt markers",
				f.rows("SELECT id FROM "+f.table(name)+" WHERE jira_target IS NOT NULL OR create_attempted_at IS NOT NULL"), []string{})
			projection += "-'jira_target'-'create_attempted_at'"
		}
		result[name] = f.rows("SELECT (" + projection + ")::text FROM " + f.table(name) + " v ORDER BY (" + projection + ")::text")
	}
	return result
}
func (f *fixture) relations() []string {
	return f.rows(`SELECT c.relname||'|'||c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 ORDER BY c.relname`, f.cfg.Schema)
}
func (f *fixture) definitions(names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		result[name+"/columns"] = f.rows(`SELECT a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||
			a.attnotnull::text||'|'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
			FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
			WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, f.table(name))
		result[name+"/constraints"] = f.rows(`SELECT conname||'|'||pg_get_constraintdef(oid,true)
			FROM pg_constraint WHERE conrelid=to_regclass($1) ORDER BY conname`, f.table(name))
		result[name+"/indexes"] = f.rows(`SELECT indexname||'|'||indexdef FROM pg_indexes
			WHERE schemaname=$1 AND tablename=$2 ORDER BY indexname`, f.cfg.Schema, "app_"+name)
	}
	return result
}
func (h *harness) assertTypedRow(row ownedRow, target jiraTarget) {
	h.t.Helper()
	h.mutateOwned(row, "profile=profile,channel=channel,jira_target=jira_target", nil, false, false)
	for _, tuple := range []struct {
		profile, channel string
		target           any
	}{
		{"not-a-delivery-profile", "C123", nil},
		{"slack-workspace-bot", "C123", encoded(h.t, target)},
		{"slack-workspace-bot", "", nil},
		{"jira-cloud-v3", "C123", encoded(h.t, target)},
		{"jira-cloud-v3", "", nil},
		{"jira-cloud-v3", "", []byte(`[]`)},
		{"jira-cloud-v3", "", []byte(`null`)},
	} {
		h.mutateOwned(row, "profile=$3,channel=$4,jira_target=$5::jsonb", []any{tuple.profile, tuple.channel, tuple.target}, true, false)
	}
}

func TestJiraJ5ActualPublishedV9DataToAdditiveV10(t *testing.T) {
	h, n := &harness{fixture: newFixture(t)}, newJira(t)
	same(t, "historical fixture must start with no relations",
		h.rows(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=$1 AND c.relkind='r' ORDER BY c.relname`, h.cfg.Schema), []string{})
	h.openPublished()
	h.enroll()
	viewer := h.user("viewer")
	f := h.seed(h.admin, "Actual published V9 finding")
	slackToken := secret(t)
	h.remember(slackToken)
	slackBody, _ := h.request(h.ctx, h.admin, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Published Slack", "channel": "C123", "enabled": true, "token": slackToken,
	}, 201)
	slackConnection := decoded[reply](t, slackBody).Connection
	slackShape := decoded[object](t, slackBody)["connection"]
	slackJobBody, _ := h.request(h.ctx, h.admin, "POST", historyPath(f.ID), object{
		"connectionId": slackConnection.ID, "idempotencyKey": "published-slack-key",
	}, 202)
	slackJob := decoded[reply](t, slackJobBody).Delivery
	slackJobShape := decoded[object](t, slackJobBody)["delivery"].(map[string]any)
	view := h.json(viewer, "POST", "/api/v1/work/views", object{
		"name": "Published V9 view", "query": "Actual published", "sort": "severity",
	}, 201).View
	providerKey := secret(t)
	h.remember(providerKey)
	profile := h.json(h.admin, "POST", "/api/v1/ai/profiles", object{
		"name": "Published advisory profile", "family": "openai", "endpoint": n.trap.URL + "/v1",
		"model": "operator-reviewed-model", "enabled": true, "structuredOutput": true, "apiKey": providerKey,
	}, 201).Profile
	policy := h.json(h.admin, "PATCH", "/api/v1/ai/policy", object{"mode": "approved-hosted"}, 200).Policy
	grant := h.json(h.admin, "POST", "/api/v1/ai/grants", object{
		"profileId": profile.ID, "profileRevision": profile.Revision, "policyRevision": policy.Revision,
		"destination": profile.Endpoint, "task": "finding-validity", "dataClass": "finding-evidence",
		"expiresAt": time.Now().UTC().Add(time.Hour),
	}, 201).Grant
	previewBody, _ := h.request(h.ctx, h.admin, "POST", "/api/v1/findings/"+f.ID+"/assessment-previews", object{
		"observationId": f.Observations[0].ID, "profileId": profile.ID, "grantId": grant.ID,
		"context": "Explicitly reviewed legacy assessment context.", "reviewed": true,
	}, 201)
	previewID := decoded[object](t, previewBody)["preview"].(map[string]any)["id"].(string)
	assessment := h.json(h.admin, "POST", "/api/v1/findings/"+f.ID+"/assessments", object{
		"previewId": previewID, "idempotencyKey": "published-assessment-key", "consent": true,
	}, 202).Assessment
	must(t, "close actual published V9 before upgrade", h.closeCore())
	oldLedger := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	same(t, "pinned published constructor did not produce true V9", h.ledger(), oldLedger)
	names := h.rows(`SELECT substr(c.relname,5) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind='r' AND c.relname<>'app_schema_versions' ORDER BY c.relname`, h.cfg.Schema)
	before, definitions, relations := h.snapshot(names), h.definitions(names), h.relations()
	for _, name := range []string{"users", "sessions", "memberships", "assets", "imports", "findings", "observations", "notes",
		"integration_connections", "finding_deliveries", "work_views", "ai_profiles", "ai_policies", "ai_egress_grants",
		"assessment_previews", "assessment_jobs"} {
		check(t, len(before[name]) > 0, "claimed published legacy data was not created through real HTTP/intake APIs")
	}
	check(t, n.calls.Load() == 0 && n.forbidden.Load() == 0, "legacy configuration/preview/queue performed native I/O")
	t.Log("REACHED: exact published V9 app, real HTTP bootstrap/roles/S3 finding/notes/Slack/view/assessment preview+job; no SQL business seeds")
	h.open()
	currentLedger := h.ledger()
	observation := object{
		"origin": publishedCommit, "startingVersions": oldLedger, "afterCurrentOpen": currentLedger,
		"legacyRelations": len(names), "legacyDataSHA256": digest(encoded(t, before)),
		"legacyDefinitionsSHA256": digest(encoded(t, definitions)), "businessSQLSeeds": false,
		"nativeCallsBeforeUpgrade": n.calls.Load(), "rawRowsIdenticalAtObservedVersion": reflect.DeepEqual(before, h.snapshot(names)),
	}
	output := filepath.Join("..", "..", ".artifacts", "jira-work-items", "published-v9-upgrade-"+nonce(t)+".json")
	must(t, "record bounded nonsecret actual migration observation", os.WriteFile(output, encoded(t, observation), 0600))
	t.Log("actual migration observation:", output)
	same(t, "RED: current app did not add V10 over exact populated published V9",
		currentLedger, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"})
	same(t, "V10 changed the historical relation set", h.relations(), relations)
	same(t, "V10 must assert the exact approved DDL delta before any projection",
		deliverycompat.Project(t, definitions, h.definitions(names)), definitions)
	same(t, "V10 changed actual historical business rows", h.legacyData(names), before)
	body, _ := h.request(h.ctx, viewer, "GET", connectionsPath+"/"+slackConnection.ID, nil, 200)
	same(t, "Jira-disabled migration changed exact old Slack metadata keys/values", decoded[object](t, body)["connection"], slackShape)
	body, _ = h.request(h.ctx, viewer, "GET", deliveryPath(slackJob.ID), nil, 200)
	same(t, "migration changed exact old Slack delivery DTO/payload keys or values", decoded[object](t, body)["delivery"], slackJobShape)
	assertFindingUnchanged(t, f, h.finding(viewer, f.ID))
	viewID := decoded[struct{ ID string }](t, view).ID
	same(t, "migration changed actual saved view", h.json(viewer, "GET", "/api/v1/work/views/"+viewID, nil, 200).View, view)
	assessmentID := decoded[struct{ ID string }](t, assessment).ID
	same(t, "migration changed actual queued advisory", h.json(viewer, "GET", "/api/v1/ai/assessments/"+assessmentID, nil, 200).Assessment, assessment)
	afterDefinitions := h.definitions(names)
	h.reopen()
	same(t, "reopen repeated/omitted a current migration", h.ledger(), currentLedger)
	same(t, "reopen altered migrated definitions", h.definitions(names), afterDefinitions)
	same(t, "reopen changed the historical relation set", h.relations(), relations)
	same(t, "reopen changed old-column business data", h.legacyData(names), before)
	h.assertTypedRow(ownedRow{"integration_connections", h.admin.Workspace, slackConnection.ID}, n.target())
	h.assertTypedRow(ownedRow{"finding_deliveries", h.admin.Workspace, slackJob.ID}, n.target())
	same(t, "rolled-back typed-profile probes changed legacy business rows", h.legacyData(names), before)
	token := secret(t)
	c := h.connection(h.admin, n.target(), token)
	job := h.queue(h.admin, h.preview(h.admin, f.ID, c), "after-real-v9-upgrade", 202)
	h.assertTypedRow(ownedRow{"integration_connections", h.admin.Workspace, c.ID}, n.target())
	h.assertTypedRow(ownedRow{"finding_deliveries", h.admin.Workspace, job.ID}, n.target())
	p := n.arm(token, "ok", "ok", func() error { return h.marker(job.ID) })
	// Preserve the historical Slack intent and process it through the same worker.
	slackArrival := n.allowSlack(slackToken, "C123")
	w := h.worker(n, "upgraded-common-worker", 4*time.Second)
	process(t, h.ctx, w, true)
	body, _ = h.request(h.ctx, viewer, "GET", deliveryPath(slackJob.ID), nil, 200)
	slackDone := decoded[reply](t, body).Delivery
	slackDoneShape := decoded[object](t, body)["delivery"].(map[string]any)
	check(t, len(slackDoneShape) == len(slackJobShape), "confirmed old Slack DTO acquired Jira-only fields")
	for field := range slackJobShape {
		_, present := slackDoneShape[field]
		check(t, present, "confirmed old Slack DTO lost a historical field")
	}
	check(t, slackDone.State == "confirmed" && slackDone.Receipt != nil && slackDone.Receipt.RemoteID == "C123:1789560000.123456",
		"common worker changed old Slack delivery behavior when Jira was not selected")
	slackCall := awaitCall(t, slackArrival)
	check(t, len(slackCall.Body) == 6 && slackCall.Body["parse"] == "none" &&
		slackCall.Body["unfurl_links"] == false && slackCall.Body["unfurl_media"] == false &&
		slackCall.Body["text"] == slackJob.Payload.Title+"\n"+slackJob.Payload.Body+"\n"+slackJob.Payload.DeepLink,
		"common worker changed native Slack accessibility/parse/unfurl/payload semantics")
	process(t, h.ctx, w, true)
	check(t, h.delivery(viewer, job.ID).State == "confirmed" && p.posts.Load() == 1, "new Jira workflow not usable after authentic migration")
	assertADF(t, awaitCall(t, p.postArrived), job.Payload)
	assertFindingUnchanged(t, f, h.finding(viewer, f.ID))
}
