//go:build integration

package assessment_runtime

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestAR5EnvironmentRequestBudgetIncludesHeldNativeHeaders(t *testing.T) {
	for _, row := range []struct {
		name    string
		budget  time.Duration
		success bool
	}{
		{"20s-budget-12s-headers", 20 * time.Second, true},
		{"3s-budget-held-headers", 3 * time.Second, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newFixture(t)
			n := newNative(f)
			core := f.coreService(f.scope, f.key, false)
			trace := map[string]any{
				"case": row.name, "selectedBudget": row.budget.String(), "lease": "30s",
				"advisoryAssertionsReached": false, "terminalAwaitStartedAfterRelease": false,
			}
			var reached, blocked []string
			defer func() {
				trace["reached"], trace["blockedTails"] = reached, blocked
				trace["protectedAPICalls"], trace["nativePOSTs"] = core.calls, n.calls.Load()
				record := encoded(t, trace)
				f.noSecrets(record)
				t.Logf("A3_TRACE %s", record)
			}()
			finding := core.seed()
			p, pol, g, key := core.profile(n, false)
			workerEnvironment(t, f, n, f.key)
			t.Setenv("ASPM_ASSESSMENT_REQUEST_TIMEOUT", row.budget.String())
			t.Setenv("ASPM_ASSESSMENT_LEASE_DURATION", "30s")
			config, err := Production.Environment("assessment")
			must(t, "parse selected actual assessment request budget", err)
			check(t, config.Client != nil, "actual selected environment omitted its client")
			t.Cleanup(config.Client.CloseIdleConnections)
			check(t, config.RequestTimeout == row.budget && config.Client.Timeout == row.budget &&
				config.Lease == 30*time.Second, "actual environment did not retain the selected request/client budget and lease")
			assertClient(t, config)
			workerOnly(t, config)
			trace["parsedBudget"], trace["clientTimeout"] = config.RequestTimeout.String(), config.Client.Timeout.String()
			reached = append(reached, "actual-core-intake-profile-policy-grant", "selected-environment-client-normal-TLS")

			job := core.enqueue(finding, p, pol, g, "selected-headers-"+row.name)
			before, storage := f.domainSnapshot(), f.storeCalls.Load()
			held := n.arm(job, key, true)
			roleStarted := time.Now()
			role := startRole(t, f.ctx, "assessment", config)
			readyContext, stopReady := context.WithTimeout(f.ctx, 8*time.Second)
			event(t, readyContext, held.ready, "native body/privacy checks and committed dispatch marker before headers")
			stopReady()
			readyObserved := time.Now()
			dispatched := core.job(job.ID)
			check(t, dispatched.Attempts == 1 && dispatched.DispatchState == "possibly-sent" &&
				dispatched.DispatchStartedAt != nil, "real API did not retain the committed native dispatch marker")
			trace["jobID"], trace["roleStartedAt"], trace["readyObservedAt"] = job.ID, roleStarted, readyObserved
			trace["dispatchStartedAt"] = dispatched.DispatchStartedAt
			reached = append(reached, "actual-assessment-Run", "validated-native-ready-and-API-marker")
			callsBeforeWait := core.calls
			var cancellationObserved bool
			var cancelledAt time.Time
			if row.success {
				releaseDeadline := readyObserved.Add(12 * time.Second)
				check(t, releaseDeadline.Before(roleStarted.Add(row.budget-time.Second)),
					"BLOCKED: fixture setup left insufficient selected budget for the deliberate header hold")
				trace["headerGateDeadline"] = releaseDeadline
				gate := time.NewTimer(time.Until(releaseDeadline))
				defer gate.Stop()
				select {
				case <-gate.C:
				case <-f.ctx.Done():
					t.Fatal("BLOCKED: original fixture deadline elapsed before the deliberate header gate")
				}
				select {
				case <-held.cancelled:
					cancellationObserved, cancelledAt = true, held.cancelledAt
					check(t, !cancelledAt.IsZero(), "native cancellation occurrence lacks its A2 timestamp")
				default:
				}
				reached = append(reached, "12s-header-gate-without-API-polling")
			} else {
				observation, stopObservation := context.WithDeadline(f.ctx, readyObserved.Add(5*time.Second))
				defer stopObservation()
				deadline, _ := observation.Deadline()
				trace["originalCancellationObservationDeadline"] = deadline
				nativeCancellation(t, observation, held, "selected 3s budget cancels actual held-header request")
				cancellationObserved, cancelledAt = true, held.cancelledAt
				check(t, !cancelledAt.Before(roleStarted.Add(2*time.Second)),
					"selected 3s request was cancelled implausibly early")
				reached = append(reached, "original-A2-occurrence-and-timestamp-cancellation-check")
			}
			check(t, core.calls == callsBeforeWait, "header-stage observation polled the protected API")
			releasedAt := time.Now()
			if row.success {
				check(t, releasedAt.Before(roleStarted.Add(row.budget)),
					"BLOCKED: observer did not release headers inside the selected request budget")
			}
			trace["releasedAt"], trace["nativeCancellationObserved"] = releasedAt, cancellationObserved
			trace["headerGateElapsedMillis"] = releasedAt.Sub(readyObserved).Milliseconds()
			if cancellationObserved {
				trace["nativeCancelledAt"] = cancelledAt
				trace["cancellationAfterRoleStartMillis"] = cancelledAt.Sub(roleStarted).Milliseconds()
				trace["cancellationAfterReadyObservationMillis"] = cancelledAt.Sub(readyObserved).Milliseconds()
				trace["cancellationBeforeRelease"] = !cancelledAt.After(releasedAt)
			}
			held.allow()
			reached = append(reached, "existing-native-response-gate-released")
			trace["terminalAwaitStartedAfterRelease"] = true
			got := core.await(job.ID, "succeeded", "failed", "uncertain", "cancelled", "invalidated")
			trace["terminalState"], trace["completedAt"], trace["dispatchState"] = got.State, got.CompletedAt, got.DispatchState
			if got.Failure != nil {
				trace["failureCode"] = got.Failure.Code
			}
			reached = append(reached, "original-8s-terminal-await-after-release")
			check(t, got.DispatchStartedAt != nil && got.DispatchStartedAt.Equal(*dispatched.DispatchStartedAt) &&
				got.Attempts == 1 && n.calls.Load() == 1 && held.posts.Load() == 1,
				"selected-budget service repeated a native POST or changed the committed marker")
			check(t, reflect.DeepEqual(before, f.domainSnapshot()) && f.storeCalls.Load() == storage,
				"header latency changed finding/configuration/domain data or fetched raw storage")
			f.noSecrets(f.log.data())
			stopRole(t, role)
			check(t, n.calls.Load() == 1, "stopping the selected-budget role produced another native attempt")
			reached = append(reached, "one-POST-unchanged-marker-domain-storage-privacy", "bounded-actual-role-stop")
			if row.success {
				if cancellationObserved && !cancelledAt.After(releasedAt) {
					t.Errorf("selected 20s request cancelled before the owned 12s header release: afterReady=%s afterRoleStart=%s",
						cancelledAt.Sub(readyObserved), cancelledAt.Sub(roleStarted))
				}
				if got.State != "succeeded" {
					blocked = append(blocked, "successful canonical advisory/provenance/usage checks: no succeeded receipt")
					t.Errorf("20s Environment-generated request must accept native headers released after 12s; got state=%s failure=%v",
						got.State, trace["failureCode"])
				} else {
					core.assertAdvisory(got, job)
					trace["advisoryAssertionsReached"] = true
					reached = append(reached, "existing-canonical-advisory-provenance-usage-model-checks")
				}
			} else {
				check(t, got.State == "uncertain" && got.DispatchState == "possibly-sent" && got.Result == nil &&
					got.Failure != nil && got.Failure.Code == "timeout" && !got.Failure.Retryable,
					"selected short deadline must preserve timeout/no-result possibly-sent uncertainty")
				reached = append(reached, "selected-short-timeout-without-result-or-unsent-claim")
			}
		})
	}
}
