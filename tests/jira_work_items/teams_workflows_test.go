//go:build integration && teams_workflows

package jira_work_items

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTeamsT1EncryptedConfigurationRolesPrivacyAndLocalPreview(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer, viewer := h.user("analyst"), h.user("viewer")
	f := h.seed(h.admin, "Teams local review")
	value := n.workflowURL()
	h.rememberTeamsURL(value)
	input := teamsInput(value)
	h.json(actor{}, "POST", connectionsPath, input, 401)
	h.json(writer, "POST", connectionsPath, input, 403)
	h.json(viewer, "POST", connectionsPath, input, 403)
	c, second := h.teamsConnection(h.admin, value), h.teamsConnection(h.admin, value)
	h.teamsEncrypted(c, value)
	h.teamsEncrypted(second, value)
	check(t, !bytes.Equal(h.ciphertext(c.ID), h.ciphertext(second.ID)), "equal URLs reused a nonce or identity-bound ciphertext")
	originalCipher := h.ciphertext(c.ID)
	rejectedToken := secret(t)
	h.remember(rejectedToken)
	for key, value := range (object{
		"token": rejectedToken, "channel": "C123", "jira": n.common.target(),
		"headers": object{"Authorization": "Bearer " + rejectedToken}, "requestedBy": writer.ID,
		"workspaceId": writer.Workspace, "revision": 7, "endpoint": n.common.slack.URL,
	}) {
		invalid := copyObject(t, input)
		invalid[key] = value
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
		h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{key: value}, 400)
	}
	for _, key := range []string{"profile", "name", "enabled", "workflowUrl", "teams"} {
		for _, invalidValue := range []any{nil, 123} {
			invalid := copyObject(t, input)
			invalid[key] = invalidValue
			h.json(h.admin, "POST", connectionsPath, invalid, 400)
		}
		invalid := copyObject(t, input)
		delete(invalid, key)
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
		if key != "profile" {
			h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{key: nil}, 400)
		}
	}
	for _, meta := range []any{
		object{}, object{"channelType": "standard"}, object{"ownershipAcknowledged": true},
		object{"channelType": "private", "ownershipAcknowledged": true},
		object{"channelType": "standard", "ownershipAcknowledged": false},
		object{"channelType": "standard", "ownershipAcknowledged": "true"},
		object{"channelType": "standard", "ownershipAcknowledged": true, "workflowOrigin": n.server.URL},
		object{"channelType": "standard", "ownershipAcknowledged": true, "teamId": "not-collected"},
	} {
		invalid := copyObject(t, input)
		invalid["teams"] = meta
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
		h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"teams": meta}, 400)
	}
	signature := "synthetic_" + nonce(t)
	h.remember(signature)
	base := n.server.URL
	for _, bad := range []string{
		"", strings.Replace(value, "https:", "http:", 1),
		"https://" + signature + "@" + strings.TrimPrefix(base, "https://") + "/invoke?sig=" + signature,
		base + "/invoke#fragment", base + "?sig=" + signature, base + "/?sig=" + signature,
		base + "/invoke", base + "/invoke?sig=", base + "/invoke?Sig=" + signature,
		base + "/invoke?sig=" + signature + "&sig=" + signature,
		base + "/invoke?sig=" + signature + "&%73ig=" + signature,
		base + "/invoke?sig=" + signature + "%20suffix",
		base + "/invoke?sig=" + signature + "&other=%0a",
		base + "/invoke?sig=" + signature + "&bad=%ZZ",
		base + "/invoke?sig=" + signature + ";bad=value",
		base + "/%2finvoke?sig=" + signature, base + "/%5cinvoke?sig=" + signature,
		base + "/%252finvoke?sig=" + signature, base + "/%2e%2e/invoke?sig=" + signature,
		base + "//invoke?sig=" + signature, base + "/invoke?sig=" + signature + "\r\n",
		"https://WORKFLOW.synthetic.invalid/invoke?sig=" + signature,
		"https://workflow.synthetic.invalid:443/invoke?sig=" + signature,
	} {
		invalid := copyObject(t, input)
		invalid["workflowUrl"] = bad
		h.json(h.admin, "POST", connectionsPath, invalid, 400)
	}
	h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": base + "/invoke"}, 400)
	h.json(h.admin, "POST", connectionsPath, json.RawMessage(`null`), 400)
	h.json(h.admin, "POST", connectionsPath, json.RawMessage(`[]`), 400)
	h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"profile": teamsProfile}, 400)
	same(t, "invalid config partially changed the connection", h.teamsJSON(viewer, "GET", connectionsPath+"/"+c.ID, nil, 200).Connection, c)
	check(t, bytes.Equal(originalCipher, h.ciphertext(c.ID)), "invalid config changed stored ciphertext")

	bounded := value + "&padding="
	bounded += strings.Repeat("p", 16384-len(bounded))
	check(t, len(bounded) == 16384, "credential boundary fixture is not exact")
	atLimit := h.teamsConnection(h.admin, bounded)
	h.teamsEncrypted(atLimit, bounded)
	over := teamsInput(bounded + "p")
	h.json(h.admin, "POST", connectionsPath, over, 400)
	h.json(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": bounded + "p"}, 400)
	h.json(viewer, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 403)
	h.json(writer, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": false}, 403)
	h.json(viewer, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": value}, 403)
	v := h.teamsPreview(writer, f.ID, c)
	same(t, "local preview exported unapproved source data", v.Payload, teamsPayload(f))
	same(t, "unchanged local review changed digest or payload", h.teamsPreview(writer, f.ID, c), v)
	for _, key := range []string{"payload", "workflowUrl", "destination", "requestedBy", "profile", "approvalRef"} {
		h.json(writer, "POST", previewPath(f.ID), object{"connectionId": c.ID, key: "forged"}, 400)
	}
	h.json(viewer, "POST", historyPath(f.ID), queueInput(v.preview, "viewer-denied"), 403)
	h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "viewer"}, 200)
	h.json(writer, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 403)
	h.json(writer, "POST", historyPath(f.ID), queueInput(v.preview, "stale-role"), 403)
	h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, object{"role": "analyst"}, 200)
	other := h.otherWorkspace()
	h.json(other, "GET", connectionsPath+"/"+c.ID, nil, 404)
	h.json(other, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": false}, 404)
	h.json(other, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 404)
	nonmember := viewer
	nonmember.Workspace = other.Workspace
	h.json(nonmember, "GET", connectionsPath+"/"+c.ID, nil, 403)
	h.json(nonmember, "POST", historyPath(f.ID), queueInput(v.preview, "nonmember"), 403)
	check(t, h.json(other, "GET", connectionsPath+"?profile="+teamsProfile, nil, 200).Total == 0, "Teams metadata crossed workspace")
	check(t, h.json(viewer, "GET", historyPath(f.ID)+"?profile="+teamsProfile, nil, 200).Total == 0, "local preview persisted an intent")

	unchanged := h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID,
		object{"name": c.Name, "enabled": c.Enabled, "workflowUrl": value, "teams": input["teams"]}, 200).Connection
	same(t, "exact config patch changed revision or timestamps", unchanged, c)
	check(t, bytes.Equal(originalCipher, h.ciphertext(c.ID)), "no-op patch changed the URL envelope")
	rotatedURL := n.workflowURL()
	h.rememberTeamsURL(rotatedURL)
	rotated := h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID,
		object{"name": "Renamed display destination", "enabled": false, "workflowUrl": rotatedURL, "teams": input["teams"]}, 200).Connection
	check(t, rotated.Revision == c.Revision+1 && !rotated.Enabled && rotated.Name == "Renamed display destination",
		"whole secret/config patch was not one atomic revision")
	h.teamsEncrypted(rotated, rotatedURL)
	h.json(writer, "POST", previewPath(f.ID), object{"connectionId": c.ID}, 409)
	h.reopen()
	same(t, "public metadata did not survive reopen", h.teamsJSON(viewer, "GET", connectionsPath+"/"+c.ID, nil, 200).Connection, rotated)
	check(t, n.calls.Load() == 0 && n.common.calls.Load() == 0 && n.common.slackPosts.Load() == 0, "config/read/preview attempted native activity")
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))

	must(t, "close configured core before absent-capability probe", h.closeCore())
	missing := &harness{fixture: newFixture(t)}
	missing.cfg.IntegrationEncryptionKey = nil
	missing.open()
	missing.enroll()
	missing.rememberTeamsURL(value)
	missing.json(missing.admin, "POST", connectionsPath, teamsInput(value), 503)
	check(t, missing.json(missing.admin, "GET", connectionsPath+"?profile="+teamsProfile, nil, 200).Total == 0 &&
		n.calls.Load() == 0, "missing key created a connection or tried native authentication")
	check(t, h.api.Load()+missing.api.Load() <= 220 && h.queries.calls.Load()+missing.queries.calls.Load() <= 1400,
		"combined T1 fixture budget exceeded")
}

func TestTeamsT2CanonicalConsentMixedListsAcceptedHistoryAndReopen(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer, viewer := h.user("analyst"), h.user("viewer")
	f := h.seed(h.admin, "Teams canonical consent")
	value := n.workflowURL()
	c, second := h.teamsConnection(h.admin, value), h.teamsConnection(h.admin, n.workflowURL())
	v := h.teamsPreview(writer, f.ID, c)
	same(t, "review payload is not the canonical finding snapshot", v.Payload, teamsPayload(f))
	for _, change := range []func(object){
		func(x object) { delete(x, "confirm") }, func(x object) { x["confirm"] = false },
		func(x object) { x["confirm"] = nil }, func(x object) { x["confirm"] = "true" },
		func(x object) { delete(x, "previewDigest") }, func(x object) { x["previewDigest"] = nil },
		func(x object) { x["previewDigest"] = "sha256:" + strings.Repeat("z", 64) },
		func(x object) { x["idempotencyKey"] = "" }, func(x object) { x["idempotencyKey"] = 12 },
		func(x object) { x["payload"] = object{"title": "forged"} },
		func(x object) { x["workflowUrl"] = value }, func(x object) { x["destination"] = v.Destination },
		func(x object) { x["requestedBy"] = h.admin.ID }, func(x object) { x["approvalRef"] = "forged" },
		func(x object) { x["prior"] = object{"state": "accepted"} },
	} {
		invalid := queueInput(v.preview, "invalid-"+nonce(t))
		change(invalid)
		h.json(writer, "POST", historyPath(f.ID), invalid, 400)
	}
	h.json(viewer, "POST", historyPath(f.ID), queueInput(v.preview, "viewer"), 403)
	queued := h.teamsQueue(writer, v, "approved-teams-key", 202)
	check(t, queued.State == "queued" && queued.ID != "" && queued.WorkspaceID == writer.Workspace &&
		queued.FindingID == f.ID && queued.RequestedBy == writer.ID && queued.ConnectionID == c.ID &&
		queued.ConnectionRevision == c.Revision && queued.DispatchStartedAt == nil && queued.OutboundAttemptedAt == nil &&
		queued.CompletedAt == nil && queued.Receipt == nil && queued.Failure == nil,
		"Teams queue did not durably preserve the exact approved intent")
	same(t, "queue changed public destination", queued.Destination, v.Destination)
	same(t, "queue changed reviewed payload", queued.Payload, v.Payload)
	same(t, "same-key replay minted another intent", h.teamsQueue(writer, v, "approved-teams-key", 200), queued)
	adminPreview := h.teamsPreview(h.admin, f.ID, c)
	h.json(h.admin, "POST", historyPath(f.ID), queueInput(adminPreview.preview, "approved-teams-key"), 409)
	h.json(h.admin, "POST", historyPath(f.ID), queueInput(v.preview, "wrong-actor-digest"), 409)
	secondPreview := h.teamsPreview(writer, f.ID, second)
	h.json(writer, "POST", historyPath(f.ID), queueInput(secondPreview.preview, "approved-teams-key"), 409)
	otherFinding := h.seed(h.admin, "Another Teams finding")
	otherPreview := h.teamsPreview(writer, otherFinding.ID, c)
	h.json(writer, "POST", historyPath(otherFinding.ID), queueInput(otherPreview.preview, "approved-teams-key"), 409)
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": "Changed after review"}, 200)
	changedPayload := h.teamsPreview(writer, f.ID, c)
	h.json(writer, "POST", historyPath(f.ID), queueInput(changedPayload.preview, "approved-teams-key"), 409)
	h.json(writer, "POST", historyPath(f.ID), queueInput(v.preview, "new-key-stale-payload"), 409)
	h.json(h.admin, "PATCH", "/api/v1/assets/"+f.AssetID, object{"name": f.AssetName}, 200)
	check(t, n.calls.Load() == 0, "preview/consent/replay automatically contacted Workflow")
	h.reopen()
	p := n.arm(value, "accepted", func() error { return h.marker(queued.ID) })
	w := h.worker(n.common, "teams-accepted", 4*time.Second)
	process(t, h.ctx, w, true)
	assertTeamsCard(t, awaitCall(t, p.arrived), queued.Payload)
	done := h.teamsDelivery(viewer, queued.ID)
	check(t, done.State == "accepted" && done.Receipt == nil && done.Failure == nil &&
		done.DispatchStartedAt != nil && done.OutboundAttemptedAt != nil && done.CompletedAt != nil,
		"native 202 became confirmed, invented a remote receipt or lost durable timestamps")
	check(t, !done.OutboundAttemptedAt.Before(*done.DispatchStartedAt) &&
		!done.CompletedAt.Before(*done.OutboundAttemptedAt), "outbound marker ordering differs")
	same(t, "dispatch rewrote immutable public destination", done.Destination, queued.Destination)
	same(t, "dispatch rewrote immutable payload", done.Payload, queued.Payload)
	process(t, h.ctx, w, false)
	must(t, "close accepted worker", w.Close())
	h.reopen()
	fresh := h.worker(n.common, "teams-no-replay", 4*time.Second)
	process(t, h.ctx, fresh, false)
	same(t, "accepted replay/reopen lost its durable result", h.teamsQueue(writer, v, "approved-teams-key", 200), done)
	rotatedURL := n.workflowURL()
	h.rememberTeamsURL(rotatedURL)
	c = h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": rotatedURL}, 200).Connection
	rotatedPreview := h.teamsPreview(writer, f.ID, c)
	check(t, rotatedPreview.BindingDigest != v.BindingDigest, "credential revision did not invalidate consent")
	h.json(writer, "POST", historyPath(f.ID), queueInput(rotatedPreview.preview, "approved-teams-key"), 409)
	h.json(writer, "POST", historyPath(f.ID), queueInput(v.preview, "fresh-key-old-credential"), 409)

	slackOne, slackJobOne := h.slackSelection(h.admin, f.ID, "mixed-teams-slack-1")
	slackTwo, slackJobTwo := h.slackSelection(h.admin, f.ID, "mixed-teams-slack-2")
	h.json(h.admin, "PATCH", connectionsPath+"/"+slackOne["id"].(string), object{"workflowUrl": value}, 400)
	h.json(h.admin, "PATCH", connectionsPath+"/"+slackOne["id"].(string), object{"teams": teamsInput(value)["teams"]}, 400)
	h.json(h.admin, "POST", historyPath(f.ID), object{
		"connectionId": slackOne["id"], "idempotencyKey": "slack-not-teams-consent", "previewDigest": v.BindingDigest, "confirm": true,
	}, 400)
	jiraConnections, jiraJobs := []object{}, []object{}
	for _, key := range []string{"mixed-teams-jira-1", "mixed-teams-jira-2"} {
		jc := h.connection(h.admin, n.common.target(), secret(t))
		h.json(h.admin, "PATCH", connectionsPath+"/"+jc.ID, object{"workflowUrl": value}, 400)
		jp := h.preview(writer, f.ID, jc)
		jd := h.queue(writer, jp, key, 202)
		body, _ := h.request(h.ctx, viewer, "GET", connectionsPath+"/"+jc.ID, nil, 200)
		item := decoded[object](t, body)["connection"].(map[string]any)
		exactKeys(t, "unchanged Jira connection", item, "id", "workspaceId", "profile", "name", "channel",
			"enabled", "credentialConfigured", "revision", "createdAt", "updatedAt", "jira", "permissionState")
		jiraConnections = append(jiraConnections, item)
		body, _ = h.request(h.ctx, viewer, "GET", deliveryPath(jd.ID), nil, 200)
		item = decoded[object](t, body)["delivery"].(map[string]any)
		exactKeys(t, "unchanged Jira delivery", item, "id", "workspaceId", "findingId", "connectionId", "connectionRevision",
			"profile", "channel", "requestedBy", "state", "payload", "createdAt", "dispatchStartedAt",
			"completedAt", "receipt", "failure", "jira", "createAttemptedAt")
		exactKeys(t, "unchanged Jira payload", item["payload"].(map[string]any), "title", "body", "deepLink", "fields")
		jiraJobs = append(jiraJobs, item)
	}
	secondJob := h.teamsQueue(writer, secondPreview, "mixed-teams-2", 202)
	h.teamsQueue(writer, h.teamsPreview(writer, otherFinding.ID, second), "other-finding-teams", 202)
	foreign := h.otherWorkspace()
	foreignFinding := h.seed(foreign, "Foreign Teams finding")
	foreignConnection := h.teamsConnection(foreign, n.workflowURL())
	foreignPreview := h.teamsPreview(foreign, foreignFinding.ID, foreignConnection)
	foreignJob := h.teamsQueue(foreign, foreignPreview, "foreign-teams", 202)
	h.json(writer, "POST", previewPath(f.ID), object{"connectionId": foreignConnection.ID}, 404)
	h.json(writer, "POST", historyPath(f.ID), queueInput(foreignPreview.preview, "foreign-connection"), 404)
	h.json(writer, "POST", historyPath(foreignFinding.ID), queueInput(v.preview, "foreign-finding"), 404)
	h.json(writer, "GET", deliveryPath(foreignJob.ID), nil, 404)
	h.json(foreign, "GET", deliveryPath(done.ID), nil, 404)
	teamConnections, teamJobs := []object{}, []object{}
	for _, id := range []string{c.ID, second.ID} {
		body, _ := h.request(h.ctx, viewer, "GET", connectionsPath+"/"+id, nil, 200)
		teamConnections = append(teamConnections, decoded[object](t, body)["connection"].(map[string]any))
	}
	for _, id := range []string{done.ID, secondJob.ID} {
		body, _ := h.request(h.ctx, viewer, "GET", deliveryPath(id), nil, 200)
		teamJobs = append(teamJobs, decoded[object](t, body)["delivery"].(map[string]any))
	}
	for _, selection := range []struct {
		path               string
		slack, jira, teams []object
	}{
		{connectionsPath, []object{slackOne, slackTwo}, jiraConnections, teamConnections},
		{historyPath(f.ID), []object{slackJobOne, slackJobTwo}, jiraJobs, teamJobs},
	} {
		for _, suffix := range []string{"", "?profile=slack-workspace-bot"} {
			h.profilePages(viewer, selection.path+suffix, "slack-workspace-bot", selection.slack, selection.teams[0]["id"].(string))
		}
		h.profilePages(viewer, jiraListPath(selection.path), jiraProfile, selection.jira, selection.teams[0]["id"].(string))
		h.profilePages(viewer, selection.path+"?profile="+teamsProfile, teamsProfile, selection.teams, selection.slack[0]["id"].(string))
		for _, query := range []string{"profile=", "profile=unknown",
			"profile=" + teamsProfile + "&profile=" + teamsProfile, "profile=" + teamsProfile + "&profile=" + jiraProfile} {
			h.json(viewer, "GET", selection.path+"?"+query, nil, 400)
		}
	}
	for _, profile := range []string{"slack-workspace-bot", jiraProfile, teamsProfile} {
		h.json(viewer, "GET", historyPath(foreignFinding.ID)+"?profile="+profile, nil, 404)
	}
	check(t, n.calls.Load() == 1 && n.common.calls.Load() == 0 && n.common.slackPosts.Load() == 0,
		"mixed lists/reopen/terminal replay dispatched or fell back to another profile")
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}
