//go:build integration && teams_workflows

package jira_work_items

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
)

func (h *harness) teamsRevoke(who actor, c teamsConnection, kind, replacement string) {
	h.t.Helper()
	switch kind {
	case "role":
		h.json(h.admin, "PATCH", "/api/v1/users/"+who.ID, object{"role": "viewer"}, 200)
	case "disable":
		h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": false}, 200)
	case "rotate":
		h.rememberTeamsURL(replacement)
		h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": replacement}, 200)
	default:
		h.t.Fatal("undeclared revocation fixture")
	}
}

func (h *harness) teamsRestore(who actor, c teamsConnection, kind, original string) {
	h.t.Helper()
	switch kind {
	case "role":
		h.json(h.admin, "PATCH", "/api/v1/users/"+who.ID, object{"role": "analyst"}, 200)
	case "disable":
		h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": true}, 200)
	case "rotate":
		h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"workflowUrl": original}, 200)
	default:
		h.t.Fatal("undeclared restoration fixture")
	}
}

func TestTeamsT3NativeFailuresSizeRevocationFencingAndCrash(t *testing.T) {
	t.Run("native-failures-and-card-bounds", testTeamsNativeFailuresAndSize)
	t.Run("queued-and-held-revocation", testTeamsQueuedAndHeldRevocation)
	t.Run("binding-and-credential-tamper", testTeamsBindingAndCredentialTamper)
	t.Run("proxy-fencing-and-crash", testTeamsProxyFencingAndCrash)
}

func testTeamsNativeFailuresAndSize(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer := h.user("analyst")
	f := h.seed(h.admin, "Teams lifetime source")
	value := n.workflowURL()
	c := h.teamsConnection(h.admin, value)
	v := h.teamsPreview(writer, f.ID, c)
	w := h.worker(n.common, "teams-outcomes", 4*time.Second)
	other := h.worker(n.common, "teams-other-owner", 4*time.Second)
	for _, tc := range []struct {
		mode, state, code string
		status            int
	}{
		{"200", "uncertain", "uncertain", 200},
		{"401", "failed", "auth", 401},
		{"403", "failed", "auth", 403},
		{"429", "rate-limited", "rate_limited", 429},
		{"500", "uncertain", "uncertain", 500},
		{"drop", "uncertain", "uncertain", 0},
		{"redirect", "blocked", "scope", 307},
	} {
		job := h.teamsQueue(writer, v, "native-"+tc.mode, 202)
		p := n.arm(value, tc.mode, func() error { return h.marker(job.ID) })
		started := time.Now()
		process(t, h.ctx, w, true)
		if tc.status == 429 {
			check(t, time.Since(started) < time.Second, "Retry-After caused a sleep/retry instead of metadata")
		}
		assertTeamsCard(t, awaitCall(t, p.arrived), job.Payload)
		result := h.teamsDelivery(h.admin, job.ID)
		check(t, result.State == tc.state && result.Receipt == nil && result.CompletedAt != nil &&
			result.OutboundAttemptedAt != nil && result.Failure != nil && result.Failure.Code == tc.code &&
			result.Failure.NativeCode == "" && result.Failure.HTTPStatus == tc.status && !result.Failure.Retryable,
			"Teams native failure/secret-query echo was misclassified, retried or exposed")
		wantDelay := int64(0)
		if tc.status == 429 {
			wantDelay = 7
		}
		check(t, result.Failure.RetryAfterSeconds == wantDelay, "native Retry-After metadata changed")
		before := n.calls.Load()
		process(t, h.ctx, other, false)
		same(t, "terminal same-key replay changed outcome", h.teamsQueue(writer, v, "native-"+tc.mode, 200), result)
		check(t, p.calls.Load() == 1 && n.calls.Load() == before, "terminal native work was retried")
	}
	timed := h.teamsQueue(writer, v, "native-body-deadline", 202)
	deadlinePlan := n.arm(value, "hold", func() error { return h.marker(timed.ID) })
	deadline, stop := context.WithTimeout(h.ctx, 300*time.Millisecond)
	processed, deadlineErr := w.ProcessNext(deadline)
	stop()
	check(t, processed && (deadlineErr == nil || errors.Is(deadlineErr, context.DeadlineExceeded)),
		"native body deadline returned an unrelated infrastructure outcome")
	assertTeamsCard(t, awaitCall(t, deadlinePlan.arrived), timed.Payload)
	awaitCancel(t, deadlinePlan.cancel)
	timedOut := h.teamsDelivery(h.admin, timed.ID)
	check(t, timedOut.State == "uncertain" && timedOut.Receipt == nil && timedOut.OutboundAttemptedAt != nil &&
		timedOut.Failure != nil && timedOut.Failure.Code == "uncertain" && deadlinePlan.calls.Load() == 1,
		"timed-out possible POST was accepted or lost uncertainty")
	process(t, h.ctx, other, false)
	same(t, "timed-out replay created another intent", h.teamsQueue(writer, v, "native-body-deadline", 200), timedOut)
	deadlinePlan.finish()

	// The exact native cap is larger than the canonical parser's title limit.
	nativePayload := v.Payload
	nativePayload.Title = ""
	overhead := len(encoded(t, teamsCard(nativePayload)))
	nativePayload.Title = strings.Repeat("x", 28*1024-overhead)
	check(t, len(encoded(t, teamsCard(nativePayload))) == 28*1024, "native boundary fixture is not exactly 28 KiB")
	token := secret(t)
	h.remember(token)
	adapter, err := connectors.OpenDelivery(h.ctx, connectors.DeliveryConfig{
		Profile: connectors.TeamsWorkflows, Endpoint: value, Token: token, WorkspaceID: writer.Workspace,
		Client: n.common.client, Limits: connectors.Limits{Requests: 1, Pages: 1, PageSize: 1, Bytes: 64 << 10},
	})
	must(t, "open unchanged native Teams adapter with explicit client", err)
	p := n.arm(value, "accepted", nil)
	action := connectors.Action{WorkspaceID: writer.Workspace, FindingID: f.ID, IntentID: "native-size-"+nonce(t),
		ApprovalRef: "owned-protocol-only", Title: nativePayload.Title, Body: nativePayload.Body, DeepLink: nativePayload.DeepLink}
	atLimit, err := adapter.Send(h.ctx, action)
	must(t, "send exact adapter card limit", err)
	check(t, atLimit.State == "accepted" && atLimit.RemoteID == "" && atLimit.RemoteURL == "", "exact-cap native result invented a receipt")
	assertTeamsCard(t, awaitCall(t, p.arrived), nativePayload)
	action.Title += "x"
	nativePayload.Title = action.Title
	check(t, len(encoded(t, teamsCard(nativePayload))) == 28*1024+1, "oversize fixture is not one byte over")
	before := n.calls.Load()
	tooLarge, err := adapter.Send(h.ctx, action)
	check(t, errors.Is(err, connectors.ErrLimit) && tooLarge.State == "failed" && n.calls.Load() == before,
		"one-byte-over card was sent, truncated or accepted")
	large := h.seedTitle(h.admin, strings.Repeat("<", 256), strings.Repeat("<", 4096))
	largePreview := h.teamsPreview(writer, large.ID, c)
	same(t, "legal Teams title inherited Jira/Slack truncation", largePreview.Payload, teamsPayload(large))
	check(t, len(encoded(t, teamsCard(largePreview.Payload))) < 28*1024, "legal canonical payload unexpectedly exceeds adapter cap")
	largeJob := h.teamsQueue(writer, largePreview, "large-canonical", 202)
	p = n.arm(value, "accepted", func() error { return h.marker(largeJob.ID) })
	process(t, h.ctx, w, true)
	assertTeamsCard(t, awaitCall(t, p.arrived), largePreview.Payload)
	check(t, h.teamsDelivery(h.admin, largeJob.ID).State == "accepted", "valid long canonical Teams notification was blocked")
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func testTeamsQueuedAndHeldRevocation(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer := h.user("analyst")
	f := h.seed(h.admin, "Teams lifetime source")
	w := h.worker(n.common, "teams-outcomes", 4*time.Second)
	other := h.worker(n.common, "teams-other-owner", 4*time.Second)
	for _, phase := range []string{"queued", "held"} {
		for _, kind := range []string{"role", "disable", "rotate"} {
			original := n.workflowURL()
			selected := h.teamsConnection(h.admin, original)
			job := h.teamsQueue(writer, h.teamsPreview(writer, f.ID, selected), phase+"-"+kind, 202)
			priorCalls := n.calls.Load()
			var active <-chan stepResult
			var held *teamsPlan
			if phase == "held" {
				held = n.arm(original, "hold", func() error { return h.marker(job.ID) })
				active = asyncStep(h.ctx, w)
				assertTeamsCard(t, awaitCall(t, held.arrived), job.Payload)
				probe, cancel := context.WithTimeout(h.ctx, 750*time.Millisecond)
				must(t, "HTTP body wait retained the single worker SQL connection", w.Ping(probe))
				process(t, probe, other, false)
				h.request(probe, h.admin, "GET", connectionsPath+"?profile="+teamsProfile, nil, 200)
				cancel()
			}
			revokedAt := time.Now()
			h.teamsRevoke(writer, selected, kind, n.workflowURL())
			if phase == "queued" {
				process(t, h.ctx, w, true)
			} else {
				awaitCancel(t, held.cancel)
				check(t, time.Since(revokedAt) <= time.Second, "held Teams I/O did not cancel within one second of revocation")
				result := awaitStep(t, active)
				if result.err != nil {
					h.private([]byte(result.err.Error()))
				}
				held.finish()
			}
			settled := h.teamsDelivery(h.admin, job.ID)
			check(t, settled.Receipt == nil && settled.Failure != nil && settled.CompletedAt != nil, "revoked intent retained a receipt")
			if phase == "queued" {
				code := map[string]string{"role": "authorization-revoked", "disable": "connection-disabled", "rotate": "connection-changed"}[kind]
				check(t, settled.State == "blocked" && settled.Failure.Code == code && settled.DispatchStartedAt == nil &&
					settled.OutboundAttemptedAt == nil && n.calls.Load() == priorCalls, "queued revocation reached native POST")
			} else {
				check(t, settled.State == "uncertain" && settled.Failure.Code == "uncertain" &&
					settled.OutboundAttemptedAt != nil && n.calls.Load() == priorCalls+1,
					"revoked possible POST became accepted or lost uncertainty")
			}
			h.teamsRestore(writer, selected, kind, original)
			h.reopen()
			process(t, h.ctx, other, false)
			same(t, "regrant/reopen resurrected revoked Teams work", h.teamsDelivery(h.admin, job.ID), settled)
		}
	}
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func testTeamsBindingAndCredentialTamper(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer := h.user("analyst")
	f := h.seed(h.admin, "Teams lifetime source")
	w := h.worker(n.common, "teams-outcomes", 4*time.Second)
	other := h.worker(n.common, "teams-other-owner", 4*time.Second)
	for _, fault := range []string{"payload", "approval", "key", "digest", "target", "credential", "profile"} {
		selected := h.teamsConnection(h.admin, n.workflowURL())
		job := h.teamsQueue(writer, h.teamsPreview(writer, f.ID, selected), "fault-"+fault, 202)
		row := ownedRow{"finding_deliveries", writer.Workspace, job.ID}
		code := "binding-changed"
		switch fault {
		case "payload":
			h.mutateOwned(row, "payload=jsonb_set(payload,'{title}',to_jsonb($3::text))", []any{"unapproved title"}, false, true)
		case "approval":
			h.mutateOwned(row, "approval_ref=$3", []any{"unapproved reference"}, false, true)
		case "key":
			h.mutateOwned(row, "idempotency_key=$3", []any{"unapproved-"+nonce(t)}, false, true)
		case "digest":
			h.mutateOwned(row, "binding_digest=$3", []any{random(t, 32)}, false, true)
		case "target":
			code = "connection-changed"
			h.mutateOwned(ownedRow{"integration_connections", selected.WorkspaceID, selected.ID},
				"teams_target=jsonb_set(teams_target,'{workflowOrigin}',to_jsonb($3::text))", []any{n.common.slack.URL}, false, true)
		case "credential":
			code = "credential-unavailable"
			donor := h.teamsConnection(h.admin, n.workflowURL())
			h.mutateOwned(ownedRow{"integration_connections", selected.WorkspaceID, selected.ID},
				"credential_ciphertext=$3", []any{h.ciphertext(donor.ID)}, false, true)
		case "profile":
			h.mutateOwned(row, "profile='slack-workspace-bot',channel='C123',jira_target=NULL,teams_target=NULL", nil, false, true)
		}
		before := n.calls.Load()
		process(t, h.ctx, other, true)
		blocked := h.delivery(h.admin, job.ID)
		check(t, blocked.State == "blocked" && blocked.Receipt == nil && blocked.Failure != nil &&
			blocked.Failure.Code == code && n.calls.Load() == before, "tampered identity/config/credential reached native I/O")
		same(t, "tampered intent acquired an outbound marker",
			h.rows("SELECT (create_attempted_at IS NULL)::text FROM "+h.table("finding_deliveries")+
				" WHERE workspace_id=$1 AND id=$2", writer.Workspace, job.ID), []string{"true"})
	}
	for _, fault := range []string{"target", "credential"} {
		original := n.workflowURL()
		selected := h.teamsConnection(h.admin, original)
		var replacement []byte
		if fault == "credential" {
			donor := h.teamsConnection(h.admin, n.workflowURL())
			replacement = h.ciphertext(donor.ID)
		}
		job := h.teamsQueue(writer, h.teamsPreview(writer, f.ID, selected), "held-current-"+fault, 202)
		held := n.arm(original, "hold", func() error { return h.marker(job.ID) })
		active := asyncStep(h.ctx, w)
		awaitCall(t, held.arrived)
		changedAt := time.Now()
		row := ownedRow{"integration_connections", selected.WorkspaceID, selected.ID}
		if fault == "target" {
			h.mutateOwned(row, "teams_target=jsonb_set(teams_target,'{workflowOrigin}',to_jsonb($3::text))",
				[]any{n.common.slack.URL}, false, true)
		} else {
			h.mutateOwned(row, "credential_ciphertext=$3", []any{replacement}, false, true)
		}
		awaitCancel(t, held.cancel)
		check(t, time.Since(changedAt) <= time.Second, "unchanged revision hid a held target/credential change")
		result := awaitStep(t, active)
		if result.err != nil {
			h.private([]byte(result.err.Error()))
		}
		held.finish()
		settled := h.teamsDelivery(h.admin, job.ID)
		check(t, settled.State == "uncertain" && settled.Receipt == nil && settled.OutboundAttemptedAt != nil &&
			settled.Failure != nil && settled.Failure.Code == "uncertain" && held.calls.Load() == 1,
			"held raw identity change bypassed fresh authority/finalization checks")
		process(t, h.ctx, other, false)
	}
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}

func testTeamsProxyFencingAndCrash(t *testing.T) {
	h, n := newHarness(t), newTeamsServer(t)
	writer := h.user("analyst")
	f := h.seed(h.admin, "Teams lifetime source")
	value := n.workflowURL()
	c := h.teamsConnection(h.admin, value)
	v := h.teamsPreview(writer, f.ID, c)
	w := h.worker(n.common, "teams-outcomes", 4*time.Second)
	other := h.worker(n.common, "teams-other-owner", 4*time.Second)
	proxyJob := h.teamsQueue(writer, v, "no-proxy", 202)
	proxy := n.common.client.Transport.(*http.Transport).Clone()
	proxy.Proxy = func(*http.Request) (*url.URL, error) {
		t.Error("Teams evaluated a forbidden proxy policy")
		return url.Parse(n.common.trap.URL)
	}
	defer proxy.CloseIdleConnections()
	unsafe, openErr := app.OpenDeliveryWorker(h.ctx, app.DeliveryWorkerConfig{
		Database: app.DatabaseConfig{DatabaseURL: h.cfg.DatabaseURL, Schema: h.cfg.Schema, ApplicationName: "teams-proxy-"+nonce(t),
			MaxConnections: 1, QueryTracer: &h.queries, LogOutput: &h.log},
		EncryptionKey: h.cfg.IntegrationEncryptionKey, WorkerID: "teams-proxy-"+nonce(t), LeaseDuration: 4*time.Second,
		SlackEndpoint: n.common.slack.URL, Client: &http.Client{Transport: proxy, Timeout: 3*time.Second},
	})
	if openErr != nil {
		h.private([]byte(openErr.Error()))
		h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": false}, 200)
		process(t, h.ctx, w, true)
		c = h.teamsJSON(h.admin, "PATCH", connectionsPath+"/"+c.ID, object{"enabled": true}, 200).Connection
		v = h.teamsPreview(writer, f.ID, c)
	} else {
		t.Cleanup(func() { must(t, "close rejected-client worker", unsafe.Close()) })
		process(t, h.ctx, unsafe, true)
	}
	proxyResult := h.teamsDelivery(h.admin, proxyJob.ID)
	check(t, proxyResult.State == "blocked" && proxyResult.OutboundAttemptedAt == nil && proxyResult.Receipt == nil,
		"proxy-capable client reached a native attempt")

	job := h.teamsQueue(writer, v, "teams-natural-fence", 202)
	p := n.arm(value, "hold", func() error { return h.marker(job.ID) })
	short := h.worker(n.common, "teams-expiring", 350*time.Millisecond)
	active := asyncStep(h.ctx, short)
	awaitCall(t, p.arrived)
	var fenceBefore, fenceAfter int64
	must(t, "observe original numeric fence", h.db.QueryRow(h.ctx, "SELECT fence FROM "+h.table("finding_deliveries")+
		" WHERE workspace_id=$1 AND id=$2", writer.Workspace, job.ID).Scan(&fenceBefore))
	h.waitExpired(job.ID)
	process(t, h.ctx, other, true)
	fenced := h.snapshotJob(job.ID)
	p.finish()
	late := awaitStep(t, active)
	if late.err != nil {
		h.private([]byte(late.err.Error()))
	}
	same(t, "old owner committed a late accepted response", h.snapshotJob(job.ID), fenced)
	must(t, "observe recovered numeric fence", h.db.QueryRow(h.ctx, "SELECT fence FROM "+h.table("finding_deliveries")+
		" WHERE workspace_id=$1 AND id=$2", writer.Workspace, job.ID).Scan(&fenceAfter))
	check(t, fenceBefore > 0 && fenceAfter > fenceBefore, "recovery did not advance the numeric fence")
	uncertain := h.teamsDelivery(h.admin, job.ID)
	check(t, uncertain.State == "uncertain" && uncertain.OutboundAttemptedAt != nil && uncertain.Receipt == nil && p.calls.Load() == 1,
		"expired attempted notification was accepted or resent")

	crashed := h.teamsQueue(writer, v, "teams-real-crash", 202)
	p = n.arm(value, "hold", func() error { return h.marker(crashed.ID) })
	// The existing child forwards origin/TLS/config only; the outbox selects Teams.
	child := h.crashWorker(&jiraServer{server: n.server, slack: n.common.slack, rootDER: n.rootDER})
	awaitCall(t, p.arrived)
	child.kill(t)
	awaitCancel(t, p.cancel)
	h.waitExpired(crashed.ID)
	process(t, h.ctx, other, true)
	recovered := h.teamsDelivery(h.admin, crashed.ID)
	check(t, recovered.State == "uncertain" && recovered.OutboundAttemptedAt != nil && recovered.Receipt == nil && p.calls.Load() == 1,
		"real process crash lost the marker or replayed native POST")
	h.reopen()
	fresh := h.worker(n.common, "teams-after-crash", 4*time.Second)
	process(t, h.ctx, fresh, false)
	same(t, "crash uncertainty did not survive reopen", h.teamsDelivery(h.admin, crashed.ID), recovered)
	same(t, "crashed same-key replay manufactured new work", h.teamsQueue(writer, v, "teams-real-crash", 200), recovered)
	h.noStoredSecrets()
	assertFindingUnchanged(t, f, h.finding(h.admin, f.ID))
}
