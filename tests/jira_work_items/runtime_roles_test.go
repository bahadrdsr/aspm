//go:build integration && jira_runtime

package jira_work_items

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runtimeSameBinding(t *testing.T, queued, done delivery) {
	t.Helper()
	check(t, queued.ID == done.ID && queued.WorkspaceID == done.WorkspaceID && queued.FindingID == done.FindingID &&
		queued.ConnectionID == done.ConnectionID && queued.ConnectionRevision == done.ConnectionRevision &&
		queued.RequestedBy == done.RequestedBy && queued.Profile == done.Profile && queued.Channel == done.Channel,
		"command processing changed the API-approved identity, actor or profile")
	same(t, "command processing changed the approved target", done.Jira, queued.Jira)
	same(t, "command processing changed the approved payload", done.Payload, queued.Payload)
}

func runtimeConfirmed(t *testing.T, queued, done delivery) {
	t.Helper()
	runtimeSameBinding(t, queued, done)
	check(t, done.State == "confirmed" && done.Receipt != nil && done.Failure == nil &&
		done.DispatchStartedAt != nil && done.CreateAttemptedAt != nil && done.CompletedAt != nil &&
		done.Receipt.RemoteID == "SYN-42" && done.Receipt.RemoteURL == siteOrigin+"/browse/SYN-42",
		"RED: actual delivery command did not persist the canonical native Jira receipt")
}

func runtimeMetadataFailure(t *testing.T, queued, done delivery, redirect bool) {
	t.Helper()
	runtimeSameBinding(t, queued, done)
	check(t, done.Receipt == nil && done.CreateAttemptedAt == nil && done.CompletedAt != nil && done.Failure != nil,
		"pre-create transport failure fabricated creation, uncertainty or a receipt")
	failure := done.Failure
	check(t, !failure.Retryable && failure.RetryAfterSeconds == 0 && failure.NativeCode == "" &&
		len(failure.MissingFields) == 0, "transport refusal fabricated permission, native fields or a retry")
	if redirect {
		check(t, done.State == "blocked" && failure.Code == "scope" && failure.Stage == "metadata" && failure.HTTPStatus == 307,
			"owned metadata redirect lost the accepted blocked/scope/metadata/307 diagnostic")
		return
	}
	class := done.State + "/" + failure.Code + "/" + failure.Stage
	check(t, failure.HTTPStatus == 0 && (class == "failed/unavailable/metadata" ||
		class == "blocked/scope/metadata" || class == "blocked/unavailable/metadata" ||
		class == "blocked/native-client-unavailable/"),
		"transport refusal did not retain an explicitly permitted safe backend classification")
}

func TestJiraRuntimeR2ActualMainSeparateJiraAndLegacySlack(t *testing.T) {
	h, n := newHarness(t), newJira(t)
	routes := runtimeObserveNative(t, h.ctx, n, "r2")
	writer := h.user("analyst")
	finding := h.seed(h.admin, "Runtime separate Jira and Slack source")
	token, slackToken := secret(t), secret(t)
	c := h.connection(h.admin, routes.target(n), token)
	v := h.preview(writer, finding.ID, c)
	same(t, "runtime preview is not the canonical local export", v.Payload, expectedPayload(finding))
	queued := h.queue(writer, v, "runtime-r2-jira", 202)
	h.remember(slackToken)
	slackConnection := h.json(h.admin, "POST", connectionsPath, object{
		"profile": "slack-workspace-bot", "name": "Owned runtime legacy Slack",
		"channel": "C123", "enabled": true, "token": slackToken,
	}, 201).Connection
	slackQueued := h.json(writer, "POST", historyPath(finding.ID), object{
		"connectionId": slackConnection.ID, "idempotencyKey": "runtime-r2-slack",
	}, 202).Delivery
	check(t, n.calls.Load()+n.slackPosts.Load() == 0 &&
		routes.jira.accepted.Load()+routes.slack.accepted.Load()+routes.trap.accepted.Load() == 0,
		"core/API approval sent native traffic before the independent main started")
	p := n.arm(token, "ok", "ok", func() error { return h.marker(queued.ID) })
	t.Cleanup(func() { runtimeNativeEvidence(t, "r2", n, p) })
	slackArrival := n.allowSlack(slackToken, "C123")
	database, pg := runtimeDatabase(h, "r2-main")
	role := runtimeStartCommand(h, database, routes.slack.origin(), runtimeCA(t, n.rootDER),
		[]string{routes.jira.origin()}, "r2-main")
	role.checkHealth()
	done := runtimeAwaitTerminal(h, writer, queued.ID)
	runtimeConfirmed(t, queued, done)
	get, post := awaitCall(t, p.getArrived), awaitCall(t, p.postArrived)
	check(t, get.Method == "GET" && post.Method == "POST" &&
		get.AuthorizationHash == digest([]byte("Bearer "+token)) &&
		post.AuthorizationHash == get.AuthorizationHash, "actual main lost the selected Jira credential or GET/POST order")
	assertADF(t, post, v.Payload)
	slackDone := runtimeAwaitTerminal(h, writer, slackQueued.ID)
	runtimeSameBinding(t, slackQueued, slackDone)
	check(t, slackDone.State == "confirmed" && slackDone.Failure == nil && slackDone.Receipt != nil &&
		slackDone.Receipt.RemoteID == "C123:1789560000.123456" && slackDone.Profile == "slack-workspace-bot",
		"Jira command wiring broke the existing selected Slack delivery")
	slackCall := awaitCall(t, slackArrival)
	check(t, slackCall.Method == "POST" && slackCall.Path == "/api/chat.postMessage" &&
		slackCall.Body["channel"] == "C123", "legacy native Slack routing or target changed")
	runtimeAwaitClaims(t, h.ctx, pg, pg.claims.Load(), role.done)
	role.alive()
	role.stop()
	pg.close()
	routes.close()
	check(t, p.gets.Load() == 1 && p.posts.Load() == 1 && n.slackPosts.Load() == 1 &&
		routes.jira.accepted.Load() > 0 && routes.slack.accepted.Load() > 0 && routes.trap.accepted.Load() == 0 &&
		pg.queries.Load() > 0 && pg.workerPeak.Load() == 1, "actual-main native/SQL ledger does not account for exactly the approved work")
	assertFindingUnchanged(t, finding, h.finding(h.admin, finding.ID))
	h.noStoredSecrets()
	t.Log("REACHED: actual built main, separate approved Jira/Slack TLS origins, scoped API/PG receipts and health-only role")
}

func TestJiraRuntimeR3NetworkRefusalsAndNarrowerCallerPolicy(t *testing.T) {
	h, n, untrusted := newHarness(t), newJira(t), newJira(t)
	routes := runtimeObserveNative(t, h.ctx, n, "r3-approved")
	badTLS := runtimeObserveNative(t, h.ctx, untrusted, "r3-untrusted")
	unknown := newRuntimeRelay(t, h.ctx, "r3-unapproved", strings.TrimPrefix(n.server.URL, "https://"), nil)
	finding := h.seed(h.admin, "Runtime network refusal source")
	unknownTarget := routes.target(n)
	unknownTarget.APIBase = unknown.origin() + "/ex/jira/" + cloudID
	redirectToken := secret(t)
	type pending struct {
		name     string
		delivery delivery
		redirect bool
	}
	var queued []pending
	for _, row := range []struct {
		name   string
		target jiraTarget
		token  string
	}{
		{"unapproved", unknownTarget, secret(t)},
		{"redirect", routes.target(n), redirectToken},
		{"untrusted-tls", badTLS.target(untrusted), secret(t)},
	} {
		c := h.connection(h.admin, row.target, row.token)
		v := h.preview(h.admin, finding.ID, c)
		queued = append(queued, pending{row.name, h.queue(h.admin, v, "runtime-r3-"+row.name, 202), row.name == "redirect"})
	}
	p := n.arm(redirectToken, "redirect", "ok", nil)
	t.Cleanup(func() { runtimeNativeEvidence(t, "r3-approved", n, p); runtimeNativeEvidence(t, "r3-untrusted", untrusted) })
	check(t, n.calls.Load()+untrusted.calls.Load() == 0 && unknown.accepted.Load() == 0,
		"API connection validation or local preview incorrectly performed native I/O")
	database, pg := runtimeDatabase(h, "r3-main")
	ca := runtimeCA(t, n.rootDER)
	// The trap is transport-approved, so redirect rejection itself must stop it.
	role := runtimeStartCommand(h, database, routes.slack.origin(), ca,
		[]string{routes.jira.origin(), badTLS.jira.origin(), routes.trap.origin()}, "r3-main")
	role.checkHealth()
	for _, item := range queued {
		done := runtimeAwaitTerminal(h, h.admin, item.delivery.ID)
		runtimeMetadataFailure(t, item.delivery, done, item.redirect)
		runtimeEvidence(t, "r3-"+item.name+"-outcome", object{"state": done.State, "failure": done.Failure})
	}
	runtimeAwaitClaims(t, h.ctx, pg, pg.claims.Load(), role.done)
	role.alive()
	role.stop()
	pg.close()
	unknown.close()
	badTLS.close()
	check(t, unknown.accepted.Load() == 0 && routes.trap.accepted.Load() == 0 && n.slackPosts.Load() == 0 &&
		p.gets.Load() == 1 && p.posts.Load() == 0 && n.calls.Load() == 1,
		"unapproved origin/redirect reached a socket or terminal metadata work was automatically repeated")
	check(t, badTLS.jira.accepted.Load() > 0 && badTLS.jira.forwarded.Load() > 0 && untrusted.calls.Load() == 0,
		"TLS negative must attempt the owned TLS socket without reaching any native HTTP handler")

	narrowToken := secret(t)
	c := h.connection(h.admin, routes.target(n), narrowToken)
	narrow := h.queue(h.admin, h.preview(h.admin, finding.ID, c), "runtime-r3-caller-narrower", 202)
	database, narrowPG := runtimeDatabase(h, "r3-narrower")
	config := runtimeConfig(t, h, database, routes.slack.origin(), ca, []string{routes.jira.origin()})
	transport := runtimeClientPolicy(t, config.DeliveryClient)
	baseDial := transport.DialContext
	var rejected, forwarded atomic.Int32
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == routes.jira.address {
			rejected.Add(1)
			return nil, errors.New("caller denied this otherwise operator-approved origin")
		}
		forwarded.Add(1)
		return baseDial(ctx, network, address)
	}
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSClientConfig.MinVersion = 0
	snapshot := runtimeOwnSnapshot(t, &config)
	beforeSockets := routes.jira.accepted.Load()
	service := runtimeStartService(h, config, "r3-narrower")
	done := runtimeAwaitTerminal(h, h.admin, narrow.ID)
	runtimeMetadataFailure(t, narrow, done, false)
	runtimeAwaitClaims(t, h.ctx, narrowPG, narrowPG.claims.Load(), service.done)
	service.alive()
	service.stop()
	narrowPG.close()
	routes.close()
	check(t, rejected.Load() == 1 && forwarded.Load() == 0 && routes.jira.accepted.Load() == beforeSockets &&
		n.calls.Load() == 1, "Run bypassed/overwrote the narrower caller dialer, retained an inner Slack-only gate, or repeated refused work")
	snapshot.unchanged(t, &config)
	runtimeEvidence(t, "r3-caller-policy", object{"callerRefusals": rejected.Load(), "callerForwards": forwarded.Load()})
	t.Log("REACHED: actual-main origin/redirect/TLS refusals and real Run preserving the caller's narrower dial policy")
}

func TestJiraRuntimeR4CanceledCreateThenFreshActualMain(t *testing.T) {
	h, n := newHarness(t), newJira(t)
	routes := runtimeObserveNative(t, h.ctx, n, "r4")
	writer := h.user("analyst")
	finding := h.seed(h.admin, "Runtime held-create source")
	token := secret(t)
	c := h.connection(h.admin, routes.target(n), token)
	v := h.preview(writer, finding.ID, c)
	queued := h.queue(writer, v, "runtime-r4-held", 202)
	p := n.arm(token, "ok", "hold", func() error { return h.marker(queued.ID) })
	plans := []*jiraPlan{p}
	t.Cleanup(func() { runtimeNativeEvidence(t, "r4", n, plans...) })
	database, pg := runtimeDatabase(h, "r4-service")
	ca := runtimeCA(t, n.rootDER)
	origins := []string{routes.jira.origin(), routes.slack.origin()}
	config := runtimeConfig(t, h, database, routes.slack.origin(), ca, origins)
	transport := runtimeClientPolicy(t, config.DeliveryClient)
	transport.ForceAttemptHTTP2 = false
	transport.TLSClientConfig.MinVersion = 0
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSClientConfig.CurvePreferences = []tls.CurveID{tls.X25519, tls.CurveP256}
	var snapshot *runtimeOwnership
	field := reflect.ValueOf(&config).Elem().FieldByName("JiraAPIOrigins")
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf([]string{}) {
		callerOwned := make([]string, 2, 3)
		copy(callerOwned, origins)
		sort.Sort(sort.Reverse(sort.StringSlice(callerOwned)))
		callerOwned[:cap(callerOwned)][2] = "caller-reserved-backing-storage"
		field.Set(reflect.ValueOf(callerOwned))
		saved := runtimeOwnSnapshot(t, &config)
		snapshot = &saved
	}
	role := runtimeStartService(h, config, "r4-service")
	assertADF(t, awaitCall(t, p.postArrived), v.Payload)
	check(t, snapshot != nil, "runtime origin selection did not survive into programmatic configuration")
	role.checkHealth()
	role.cancel()
	awaitCancel(t, p.postCancelled)
	role.stop()
	pg.close()
	bound, cancel := context.WithTimeout(h.ctx, time.Second)
	_, _, healthErr := role.response(bound, "GET", "/healthz")
	cancel()
	check(t, healthErr != nil, "real Run left its owned HTTP listener alive after shutdown")
	uncertain := runtimeAwaitTerminal(h, writer, queued.ID)
	runtimeSameBinding(t, queued, uncertain)
	check(t, uncertain.State == "uncertain" && uncertain.Receipt == nil && uncertain.CreateAttemptedAt != nil &&
		uncertain.CompletedAt != nil && uncertain.Failure != nil && uncertain.Failure.Code == "uncertain" &&
		uncertain.Failure.Stage == "create" && !uncertain.Failure.Retryable &&
		p.gets.Load() == 1 && p.posts.Load() == 1, "service cancellation lost the durable attempted-create uncertainty")
	snapshot.unchanged(t, &config)
	beforeSockets := routes.jira.accepted.Load()

	database, freshPG := runtimeDatabase(h, "r4-fresh-main")
	fresh := runtimeStartCommand(h, database, routes.slack.origin(), ca, origins, "r4-fresh-main")
	fresh.checkHealth()
	runtimeAwaitClaims(t, h.ctx, freshPG, freshPG.claims.Load(), fresh.done)
	same(t, "fresh actual main changed terminal uncertainty", h.delivery(writer, queued.ID), uncertain)
	check(t, n.calls.Load() == 2 && p.gets.Load() == 1 && p.posts.Load() == 1 &&
		routes.jira.accepted.Load() == beforeSockets, "fresh main retried the old uncertain intent")
	const freshKey = "runtime-r4-new-explicit-intent"
	nextPreview := h.preview(writer, finding.ID, c)
	q := n.arm(token, "ok", "ok", runtimeKeyMarker(h, writer, freshKey))
	plans = append(plans, q)
	newIntent := h.queue(writer, nextPreview, freshKey, 202)
	newResult := runtimeAwaitTerminal(h, writer, newIntent.ID)
	runtimeConfirmed(t, newIntent, newResult)
	assertADF(t, awaitCall(t, q.postArrived), nextPreview.Payload)
	runtimeAwaitClaims(t, h.ctx, freshPG, freshPG.claims.Load(), fresh.done)
	same(t, "new work resurrected prior uncertainty", h.delivery(writer, queued.ID), uncertain)
	fresh.alive()
	fresh.stop()
	freshPG.close()
	routes.close()
	check(t, p.gets.Load() == 1 && p.posts.Load() == 1 && q.gets.Load() == 1 && q.posts.Load() == 1 &&
		n.calls.Load() == 4 && n.slackPosts.Load() == 0 && routes.trap.accepted.Load() == 0 &&
		routes.jira.accepted.Load() > beforeSockets, "fresh actual main did not send only the separately approved new intent")
	snapshot.unchanged(t, &config)
	assertFindingUnchanged(t, finding, h.finding(h.admin, finding.ID))
	t.Log("REACHED: real Run cancellation, native cancellation, durable uncertainty, and fresh actual main processing only new consented work")
}
