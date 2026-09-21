//go:build integration

package ai_assessments

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAA1ReviewedPreviewConsentScopePrivacyAndDeniedWrites(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	writer, viewer := h.addUser("analyst"), h.addUser("viewer")
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	check(t, h.native.count() == 0, "profile/grant creation automatically assessed a finding")
	input := func() map[string]any {
		return map[string]any{"observationId": finding.Observations[0].ID, "profileId": p.ID,
			"grantId": grant.ID, "context": reviewedContext(), "reviewed": true}
	}
	h.json(actor{}, "POST", previewPath(finding.ID), input(), 401)
	h.json(viewer, "POST", previewPath(finding.ID), input(), 403)
	h.json(h.otherWorkspace(), "POST", previewPath(finding.ID), input(), 404)
	for _, field := range []string{"actorId", "endpoint", "headers", "promptRevision", "approvalRef", "profileRevision", "result"} {
		body := input()
		body[field] = "caller-controlled"
		h.json(writer, "POST", previewPath(finding.ID), body, 400)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["reviewed"] = false },
		func(v map[string]any) { delete(v, "reviewed") },
		func(v map[string]any) { v["context"] = " " },
		func(v map[string]any) { v["context"] = "known selected credential: " + key },
		func(v map[string]any) { v["context"] = "NUL\x00context" },
	} {
		body := input()
		change(body)
		h.json(writer, "POST", previewPath(finding.ID), body, 400)
	}
	large := input()
	large["context"] = strings.Repeat("安全", 6000)
	h.json(writer, "POST", previewPath(finding.ID), large, 413)
	missing := input()
	missing["observationId"] = strings.Repeat("f", 32)
	h.json(writer, "POST", previewPath(finding.ID), missing, 404)
	exact := input()
	exact["grantId"] = ""
	h.json(writer, "POST", previewPath(finding.ID), exact, 409)
	unreviewed, _ := h.profile("anthropic", false)
	exact["profileId"] = unreviewed.ID
	h.json(writer, "POST", previewPath(finding.ID), exact, 409)

	v := h.preview(writer, finding, p, pol, grant, reviewedContext())
	h.json(writer, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "missing-consent"}, 400)
	h.json(writer, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "false-consent", "consent": false}, 400)
	h.json(h.admin, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "wrong-requester", "consent": true}, 403)
	job := h.enqueue(writer, v, "explicit-consent", 202)
	h.json(viewer, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "viewer", "consent": true}, 403)
	h.json(viewer, "POST", jobsPath+"/"+job.ID+"/cancel", map[string]any{}, 403)
	for _, field := range []string{"actorId", "endpoint", "promptRevision", "usage", "success"} {
		h.json(writer, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "forged-" + field, "consent": true, field: "untrusted"}, 400)
	}
	check(t, h.job(viewer, job.ID).ID == job.ID, "viewer lost scoped advisory read access")
	h.json(h.otherWorkspace(), "GET", jobsPath+"/"+job.ID, nil, 404)
	h.assertReadonly(before, storage)
	check(t, h.native.count() == 0, "preview/queue/RBAC caused inference")

	config := h.configForWorker("invalid-client")
	proxy := h.native.client.Transport.(*http.Transport).Clone()
	proxy.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: "127.0.0.1:1"})
	insecure := h.native.client.Transport.(*http.Transport).Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	for _, client := range []*http.Client{nil, http.DefaultClient, {Transport: proxy, Timeout: time.Second}, {Transport: insecure, Timeout: time.Second}} {
		config.Client = client
		w, err := Production.OpenWorker(h.ctx, config)
		if w != nil {
			_ = w.Close()
		}
		check(t, err != nil && w == nil, "worker accepted ambient/proxy/insecure transport selection")
	}
	check(t, h.native.count() == 0, "invalid constructor performed provider I/O")
	h.json(h.admin, "POST", grantsPath, map[string]any{"profileId": p.ID, "profileRevision": p.Revision,
		"policyRevision": pol.Revision, "destination": p.Endpoint + "/", "task": validityTask, "dataClass": evidenceClass,
		"expiresAt": h.now().Add(time.Hour).Format(time.RFC3339Nano)}, 409)
	h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]string{"name": "Changed after consent"}, 200)
	h.json(writer, "POST", historyPath(finding.ID), map[string]any{"previewId": v.ID, "idempotencyKey": "stale-queue", "consent": true}, 409)
	process(t, h.ctx, h.worker(h.configForWorker("pre-dispatch-current-authority")), true)
	invalid := h.job(h.admin, job.ID)
	check(t, invalid.State == "invalidated" && invalid.Attempts == 0 && invalid.DispatchState == "not-started" &&
		invalid.Result == nil && h.native.count() == 0, "queue-time or immediate pre-I/O authority was not revalidated")
	h.assertReadonly(before, storage)
	t.Log("AA1 reviewed derived-context consent/scope/privacy and denied writes completed; no provider I/O")
}

func TestAA2ImmutableQueueReplayHistoryAndObservationSnapshotSurviveReopen(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
	first := h.enqueue(h.admin, v, "immutable-binding", 202)
	replay := h.enqueue(h.admin, v, "immutable-binding", 200)
	check(t, sameAssessment(first, replay), "identical queue replay did not preserve the original receipt")
	v2 := h.preview(h.admin, finding, p, pol, grant, reviewedContext()+"\nA second explicitly reviewed snapshot.")
	h.json(h.admin, "POST", historyPath(finding.ID), map[string]any{"previewId": v2.ID, "idempotencyKey": "immutable-binding", "consent": true}, 409)
	second := h.enqueue(h.admin, v2, "another-binding", 202)
	viewer := h.addUser("viewer")
	page := h.json(viewer, "GET", historyPath(finding.ID)+"?limit=1", nil, 200)
	check(t, page.Total == 2 && len(page.Items) == 1 && page.NextCursor != nil, "assessment history native continuation lost")
	tail := h.json(viewer, "GET", historyPath(finding.ID)+"?limit=1&cursor="+*page.NextCursor, nil, 200)
	check(t, len(tail.Items) == 1 && tail.NextCursor == nil && tail.Items[0]["id"] != page.Items[0]["id"], "history pagination duplicated or lost a job")
	cancelled := h.json(h.admin, "POST", jobsPath+"/"+second.ID+"/cancel", map[string]any{}, 200).Assessment
	check(t, cancelled.State == "cancelled" && cancelled.Attempts == 0 && cancelled.DispatchState == "not-started" && cancelled.Result == nil,
		"queued cancellation claimed a send or a successful assessment")
	h.reopen()
	check(t, h.job(h.admin, first.ID).State == "queued" && h.native.count() == 0, "queued job did not survive independent core reopen")
	_, changed := h.ingest(h.admin, finding.AssetID, "later-scan", "New raw source text is NOT the earlier approved context.")
	check(t, len(changed.Observations) == 2, "legitimate later scan did not preserve the original observation")
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	h.native.arm(p, key, v, first.ID, "ok", "supported", false)
	w := h.worker(h.configForWorker("reopened-worker"))
	process(t, h.ctx, w, true)
	result := h.job(viewer, first.ID)
	h.assertResult(result, p, v, "supported")
	check(t, result.ObservationID == v.ObservationID && result.Context == v.Context && result.ContextDigest == v.ContextDigest,
		"later source scan replaced the explicitly approved immutable snapshot")
	h.reopen()
	check(t, sameAssessment(h.job(h.admin, first.ID), result), "completed advisory/provenance did not survive core reopen")
	replay = h.enqueue(h.admin, v, "immutable-binding", 200)
	check(t, replay.ID == first.ID && replay.State == "succeeded" && h.native.count() == 1, "replay resubmitted an already dispatched job")
	h.assertReadonly(before, storage)
	t.Log("AA2 immutable queue/replay/history/reopen and old-observation context retained without source mutations")
}

func TestAA3FourNativeProviderMappingsUsageAndGroundedAdvisory(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	w := h.worker(h.configForWorker("four-native-profiles"))
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	for index, family := range []string{"openai", "azure-foundry", "anthropic", "local"} {
		p, key := h.profile(family, true)
		pol, grant := h.approve(p)
		v := h.preview(h.admin, finding, p, pol, grant, reviewedContext()+"\nExplicit family: "+family)
		job := h.enqueue(h.admin, v, "native-"+family, 202)
		conclusion := []string{"supported", "contradicted", "inconclusive", "supported"}[index]
		h.native.arm(p, key, v, job.ID, "ok", conclusion, false)
		process(t, h.ctx, w, true)
		got := h.job(h.admin, job.ID)
		h.assertResult(got, p, v, conclusion)
		input, writes := int64(11), int64(0)
		if family == "anthropic" {
			input, writes = 13, 2
		}
		check(t, got.Usage.Known && got.Usage.InputTokens == input && got.Usage.OutputTokens == 7 &&
			got.Usage.CachedInputTokens == 3 && got.Usage.CacheWriteTokens == writes, "actual native usage/cache accounting lost")
		stop := map[string]string{"openai": "completed", "azure-foundry": "completed", "anthropic": "end_turn", "local": "stop"}[family]
		check(t, got.StopReason == stop && h.native.count() == index+1, "native stop reason lost or duplicate provider attempt")
		h.assertReadonly(before, storage)
	}
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, strings.Repeat("x", contextLimit-1)+"\n")
	job := h.enqueue(h.admin, v, "unknown-usage", 202)
	h.native.arm(p, key, v, job.ID, "unknown-usage", "inconclusive", false)
	process(t, h.ctx, w, true)
	got := h.job(h.admin, job.ID)
	h.assertResult(got, p, v, "inconclusive")
	check(t, !got.Usage.Known && h.native.count() == 5, "missing provider usage became invented known zero spending")
	h.assertReadonly(before, storage)
	t.Log("AA3 actual four-family protocol/advisory/usage mappings and unknown usage completed; no live-model certification")
}

func TestAA4DistinctRejectedOutputAndProviderFailuresNeverRetryOrMutateFindings(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	w := h.worker(h.configForWorker("failure-boundaries"))
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	for index, scenario := range []struct{ family, mode string }{
		{"openai", "auth"}, {"openai", "rate-limited"}, {"azure-foundry", "provider"},
		{"openai", "schema"}, {"openai", "refusal"}, {"local", "grounding"},
		{"anthropic", "tool-output"}, {"local", "response-limit"},
	} {
		p, key := h.profile(scenario.family, true)
		pol, grant := h.approve(p)
		v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
		job := h.enqueue(h.admin, v, "failure-"+scenario.mode, 202)
		h.native.arm(p, key, v, job.ID, scenario.mode, "supported", false)
		process(t, h.ctx, w, true)
		got := h.job(h.admin, job.ID)
		check(t, got.State == "failed" && got.Result == nil && got.Failure != nil &&
			got.Failure.Code == scenario.mode && !got.Failure.Retryable && got.Attempts == 1 &&
			got.DispatchStartedAt != nil && got.AdvisoryOnly && got.RequestID == "fixture-native-request",
			"schema/refusal/grounding/tool/auth/rate/provider/bounds failure was collapsed, successful or retried")
		check(t, got.RequestedModel == p.Model && got.Deployment == p.Deployment &&
			got.RetryAfterMillis == 3000, "safe requested model/deployment/request/retry metadata lost on failure")
		if scenario.mode == "schema" || scenario.mode == "refusal" || scenario.mode == "grounding" || scenario.mode == "tool-output" {
			check(t, got.Usage.Known && got.Usage.OutputTokens == 7 && got.ReturnedModel == "fixture-returned-model" && got.StopReason != "",
				"actual usage/model/stop metadata was discarded when output was rejected")
		}
		process(t, h.ctx, w, false)
		check(t, h.native.count() == index+1, "SDK/service retry, fallback or tool execution occurred")
		h.assertReadonly(before, storage)
	}
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
	job := h.enqueue(h.admin, v, "native-timeout", 202)
	held := h.native.arm(p, key, v, job.ID, "ok", "supported", true)
	process(t, h.ctx, w, true)
	event(t, h.ctx, held.cancelled, "native request deadline cancellation")
	timedOut := h.job(h.admin, job.ID)
	check(t, (timedOut.State == "failed" || timedOut.State == "uncertain") && timedOut.Result == nil &&
		timedOut.Failure != nil && timedOut.Failure.Code == "timeout" && timedOut.Attempts == 1 &&
		timedOut.DispatchState == "possibly-sent" && !timedOut.Usage.Known && h.native.count() == 9,
		"deadline was collapsed into successful/unsent/retried or known-zero-use output")
	h.assertReadonly(before, storage)
	t.Log("AA4 explicit distinct failures, retained safe usage/provenance and single-attempt/no-mutation behavior completed")
}

func TestAA5HeldNativeIOReleasesSingleSQLPoolAndCancellationIsPotentiallySent(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
	job := h.enqueue(h.admin, v, "held-native", 202)
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	script := h.native.arm(p, key, v, job.ID, "ok", "supported", true)
	config := h.configForWorker("independent-single-pool")
	w := h.worker(config)
	must(t, "close core while independent worker operates", h.core.Close())
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() { _, err := w.ProcessNext(ctx); done <- err }()
	event(t, h.ctx, script.entered, "actual native request")
	h.readMarker(job.ID)
	pingCtx, stop := context.WithTimeout(h.ctx, time.Second)
	must(t, "worker one-slot pool responsive during held native request", w.Ping(pingCtx))
	stop()
	cancel()
	cancelCtx, stop := context.WithTimeout(h.ctx, time.Second)
	event(t, cancelCtx, script.cancelled, "context cancellation observed by actual native HTTP server")
	select {
	case err := <-done:
		check(t, err == nil || errors.Is(err, context.Canceled), "caller cancellation became an unrelated worker error")
	case <-cancelCtx.Done():
		t.Fatal("worker did not release its cancelled native operation")
	}
	stop()
	h.open()
	got := h.job(h.admin, job.ID)
	check(t, (got.State == "cancelled" || got.State == "uncertain") && got.Result == nil && got.Attempts == 1 &&
		got.DispatchState == "possibly-sent" && got.DispatchStartedAt != nil,
		"post-dispatch cancellation claimed unsent data or assessment success")
	fresh := h.worker(h.configForWorker("fresh-no-repeat"))
	process(t, h.ctx, fresh, false)
	check(t, h.native.count() == 1, "fresh worker repeated cancelled/uncertain inference")
	h.assertReadonly(before, storage)
	t.Log("AA5 committed dispatch marker, independent one-slot responsiveness and truthful native cancellation completed")
}

func TestAA6CurrentAuthorityChangesActivelyCancelHeldNativeResponses(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	writer := h.addUser("analyst")
	w := h.worker(h.configForWorker("active-authority"))
	for index, change := range []string{"grant-revoke", "grant-expiry", "profile", "policy", "requester"} {
		p, key := h.profile("openai", true)
		pol, grant := h.approve(p)
		if change == "grant-expiry" {
			grant = h.json(h.admin, "POST", grantsPath, map[string]any{"profileId": p.ID, "profileRevision": p.Revision,
				"policyRevision": pol.Revision, "destination": p.Endpoint, "task": validityTask, "dataClass": evidenceClass,
				"expiresAt": h.now().Add(5 * time.Second).Format(time.RFC3339Nano)}, 201).Grant
		}
		v := h.preview(writer, finding, p, pol, grant, reviewedContext())
		job := h.enqueue(writer, v, "authority-"+change, 202)
		script := h.native.arm(p, key, v, job.ID, "ok", "supported", true)
		done := make(chan error, 1)
		go func() { _, err := w.ProcessNext(h.ctx); done <- err }()
		event(t, h.ctx, script.entered, "held authorized native request")
		before, storage := h.domainSnapshot(), h.storageCalls.Load()
		switch change {
		case "grant-revoke":
			h.json(h.admin, "POST", grantsPath+"/"+grant.ID+"/revoke", map[string]any{}, 200)
		case "grant-expiry":
			h.offset.Add(int64(6 * time.Second))
		case "profile":
			h.json(h.admin, "PATCH", profilesPath+"/"+p.ID, map[string]any{"name": "Changed current profile"}, 200)
		case "policy":
			h.json(h.admin, "PATCH", policyPath, map[string]any{"mode": "disabled"}, 200)
		case "requester":
			h.json(h.admin, "PATCH", "/api/v1/users/"+writer.ID, map[string]any{"role": "viewer"}, 200)
		}
		ctx, stop := context.WithTimeout(h.ctx, time.Second)
		event(t, ctx, script.cancelled, "active authority cancellation before the longer request deadline")
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("active invalidation did not settle the owned operation")
		}
		stop()
		got := h.job(h.admin, job.ID)
		check(t, got.State == "invalidated" && got.Result == nil && got.Failure != nil &&
			got.Failure.Code != "" && !got.Failure.Retryable && got.Attempts == 1 && got.DispatchState == "possibly-sent",
			"current grant/profile/policy/role authority loss committed success or claimed unsent")
		check(t, h.native.count() == index+1, "invalidated request was retried or fell back")
		h.assertReadonly(before, storage)
	}
	requester, secondAdmin := h.addUser("analyst"), h.addUser("admin")
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(requester, finding, p, pol, grant, reviewedContext())
	job := h.enqueue(requester, v, "issuer-demotion-is-not-revocation", 202)
	script := h.native.arm(p, key, v, job.ID, "ok", "inconclusive", true)
	done := make(chan error, 1)
	go func() { _, err := w.ProcessNext(h.ctx); done <- err }()
	event(t, h.ctx, script.entered, "held request under durable workspace grant")
	h.json(secondAdmin, "PATCH", "/api/v1/users/"+h.admin.ID, map[string]any{"role": "analyst"}, 200)
	script.allowResponse()
	select {
	case err := <-done:
		must(t, "finish still-current requester grant after issuer demotion", err)
	case <-h.ctx.Done():
		t.Fatal("durable workspace grant did not finish")
	}
	h.assertResult(h.job(requester, job.ID), p, v, "inconclusive")
	check(t, h.native.count() == 6, "issuer demotion created a retry or incorrect grant revocation")
	t.Log("AA6 actual grant revoke/expiry/profile/policy/requester changes cancelled held HTTP and denied final success")
}

func TestAA7SharedAdmissionExpiredDispatchAndStaleFenceAcrossWorkersAndProfiles(t *testing.T) {
	h := newHarness(t, false)
	finding := h.seed()
	p, key := h.profile("openai", true)
	pol, grant := h.approve(p)
	v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
	first := h.enqueue(h.admin, v, "fenced-first", 202)
	p2, key2 := h.profile("anthropic", true)
	pol2, grant2 := h.approve(p2)
	v2 := h.preview(h.admin, finding, p2, pol2, grant2, reviewedContext()+"\nAnother reviewed profile shares the pool.")
	second := h.enqueue(h.admin, v2, "shared-second", 202)
	script := h.native.arm(p, key, v, first.ID, "ok", "supported", true)
	config := h.configForWorker("owner-a")
	config.LeaseDuration, config.RequestWindow, config.RequestsPerWindow = 300*time.Millisecond, 3*time.Second, 1
	gate := &finalizationGate{armed: &script.replyReady, entered: make(chan struct{}), release: make(chan struct{})}
	config.Database.QueryTracer = gate
	a := h.worker(config)
	config.WorkerID, config.Database.QueryTracer = "owner-b", nil
	b := h.worker(config)
	bad := config
	bad.WorkerID, bad.RequestsPerWindow = "incoherent", 2
	wrong, err := Production.OpenWorker(h.ctx, bad)
	if wrong != nil {
		_ = wrong.Close()
	}
	check(t, err != nil && wrong == nil, "another instance obtained a private full quota or changed shared limits")
	bad = config
	bad.WorkerID, bad.Scope = "different-scope", config.Scope+"-another-private-pool"
	wrong, err = Production.OpenWorker(h.ctx, bad)
	if wrong != nil {
		_ = wrong.Close()
	}
	check(t, err != nil && wrong == nil, "one database silently admitted a second private quota scope")
	before, storage := h.domainSnapshot(), h.storageCalls.Load()
	done := make(chan error, 1)
	go func() { _, err := a.ProcessNext(h.ctx); done <- err }()
	event(t, h.ctx, script.entered, "first shared-scope native request")
	process(t, h.ctx, b, false)
	ctx, stop := context.WithTimeout(h.ctx, time.Second)
	must(t, "capacity waiting did not retain the other worker's SQL slot", b.Ping(ctx))
	stop()
	check(t, h.job(h.admin, second.ID).State == "queued" && h.native.count() == 1, "cross-profile concurrency admitted another request")
	script.allowResponse()
	event(t, h.ctx, gate.entered, "real SQL begin paused after native response scheduling")
	ctx, stop = context.WithTimeout(h.ctx, 2*time.Second)
	wait(t, ctx, "actual database lease expires without SQL mutation", func() bool {
		var expired bool
		err := h.db.QueryRow(ctx, "SELECT lease_until<=clock_timestamp() FROM "+h.table("assessment_jobs")+" WHERE id=$1", first.ID).Scan(&expired)
		must(t, "readonly database lease probe", err)
		return expired
	})
	stop()
	process(t, h.ctx, b, true)
	recovered := h.job(h.admin, first.ID)
	check(t, recovered.State == "uncertain" && recovered.Result == nil && recovered.Attempts == 1 &&
		recovered.DispatchState == "possibly-sent", "expired dispatch was claimed unsent or replayed")
	close(gate.release)
	select {
	case <-done:
	case <-h.ctx.Done():
		t.Fatal("stale owner failed to return after its real query resumed")
	}
	check(t, sameAssessment(h.job(h.admin, first.ID), recovered), "stale owner/fence overwrote recovered terminal state")
	process(t, h.ctx, b, false)
	check(t, h.native.count() == 1 && h.job(h.admin, second.ID).State == "queued", "request-window budget reset per worker/profile")
	h.native.arm(p2, key2, v2, second.ID, "ok", "contradicted", false)
	ctx, stop = context.WithTimeout(h.ctx, 5*time.Second)
	wait(t, ctx, "shared database request window permits the still-unattempted job", func() bool {
		worked, err := b.ProcessNext(ctx)
		must(t, "poll legitimate capacity without an occupied connection", err)
		return worked
	})
	stop()
	h.assertResult(h.job(h.admin, second.ID), p2, v2, "contradicted")
	check(t, h.native.count() == 2, "shared admission or expired-dispatch recovery duplicated inference")
	h.assertReadonly(before, storage)
	t.Log("AA7 shared cross-profile/replica concurrency+window, lease recovery and stale-fence rejection completed")
}
