//go:build integration

package jira_work_items

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func connectionInput(target jiraTarget, token string) object {
	return object{"profile": jiraProfile, "name": "Owned selected Jira", "enabled": true, "token": token, "jira": target}
}
func copyObject(t *testing.T, value any) object { return decoded[object](t, encoded(t, value)) }
func jiraListPath(path string) string           { return path + "?profile=" + jiraProfile }

func exactKeys(t *testing.T, label string, value object, keys ...string) {
	t.Helper()
	check(t, len(value) == len(keys), label+": undeclared or omitted JSON key")
	for _, key := range keys {
		_, present := value[key]
		check(t, present, label+": missing JSON key "+key)
	}
}
func slackListShape(t *testing.T, value object, history bool) {
	t.Helper()
	check(t, value["profile"] == "slack-workspace-bot", "old Slack list returned another profile")
	if !history {
		exactKeys(t, "old Slack connection", value, "id", "workspaceId", "profile", "name", "channel", "enabled",
			"credentialConfigured", "revision", "createdAt", "updatedAt")
		return
	}
	exactKeys(t, "old Slack delivery", value, "id", "workspaceId", "findingId", "connectionId", "connectionRevision",
		"profile", "channel", "requestedBy", "state", "payload", "createdAt", "dispatchStartedAt", "completedAt", "receipt", "failure")
	exactKeys(t, "old Slack payload", value["payload"].(map[string]any), "title", "body", "deepLink")
}
func (h *harness) slackSelection(who actor, findingID, key string) (object, object) {
	h.t.Helper()
	token := secret(h.t)
	h.remember(token)
	body, _ := h.request(h.ctx, who, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Owned Slack " + key, "channel": "C123", "enabled": true, "token": token,
	}, 201)
	c := decoded[object](h.t, body)["connection"].(map[string]any)
	slackListShape(h.t, c, false)
	body, _ = h.request(h.ctx, who, "POST", historyPath(findingID), object{
		"connectionId": c["id"], "idempotencyKey": key,
	}, 202)
	job := decoded[object](h.t, body)["delivery"].(map[string]any)
	slackListShape(h.t, job, true)
	return c, job
}
func (h *harness) profilePages(who actor, path, profile string, expected []object, otherProfileCursor string) {
	h.t.Helper()
	expected = append([]object(nil), expected...)
	sort.Slice(expected, func(i, j int) bool { return expected[i]["id"].(string) < expected[j]["id"].(string) })
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	base := path + separator + "limit=1"
	nativeID := regexp.MustCompile(`^[a-f0-9]{32}$`)
	page := func(cursor string, offset int) {
		h.t.Helper()
		query := base
		if cursor != "" {
			check(h.t, nativeID.MatchString(cursor), "list continuation is not a native ID")
			query += "&cursor=" + cursor
		}
		body, _ := h.request(h.ctx, who, "GET", query, nil, 200)
		envelope := decoded[object](h.t, body)
		keys := []string{"apiVersion", "items", "total", "nextCursor"}
		if dataOrigin, present := envelope["dataOrigin"]; present {
			check(h.t, dataOrigin == "live" || dataOrigin == "synthetic", "list returned invalid data origin")
			keys = append(keys, "dataOrigin")
		}
		exactKeys(h.t, "old list envelope", envelope, keys...)
		_, array := envelope["items"].([]any)
		r := decoded[reply](h.t, body)
		check(h.t, array && r.APIVersion == app.APIVersion && r.Total == len(expected),
			"list total or envelope escaped the selected profile/workspace/finding")
		if offset == len(expected) {
			check(h.t, len(r.Items) == 0 && r.NextCursor == nil, "empty selected page fell back to another scope/profile")
			return
		}
		check(h.t, len(r.Items) == 1, "native-ID selected page did not respect limit")
		item := decoded[object](h.t, r.Items[0])
		id, ok := item["id"].(string)
		check(h.t, ok && nativeID.MatchString(id) && id > cursor && item["profile"] == profile &&
			item["workspaceId"] == who.Workspace, "selected page lost native-ID ordering/profile/workspace")
		if profile == "slack-workspace-bot" {
			slackListShape(h.t, item, strings.Contains(path, "/findings/"))
		}
		same(h.t, "selected page changed resource keys/values or membership", item, expected[offset])
		if offset+1 < len(expected) {
			check(h.t, r.NextCursor != nil && *r.NextCursor == id, "nextCursor is not the last selected native ID")
		} else {
			check(h.t, r.NextCursor == nil, "last selected page advertised a cross-profile continuation")
		}
	}
	cursor := ""
	for i := 0; i <= len(expected); i++ {
		page(cursor, i)
		if i < len(expected) {
			cursor = expected[i]["id"].(string)
		}
	}
	if otherProfileCursor != "" {
		offset := sort.Search(len(expected), func(i int) bool { return expected[i]["id"].(string) > otherProfileCursor })
		page(otherProfileCursor, offset)
	}
}

func assertFindingUnchanged(t *testing.T, before, after app.Finding) {
	t.Helper()
	check(t, before.WorkflowState == after.WorkflowState && before.SourceState == after.SourceState &&
		before.Disposition == after.Disposition && before.VerifiedResolution == after.VerifiedResolution,
		"Jira creation changed finding lifecycle or verification")
	same(t, "Jira creation changed notes", after.Notes, before.Notes)
	same(t, "Jira creation changed source observations", after.Observations, before.Observations)
}

func TestJiraJ1ConnectionPrivacyRolesAndStrictLocalPreview(t *testing.T) {
	t.Run("common-list-defaults-and-invalid-profile-without-jira-creation", func(t *testing.T) {
		h := newHarness(t)
		f := h.seed(h.admin, "Jira-independent Slack list selector")
		first, firstJob := h.slackSelection(h.admin, f.ID, "default-slack-1")
		second, secondJob := h.slackSelection(h.admin, f.ID, "default-slack-2")
		for _, selection := range []struct {
			path  string
			items []object
		}{
			{connectionsPath, []object{first, second}},
			{historyPath(f.ID), []object{firstJob, secondJob}},
		} {
			h.profilePages(h.admin, selection.path, "slack-workspace-bot", selection.items, "")
			h.profilePages(h.admin, selection.path+"?profile=slack-workspace-bot", "slack-workspace-bot", selection.items, "")
		}
		t.Log("REACHED: real Slack-only connection/history defaults and explicit Slack selection; exact keys, scoped totals and native-ID pages")
		for _, path := range []string{connectionsPath, historyPath(f.ID)} {
			for _, query := range []string{
				"profile=", "profile=not-a-profile",
				"profile=slack-workspace-bot&profile=slack-workspace-bot",
				"profile=jira-cloud-v3&profile=jira-cloud-v3",
				"profile=slack-workspace-bot&profile=jira-cloud-v3",
				"profile=jira-cloud-v3&profile=slack-workspace-bot",
			} {
				h.json(h.admin, "GET", path+"?limit=1&"+query, nil, 400)
			}
			h.profilePages(h.admin, jiraListPath(path), jiraProfile, nil, "")
		}
		t.Log("REACHED: invalid/repeated selectors rejected and explicit empty Jira scopes did not fall back to Slack")
	})
	t.Run("actual-preview-route", func(t *testing.T) {
		h := newHarness(t)
		f := h.seed(h.admin, "Preview route finding")
		h.json(h.admin, "POST", previewPath(f.ID), object{"connectionId": "not-a-valid-id"}, 400)
		t.Log("reached actual local-preview input validation, not a missing-route 404")
	})
	t.Run("encrypted-target-and-review", func(t *testing.T) {
		h, n := newHarness(t), newJira(t)
		writer, viewer := h.user("analyst"), h.user("viewer")
		f := h.seed(h.admin, "Strict preview source")
		token := secret(t)
		h.remember(token)
		input := connectionInput(n.target(), token)
		h.json(actor{}, "POST", connectionsPath, input, 401)
		h.json(writer, "POST", connectionsPath, input, 403)
		h.json(viewer, "POST", connectionsPath, input, 403)
		c := h.connection(h.admin, n.target(), token)
		second := h.connection(h.admin, n.target(), token)
		h.assertEncrypted(c, token)
		h.assertEncrypted(second, token)
		check(t, !bytes.Equal(h.ciphertext(c.ID), h.ciphertext(second.ID)), "duplicate token reused authenticated ciphertext/nonce")
		for key, value := range (object{
			"channel": "C123", "endpoint": n.slack.URL, "headers": object{"Authorization": token},
			"workspaceId": writer.Workspace, "requestedBy": writer.ID, "revision": 77,
		}) {
			invalid := copyObject(t, input)
			invalid[key] = value
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		for key, value := range (object{
			"apiBase": n.server.URL, "credentialType": "basic-api-token", "cloudId": "not-a-cloud-id",
			"project": "../SYN", "issueType": "Task", "siteOrigin": siteOrigin + "/not-an-origin",
			"arbitraryMetadata": object{"token": token},
		}) {
			invalid := copyObject(t, input)
			invalid["jira"].(map[string]any)[key] = value
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		for _, base := range []string{
			strings.Replace(n.target().APIBase, "https:", "http:", 1),
			n.server.URL + "/ex/jira/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
			n.target().APIBase + "?token=not-authority", n.target().APIBase + "#fragment",
			n.server.URL + "/ex/jira/%2f" + cloudID,
		} {
			invalid := copyObject(t, input)
			invalid["jira"].(map[string]any)["apiBase"] = base
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		for _, mapping := range []object{
			{"summary": "asset.name"}, {"project": "finding.id"}, {"description": "finding.title"},
			{"customfield_10010": "credentials.token"}, {"customfield_10010": "finding.notes"},
			{"customfield_10010": "report.raw"}, {"customfield_10010": object{"script": "not-allowed"}},
			{"customfield_10010": "{{ finding.title }}"},
		} {
			invalid := copyObject(t, input)
			invalid["jira"].(map[string]any)["fieldMappings"] = mapping
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		tooManyFields := object{}
		for i := 0; i < 17; i++ {
			tooManyFields["customfield_"+strconv.Itoa(20000+i)] = "finding.id"
		}
		invalid := copyObject(t, input)
		invalid["jira"].(map[string]any)["fieldMappings"] = tooManyFields
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
		for _, value := range []any{"", nil, "Bearer " + token} {
			invalid := copyObject(t, input)
			invalid["token"] = value
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		body, _ := h.request(h.ctx, viewer, "GET", connectionsPath+"/"+c.ID, nil, 200)
		meta := decoded[object](t, body)["connection"].(map[string]any)
		check(t, len(meta) == 12, "Jira metadata has an undeclared field or omitted contract field")
		for _, field := range []string{"id", "workspaceId", "profile", "name", "channel", "enabled",
			"credentialConfigured", "revision", "createdAt", "updatedAt", "jira", "permissionState"} {
			_, present := meta[field]
			check(t, present, "Jira metadata omitted a declared nonsecret field")
		}
		h.json(viewer, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 403)
		h.json(writer, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": false}, 403)
		h.json(viewer, "PATCH", connectionsPath+"/"+c.ID, object{"jira": n.target()}, 403)
		v := h.preview(writer, f.ID, c)
		same(t, "local preview is not the exact allowed finding export", v.Payload, expectedPayload(f))
		oversize := h.seedTitle(h.admin, "Oversize native summary", strings.Repeat("界", 256))
		h.json(writer, "POST", previewPath(oversize.ID), object{"connectionId": c.ID}, 400)
		check(t, h.json(viewer, "GET", jiraListPath(historyPath(oversize.ID)), nil, 200).Total == 0, "invalid native summary created an intent")
		for key, value := range (object{
			"payload": object{"title": "forged"}, "requestedBy": h.admin.ID, "profile": "slack-workspace-bot",
			"fields": object{"project": "FOREIGN"}, "deepLink": n.trap.URL, "nativeValidation": "verified",
		}) {
			h.json(writer, "POST", previewPath(f.ID), object{"connectionId": c.ID, key: value}, 400)
		}
		other := h.otherWorkspace()
		h.json(other, "GET", connectionsPath+"/"+c.ID, nil, 404)
		h.json(other, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 404)
		check(t, h.json(other, "GET", jiraListPath(connectionsPath), nil, 200).Total == 0, "connection listing crossed workspace")
		check(t, h.json(viewer, "GET", jiraListPath(historyPath(f.ID)), nil, 200).Total == 0, "local preview created an intent")
		beforeCipher := h.ciphertext(c.ID)
		unchanged := h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"token": token, "jira": n.target()}, 200).Connection
		check(t, unchanged.Revision == c.Revision && bytes.Equal(beforeCipher, h.ciphertext(c.ID)), "no-op patch rotated credential/revision")
		h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"jira": nil}, 400)
		rotated := secret(t)
		h.remember(rotated)
		changed := h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"token": rotated, "enabled": false}, 200).Connection
		check(t, changed.Revision == c.Revision+1 && !changed.Enabled, "atomic rotation/disable did not increment exactly once")
		h.assertEncrypted(changed, rotated)
		h.json(writer, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 409)
		h.reopen()
		same(t, "target/approval metadata did not survive reopen",
			h.json(viewer, "GET", connectionsPath+"/"+c.ID, nil, 200).Connection, changed)
		check(t, n.calls.Load() == 0, "configuration/read/preview performed native I/O")
		h.noStoredSecrets()
		assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
	})
	t.Run("independent-key-is-required", func(t *testing.T) {
		h, n := &harness{fixture: newFixture(t)}, newJira(t)
		h.cfg.IntegrationEncryptionKey = nil
		h.open()
		h.enroll()
		h.json(h.admin, "POST", connectionsPath, connectionInput(n.target(), secret(t)), 503)
		check(t, n.calls.Load() == 0, "missing encryption capability fell back to native/default credential")
	})
}

func TestJiraJ2CanonicalIntentReplayNativeReceiptAndReopen(t *testing.T) {
	h, n := newHarness(t), newJira(t)
	writer, viewer := h.user("analyst"), h.user("viewer")
	f := h.seed(h.admin, "Canonical Jira issue source")
	token := secret(t)
	c := h.connection(h.admin, n.target(), token)
	v := h.preview(writer, f.ID, c)
	same(t, "server preview did not resolve declared field mappings", v.Payload, expectedPayload(f))
	for _, change := range []func(object){
		func(input object) { delete(input, "confirm") },
		func(input object) { input["confirm"] = false },
		func(input object) { delete(input, "previewDigest") },
		func(input object) { input["payload"] = object{"title": "client-forged"} },
		func(input object) { input["approvalRef"] = "client-forged" },
		func(input object) { input["requestedBy"] = h.admin.ID },
		func(input object) { input["prior"] = object{"state": "confirmed"} },
		func(input object) { input["jira"] = n.target() },
		func(input object) { input["idempotencyKey"] = "" },
	} {
		input := queueInput(v, "invalid-"+nonce(t))
		change(input)
		h.json(writer, "POST", historyPath(f.ID), input, 400)
	}
	h.queue(viewer, v, "viewer-denied", 403)
	queued := h.queue(writer, v, "stable-approved-key", 202)
	check(t, queued.ID != "" && queued.Profile == jiraProfile && queued.Channel == "" &&
		queued.WorkspaceID == writer.Workspace && queued.RequestedBy == writer.ID && queued.FindingID == f.ID &&
		queued.ConnectionID == c.ID && queued.ConnectionRevision == c.Revision && queued.State == "queued" &&
		queued.DispatchStartedAt == nil && queued.CreateAttemptedAt == nil && queued.CompletedAt == nil &&
		queued.Receipt == nil && queued.Failure == nil, "queue did not persist exact canonical approval identity")
	same(t, "queue lost approved target", queued.Jira, c.Jira)
	same(t, "queue silently changed reviewed field values", queued.Payload, v.Payload)
	same(t, "identical explicit replay minted another intent", h.queue(writer, v, "stable-approved-key", 200), queued)
	adminPreview := h.preview(h.admin, f.ID, c)
	h.queue(h.admin, adminPreview, "stable-approved-key", 409)
	second := h.seed(h.admin, "Different Jira finding")
	h.queue(writer, h.preview(writer, second.ID, c), "stable-approved-key", 409)
	other := h.otherWorkspace()
	foreign := h.connection(other, n.target(), secret(t))
	h.json(writer, "POST", previewPath(f.ID), object{"connectionId": foreign.ID}, 404)
	h.json(other, "GET", deliveryPath(queued.ID), nil, 404)
	check(t, h.json(viewer, "GET", jiraListPath(historyPath(f.ID))+"&limit=1", nil, 200).Total == 1 &&
		n.calls.Load() == 0, "queue/history performed native creation or lost scoped intent")
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": "Changed after approval"}, 200)
	h.queue(writer, h.preview(writer, f.ID, c), "stable-approved-key", 409)
	h.reopen()
	p := n.arm(token, "paged", "ok", func() error { return h.marker(queued.ID) })
	w := h.worker(n, "native-receipt", 4*time.Second)
	process(t, h.ctx, w, true)
	check(t, p.gets.Load() == 2 && p.posts.Load() == 1, "worker did not run bounded native createmeta before one create")
	assertADF(t, awaitCall(t, p.postArrived), queued.Payload)
	done := h.delivery(viewer, queued.ID)
	check(t, done.State == "confirmed" && done.Receipt != nil && done.Receipt.RemoteID == "SYN-42" &&
		done.Receipt.RemoteURL == siteOrigin+"/browse/SYN-42" && done.Failure == nil &&
		done.DispatchStartedAt != nil && done.CreateAttemptedAt != nil && done.CompletedAt != nil,
		"native issue key/safe browser link/attempt timestamps were not durably confirmed")
	same(t, "native dispatch rewrote approved payload", done.Payload, queued.Payload)
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": f.AssetName}, 200)
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
	must(t, "close original worker", w.Close())
	h.reopen()
	fresh := h.worker(n, "fresh-no-replay", 4*time.Second)
	process(t, h.ctx, fresh, false)
	process(t, h.ctx, fresh, false)
	same(t, "confirmed replay after reopen lost exact intent", h.queue(writer, v, "stable-approved-key", 200), done)
	history := h.json(viewer, "GET", jiraListPath(historyPath(f.ID))+"&limit=1", nil, 200)
	check(t, history.Total == 1 && len(history.Items) == 1 && history.NextCursor == nil, "native history pagination changed")
	same(t, "history is not durable selected issue", decoded[delivery](t, history.Items[0]), done)
	changedTarget := n.target()
	changedTarget.FieldMappings = map[string]string{"customfield_10010": "finding.id"}
	newConnection := h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"jira": changedTarget}, 200).Connection
	h.queue(writer, h.preview(writer, f.ID, newConnection), "stable-approved-key", 409)
	h.queue(writer, v, "stable-approved-key", 409)
	check(t, p.posts.Load() == 1 && n.forbidden.Load() == 0, "replay/reopen/revision conflict caused another create or credential routing leak")
	nativeBeforeLists := n.calls.Load()
	slackOne, slackJobOne := h.slackSelection(h.admin, f.ID, "mixed-slack-1")
	slackTwo, slackJobTwo := h.slackSelection(h.admin, f.ID, "mixed-slack-2")
	secondJira := h.connection(h.admin, n.target(), secret(t))
	secondJiraJob := h.queue(writer, h.preview(writer, f.ID, secondJira), "mixed-jira-2", 202)
	h.json(h.admin, "POST", historyPath(second.ID), object{
		"connectionId": slackOne["id"], "idempotencyKey": "other-finding-slack",
	}, 202)
	h.queue(writer, h.preview(writer, second.ID, secondJira), "other-finding-jira", 202)
	foreignFinding := h.seed(other, "Other workspace mixed-profile finding")
	h.slackSelection(other, foreignFinding.ID, "other-workspace-slack")
	h.queue(other, h.preview(other, foreignFinding.ID, foreign), "other-workspace-jira", 202)
	jiraConnections, jiraJobs := []object{}, []object{}
	for _, id := range []string{newConnection.ID, secondJira.ID} {
		body, _ := h.request(h.ctx, viewer, "GET", connectionsPath+"/"+id, nil, 200)
		jiraConnections = append(jiraConnections, decoded[object](t, body)["connection"].(map[string]any))
	}
	for _, id := range []string{done.ID, secondJiraJob.ID} {
		body, _ := h.request(h.ctx, viewer, "GET", deliveryPath(id), nil, 200)
		jiraJobs = append(jiraJobs, decoded[object](t, body)["delivery"].(map[string]any))
	}
	for _, resource := range []struct {
		path, field string
		want        object
	}{
		{connectionsPath + "/" + slackOne["id"].(string), "connection", slackOne},
		{deliveryPath(slackJobOne["id"].(string)), "delivery", slackJobOne},
	} {
		body, _ := h.request(h.ctx, viewer, "GET", resource.path, nil, 200)
		same(t, "common individual GET changed Slack keys/values", decoded[object](t, body)[resource.field], resource.want)
	}
	t.Log("REACHED: API-created mixed Slack/Jira connections and intents in the selected workspace/finding, another finding and another workspace")
	for _, selection := range []struct {
		path        string
		slack, jira []object
	}{
		{connectionsPath, []object{slackOne, slackTwo}, jiraConnections},
		{historyPath(f.ID), []object{slackJobOne, slackJobTwo}, jiraJobs},
	} {
		for _, suffix := range []string{"", "?profile=slack-workspace-bot"} {
			h.profilePages(viewer, selection.path+suffix, "slack-workspace-bot", selection.slack, selection.jira[0]["id"].(string))
		}
		h.profilePages(viewer, jiraListPath(selection.path), jiraProfile, selection.jira, selection.slack[0]["id"].(string))
	}
	for _, selector := range []string{"", "?profile=slack-workspace-bot", "?profile=" + jiraProfile} {
		h.json(viewer, "GET", historyPath(foreignFinding.ID)+selector, nil, 404)
		h.json(other, "GET", historyPath(f.ID)+selector, nil, 404)
	}
	check(t, n.calls.Load() == nativeBeforeLists && n.slackPosts.Load() == 0,
		"mixed-profile local queue/list/read caused native I/O")
	t.Log("REACHED: mixed defaults/explicit Slack retain old JSON; explicit Jira lists/history, scoped totals and native/cross-profile cursor bounds")
	h.noStoredSecrets()
}

func TestJiraJ3NativeFailuresDiagnosticsAndNoBlindRetry(t *testing.T) {
	h, n := newHarness(t), newJira(t)
	f := h.seed(h.admin, "Native failure source")
	token := secret(t)
	c := h.connection(h.admin, n.target(), token)
	w := h.worker(n, "native-failures", 4*time.Second)
	cases := []struct {
		label, meta, create, state, code, stage string
		status, posts                           int
		fields                                  []string
	}{
		{"missing-required", "missing", "ok", "failed", "required_fields", "metadata", 0, 0, []string{"customfield_10099"}},
		{"unsupported-required", "unsupported", "ok", "failed", "required_fields", "metadata", 0, 0, []string{"customfield_10010"}},
		{"metadata-denied", "403", "ok", "failed", "auth", "metadata", 403, 0, nil},
		{"metadata-rate", "429", "ok", "rate-limited", "rate_limited", "metadata", 429, 0, nil},
		{"metadata-redirect", "redirect", "ok", "blocked", "scope", "metadata", 307, 0, nil},
		{"create-denied", "ok", "403", "failed", "auth", "create", 403, 1, nil},
		{"create-rate", "ok", "429", "rate-limited", "rate_limited", "create", 429, 1, nil},
		{"native-field-diagnostic", "ok", "400", "failed", "required_fields", "create", 400, 1, []string{"customfield_10099"}},
		{"drop-ack", "ok", "drop", "uncertain", "uncertain", "create", 0, 1, nil},
		{"no-native-result", "ok", "no-result", "uncertain", "uncertain", "create", 201, 1, nil},
		{"malformed-ack", "ok", "malformed", "uncertain", "uncertain", "create", 201, 1, nil},
		{"server-error", "ok", "500", "uncertain", "uncertain", "create", 500, 1, nil},
		{"timed-out-create", "ok", "hold", "uncertain", "uncertain", "create", 0, 1, nil},
	}
	for _, tc := range cases {
		v := h.preview(h.admin, f.ID, c)
		job := h.queue(h.admin, v, tc.label, 202)
		p := n.arm(token, tc.meta, tc.create, func() error { return h.marker(job.ID) })
		ctx := h.ctx
		cancel := func() {}
		if tc.create == "hold" {
			ctx, cancel = context.WithTimeout(h.ctx, 300*time.Millisecond)
		}
		processed, err := w.ProcessNext(ctx)
		cancel()
		if tc.create == "hold" {
			check(t, err == nil || errors.Is(err, context.DeadlineExceeded), "timeout returned an unrelated error")
			awaitCancel(t, p.postCancelled)
		} else {
			must(t, "settle native "+tc.label, err)
		}
		check(t, processed && int(p.posts.Load()) == tc.posts && p.gets.Load() == 1, tc.label+": metadata/create request counts differ")
		result := h.delivery(h.admin, job.ID)
		check(t, result.State == tc.state && result.Receipt == nil && result.Failure != nil && result.CompletedAt != nil,
			tc.label+": native failure hidden or mislabeled as success")
		fail := result.Failure
		check(t, fail.Code == tc.code && fail.Stage == tc.stage && !fail.Retryable, tc.label+": durable diagnostic/stage/retry semantics differ")
		check(t, reflectStrings(fail.MissingFields, tc.fields), tc.label+": actionable native missing/unsupported field IDs discarded")
		if tc.status != 0 {
			check(t, fail.HTTPStatus == tc.status, tc.label+": native HTTP status discarded")
		}
		if tc.status == 429 {
			check(t, fail.RetryAfterSeconds == 7, tc.label+": native Retry-After discarded or slept/retried")
		}
		check(t, (result.CreateAttemptedAt != nil) == (tc.posts == 1), tc.label+": create marker does not distinguish metadata-only failure")
		if tc.posts == 1 {
			assertADF(t, awaitCall(t, p.postArrived), job.Payload)
		}
		before := n.calls.Load()
		process(t, h.ctx, w, false)
		same(t, tc.label+": explicit same-key replay minted a new intent", h.queue(h.admin, v, tc.label, 200), result)
		h.reopen()
		check(t, reflect.DeepEqual(h.delivery(h.admin, job.ID), result) && n.calls.Load() == before,
			tc.label+": terminal outcome/reopen caused blind retry or lost uncertainty")
	}
	must(t, "close failure worker", w.Close())
	fresh := h.worker(n, "terminal-fresh", 4*time.Second)
	process(t, h.ctx, fresh, false)
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
	h.noStoredSecrets()
}
