//go:build integration && teams_runtime

package jira_work_items

import (
	"context"
	"crypto/tls"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestTeamsRuntimeR2ActualMainCombinedOriginsAndSignedCredential(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	routes := teamsRuntimeObserve(t, h.ctx, n, "tr2")
	writer := h.user("analyst")
	finding := h.seed(h.admin, "Teams actual-main combined network")
	value := n.workflowURL()
	connection := h.teamsConnection(h.admin, value)
	preview := h.teamsPreview(writer, finding.ID, connection)
	same(t, "runtime Teams review differs from canonical approved payload", preview.Payload, teamsPayload(finding))
	queued := h.teamsQueue(writer, preview, "tr2-teams-first", 202)
	plan := n.arm(value, "accepted", func() error { return h.marker(queued.ID) })

	jiraToken, slackToken := secret(t), secret(t)
	jiraConnection := h.connection(h.admin, routes.common.target(n.common), jiraToken)
	jiraPreview := h.preview(writer, finding.ID, jiraConnection)
	jiraQueued := h.queue(writer, jiraPreview, "tr2-jira-control", 202)
	jiraPlan := n.common.arm(jiraToken, "ok", "ok", func() error { return h.marker(jiraQueued.ID) })
	t.Cleanup(func() { runtimeNativeEvidence(t, "tr2", n.common, jiraPlan) })
	h.remember(slackToken)
	slackConnection := h.json(h.admin, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Owned Teams runtime Slack control",
		"channel": "C123", "enabled": true, "token": slackToken,
	}, 201).Connection
	slackQueued := h.json(writer, "POST", historyPath(finding.ID), object{
		"connectionId": slackConnection.ID, "idempotencyKey": "tr2-slack-control",
	}, 202).Delivery
	slackArrival := n.common.allowSlack(slackToken, "C123")
	check(t, n.calls.Load()+n.common.calls.Load()+n.common.slackPosts.Load() == 0 &&
		routes.teams.accepted.Load()+routes.common.jira.accepted.Load()+routes.common.slack.accepted.Load() == 0,
		"API configuration/preview/consent sent native traffic before actual main")
	ca := runtimeCA(t, n.rootDER, n.common.rootDER)
	beforeWrites := h.writes.Load()
	database, pg := runtimeDatabase(h, "tr2-main")
	// Jira is shared across lists; the Teams origin itself is admitted only by Teams here.
	role := teamsRuntimeStartCommand(h, database, routes.common.slack.origin(), ca,
		[]string{routes.common.jira.origin()}, []string{routes.teams.origin(), routes.common.jira.origin()}, "tr2-main")
	role.checkHealth()
	done := teamsRuntimeAwaitTerminal(h, writer, queued.ID)
	teamsRuntimeAccepted(t, queued, done)
	assertTeamsCard(t, awaitCall(t, plan.arrived), preview.Payload)
	jiraDone := runtimeAwaitTerminal(h, writer, jiraQueued.ID)
	runtimeConfirmed(t, jiraQueued, jiraDone)
	get, post := awaitCall(t, jiraPlan.getArrived), awaitCall(t, jiraPlan.postArrived)
	check(t, get.Method == "GET" && post.Method == "POST" &&
		get.AuthorizationHash == digest([]byte("Bearer "+jiraToken)) && post.AuthorizationHash == get.AuthorizationHash,
		"Teams runtime selection changed the explicit Jira native credential")
	assertADF(t, post, jiraPreview.Payload)
	slackDone := runtimeAwaitTerminal(h, writer, slackQueued.ID)
	runtimeSameBinding(t, slackQueued, slackDone)
	check(t, slackDone.Profile == "slack-workspace-bot" && slackDone.State == "confirmed" &&
		slackDone.Failure == nil && slackDone.Receipt != nil &&
		slackDone.Receipt.RemoteID == "C123:1789560000.123456",
		"Teams runtime selection changed the existing Slack receipt")
	slackCall := awaitCall(t, slackArrival)
	check(t, slackCall.Method == "POST" && slackCall.Path == "/api/chat.postMessage" && slackCall.Body["channel"] == "C123",
		"combined admission rerouted the legacy Slack native request")
	runtimeAwaitClaims(t, h.ctx, pg, pg.claims.Load(), role.done)
	role.alive()
	role.stop()
	pg.close()
	same(t, "same-key accepted replay created more work", h.teamsQueue(writer, preview, "tr2-teams-first", 200), done)
	teamsRuntimeHistory(h, writer, finding.ID, done)

	nextPreview := h.teamsPreview(writer, finding.ID, connection)
	next := h.teamsQueue(writer, nextPreview, "tr2-existing-network-origin", 202)
	nextPlan := n.arm(value, "accepted", func() error { return h.marker(next.ID) })
	database, secondPG := runtimeDatabase(h, "tr2-shared-main")
	shared := teamsRuntimeStartCommand(h, database, routes.common.slack.origin(), ca,
		[]string{routes.common.jira.origin(), routes.teams.origin()}, nil, "tr2-shared-main")
	shared.checkHealth()
	second := teamsRuntimeAwaitTerminal(h, writer, next.ID)
	teamsRuntimeAccepted(t, next, second)
	assertTeamsCard(t, awaitCall(t, nextPlan.arrived), nextPreview.Payload)
	runtimeAwaitClaims(t, h.ctx, secondPG, secondPG.claims.Load(), shared.done)
	shared.alive()
	shared.stop()
	secondPG.close()
	routes.close()
	teamsRuntimeHistory(h, writer, finding.ID, done, second)
	check(t, plan.calls.Load() == 1 && nextPlan.calls.Load() == 1 && n.calls.Load() == 2 &&
		routes.exact.Load() == 2 && jiraPlan.gets.Load() == 1 && jiraPlan.posts.Load() == 1 &&
		n.common.slackPosts.Load() == 1 && routes.common.trap.accepted.Load() == 0 &&
		pg.workerPeak.Load() == 1 && secondPG.workerPeak.Load() == 1 &&
		pg.queries.Load() > 0 && secondPG.queries.Load() > 0 && h.writes.Load() == beforeWrites,
		"main did not preserve the exact credential/role union, no-repeat ledger or DB-only worker scope")
	same(t, "network-union restart changed earlier accepted result", h.teamsDelivery(writer, queued.ID), done)
	h.noStoredSecrets()
	assertFindingUnchanged(t, finding, h.finding(h.admin, finding.ID))
	t.Log("REACHED: actual main signed Teams 202/accepted, explicit Jira/Slack controls, public history and network-only union")
}

func TestTeamsRuntimeR3RefusalsCancellationAndFreshMain(t *testing.T) {
	h, n, untrusted := newHarness(t), newTeamsServer(t), newTeamsServer(t)
	routes := teamsRuntimeObserve(t, h.ctx, n, "tr3")
	badTLS := teamsRuntimeObserve(t, h.ctx, untrusted, "tr3-untrusted")
	unknown := newRuntimeRelay(t, h.ctx, "tr3-unapproved", routes.teams.upstream, nil)
	writer := h.user("analyst")
	finding := h.seed(h.admin, "Teams runtime refusal and recovery")
	unknownURL := unknown.origin() + n.workflowURL()[len(n.server.URL):]
	redirectURL, untrustedURL := n.workflowURL(), untrusted.workflowURL()
	type pending struct {
		name string
		job  teamsDelivery
	}
	var jobs []pending
	for _, row := range []struct{ name, value string }{
		{"unapproved", unknownURL}, {"redirect", redirectURL}, {"untrusted-tls", untrustedURL},
	} {
		c := h.teamsConnection(h.admin, row.value)
		v := h.teamsPreview(writer, finding.ID, c)
		jobs = append(jobs, pending{row.name, h.teamsQueue(writer, v, "tr3-"+row.name, 202)})
	}
	redirect := n.arm(redirectURL, "redirect", func() error { return h.marker(jobs[1].job.ID) })
	ca := runtimeCA(t, n.rootDER, n.common.rootDER)
	beforeWrites := h.writes.Load()
	check(t, n.calls.Load()+untrusted.calls.Load() == 0 && unknown.accepted.Load() == 0,
		"Teams API metadata or local review performed native I/O")
	database, pg := runtimeDatabase(h, "tr3-negative-main")
	role := teamsRuntimeStartCommand(h, database, routes.common.slack.origin(), ca, nil,
		[]string{routes.teams.origin(), badTLS.teams.origin(), routes.common.trap.origin()}, "tr3-negative-main")
	role.checkHealth()
	var results []teamsDelivery
	for _, row := range jobs {
		done := teamsRuntimeAwaitTerminal(h, writer, row.job.ID)
		teamsRuntimeRefused(t, row.job, done, row.name == "redirect")
		results = append(results, done)
	}
	assertTeamsCard(t, awaitCall(t, redirect.arrived), jobs[1].job.Payload)
	runtimeAwaitClaims(t, h.ctx, pg, pg.claims.Load(), role.done)
	role.alive()
	role.stop()
	pg.close()
	check(t, unknown.accepted.Load() == 0 && unknown.forwarded.Load() == 0 &&
		routes.common.trap.accepted.Load() == 0 && n.common.forbidden.Load() == 0 &&
		redirect.calls.Load() == 1 && n.calls.Load() == 1,
		"unapproved/redirect destination reached a socket or a terminal Teams POST was repeated")
	check(t, badTLS.teams.accepted.Load() > 0 && badTLS.teams.forwarded.Load() > 0 &&
		badTLS.teams.toUpstream.Load() > 0 && untrusted.calls.Load() == 0,
		"untrusted TLS negative needs a real handshake-byte witness and zero native HTTP")
	tlsSockets := badTLS.teams.accepted.Load()

	value := n.workflowURL()
	c := h.teamsConnection(h.admin, value)
	preview := h.teamsPreview(writer, finding.ID, c)
	held := h.teamsQueue(writer, preview, "tr3-held-send", 202)
	heldPlan := n.arm(value, "hold", func() error { return h.marker(held.ID) })
	database, runPG := runtimeDatabase(h, "tr3-cancel-run")
	jira := []string{routes.common.jira.origin(), routes.common.slack.origin()}
	teams := []string{routes.teams.origin(), routes.common.slack.origin(), routes.common.jira.origin()}
	config := teamsRuntimeConfig(t, h, database, routes.common.slack.origin(), ca, jira, teams)
	callerJira := make([]string, len(jira), len(jira)+1)
	copy(callerJira, jira)
	sort.Sort(sort.Reverse(sort.StringSlice(callerJira)))
	callerJira[:cap(callerJira)][len(jira)] = "caller-reserved-jira-backing"
	config.JiraAPIOrigins = callerJira
	field := reflect.ValueOf(&config).Elem().FieldByName("TeamsWorkflowOrigins")
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf([]string{}) {
		callerTeams := make([]string, len(teams), len(teams)+1)
		copy(callerTeams, teams)
		sort.Sort(sort.Reverse(sort.StringSlice(callerTeams)))
		callerTeams[:cap(callerTeams)][len(teams)] = "caller-reserved-teams-backing"
		field.Set(reflect.ValueOf(callerTeams))
	}
	key := make([]byte, len(config.IntegrationEncryptionKey), len(config.IntegrationEncryptionKey)+1)
	copy(key, config.IntegrationEncryptionKey)
	key[:cap(key)][len(key)] = 0xa7
	config.IntegrationEncryptionKey = key
	transport := runtimeClientPolicy(t, config.DeliveryClient)
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig.MinVersion = 0
	transport.TLSClientConfig.NextProtos = make([]string, 1, 2)
	transport.TLSClientConfig.NextProtos[0] = "http/1.1"
	transport.TLSClientConfig.NextProtos[:2][1] = "caller-reserved-tls-backing"
	transport.TLSClientConfig.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256}
	unchanged := teamsRuntimeOwnSnapshot(t, &config)
	service := runtimeStartService(h, config, "tr3-cancel-run")
	assertTeamsCard(t, awaitCall(t, heldPlan.arrived), preview.Payload)
	service.checkHealth()
	service.cancel()
	awaitCancel(t, heldPlan.cancel)
	service.stop()
	runPG.close()
	bound, cancel := context.WithTimeout(h.ctx, time.Second)
	_, _, healthErr := service.response(bound, "GET", "/healthz")
	cancel()
	check(t, healthErr != nil, "Run left its listener alive after held-send cancellation")
	uncertain := teamsRuntimeAwaitTerminal(h, writer, held.ID)
	teamsRuntimeRefused(t, held, uncertain, false)
	unchanged()
	results = append(results, uncertain)
	beforeSockets, beforeHTTP := routes.teams.accepted.Load(), n.calls.Load()

	database, freshPG := runtimeDatabase(h, "tr3-fresh-main")
	fresh := teamsRuntimeStartCommand(h, database, routes.common.slack.origin(), ca, jira, teams, "tr3-fresh-main")
	fresh.checkHealth()
	runtimeAwaitClaims(t, h.ctx, freshPG, freshPG.claims.Load(), fresh.done)
	same(t, "fresh main changed the old uncertain result", h.teamsDelivery(writer, held.ID), uncertain)
	same(t, "uncertain same-key replay created a new send", h.teamsQueue(writer, preview, "tr3-held-send", 200), uncertain)
	check(t, n.calls.Load() == beforeHTTP && routes.teams.accepted.Load() == beforeSockets && heldPlan.calls.Load() == 1,
		"fresh main blindly retried a Teams send with a committed outbound marker")
	const freshKey = "tr3-new-explicit-intent"
	freshPreview := h.teamsPreview(writer, finding.ID, c)
	nextPlan := n.arm(value, "accepted", runtimeKeyMarker(h, writer, freshKey))
	next := h.teamsQueue(writer, freshPreview, freshKey, 202)
	done := teamsRuntimeAwaitTerminal(h, writer, next.ID)
	teamsRuntimeAccepted(t, next, done)
	assertTeamsCard(t, awaitCall(t, nextPlan.arrived), freshPreview.Payload)
	runtimeAwaitClaims(t, h.ctx, freshPG, freshPG.claims.Load(), fresh.done)
	same(t, "new consent resurrected old uncertainty", h.teamsDelivery(writer, held.ID), uncertain)
	fresh.alive()
	fresh.stop()
	freshPG.close()
	unknown.close()
	badTLS.close()
	routes.close()
	results = append(results, done)
	teamsRuntimeHistory(h, writer, finding.ID, results...)
	check(t, redirect.calls.Load() == 1 && heldPlan.calls.Load() == 1 && nextPlan.calls.Load() == 1 &&
		n.calls.Load() == 3 && routes.exact.Load() == 3 && untrusted.calls.Load() == 0 &&
		n.common.calls.Load()+n.common.slackPosts.Load()+n.common.forbidden.Load() == 0 &&
		unknown.accepted.Load() == 0 && routes.common.trap.accepted.Load() == 0 &&
		badTLS.teams.accepted.Load() == tlsSockets &&
		routes.teams.accepted.Load() > beforeSockets && pg.workerPeak.Load() == 1 &&
		runPG.workerPeak.Load() == 1 && freshPG.workerPeak.Load() == 1 &&
		h.writes.Load() == beforeWrites && n.calls.Load()+untrusted.calls.Load() <= 64,
		"negative/recovery ledgers contain an unapproved socket, replay, receipt or unrelated role traffic")
	unchanged()
	h.noStoredSecrets()
	assertFindingUnchanged(t, finding, h.finding(h.admin, finding.ID))
	t.Log("REACHED: actual-main origin/redirect/TLS refusals, real Run cancellation and fresh-main new-intent recovery")
}
