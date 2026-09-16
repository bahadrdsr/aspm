//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestSecurityM05_TwoStalledS3PublicationsDoNotOccupySQLPool(t *testing.T) {
	h, storage := securityHarness(t, 2, nil)
	_, _, original := h.seed()
	var inputs []object
	var uploads []*securityHTTP
	storage.arm("PUT", 2)
	for i := 0; i < 2; i++ {
		raw := sarif(t, func(_ object, result object) {
			result["guid"] = fmt.Sprintf("33333333-3333-4333-8333-%012d", i+1)
		})
		input := h.input(original.AssetID, "sarif", raw)
		input["sourceId"] = fmt.Sprintf("stalled-owned-source-%d", i)
		inputs = append(inputs, input)
		uploads = append(uploads, securityRequest(h, h.admin, "POST", "/api/v1/imports", input, 12*time.Second, ""))
	}
	securityEntered(t, storage.gate, "first actual S3 PUT response")
	securityEntered(t, storage.gate, "second actual S3 PUT response")
	for _, input := range inputs {
		h.services.storedReport(t, h.admin.workspace, []byte(input["report"].(string)))
	}
	session := securityRequest(h, h.admin, "GET", "/api/v1/session", nil, 2*time.Second, "")
	work := securityRequest(h, h.admin, "GET", "/api/v1/work", nil, 2*time.Second, "")
	for name, pending := range map[string]*securityHTTP{"session": session, "Work": work} {
		response := pending.wait(t)
		if response.Code != 200 {
			t.Errorf("%s read could not finish while TWO S3 PUT acknowledgements were stalled under MaxConnections=2: status %d", name, response.Code)
		} else {
			h.decode(response)
		}
	}
	storage.gate.open()
	for i, pending := range uploads {
		response := pending.wait(t)
		equal(t, "publication eventually acknowledged", response.Code, http.StatusAccepted)
		queued := h.decode(response).Import
		done := h.finish(queued.ID, "succeeded")
		evidence := h.request(h.admin, "GET", "/api/v1/imports/"+done.ID+"/evidence", nil, 200)
		if !bytes.Equal(evidence.Body.Bytes(), []byte(inputs[i]["report"].(string))) {
			t.Fatal("released publication lost exact evidence")
		}
		replay := h.finish(h.upload(inputs[i]).ID, "succeeded")
		equal(t, "released publication replays idempotently", replay.RunID, done.RunID)
	}
	equal(t, "two real new findings after release", len(h.work(h.admin, "")), 3)
}

func TestSecurityM05_WorkCountAndPageShareOneSnapshotAcrossWorkerCommit(t *testing.T) {
	tracer := &securityTracer{countGate: newSecurityGate()}
	h, _ := securityHarness(t, 4, tracer)
	_, _, prior := h.seed()
	raw := sarif(t, func(_ object, result object) { result["guid"] = "44444444-4444-4444-8444-444444444444" })
	input := h.input(prior.AssetID, "sarif", raw)
	input["sourceId"] = "snapshot-second-source"
	queued := securityUpload(h, h.admin, input)
	request := securityRequest(h, h.admin, "GET", "/api/v1/work", nil, 15*time.Second, "work-list")
	securityEntered(t, tracer.countGate, "real Work count completion")
	h.finish(queued.ID, "succeeded")
	equal(t, "independent worker really committed", len(h.work(h.admin, "")), 2)
	tracer.countGate.open()
	response := request.wait(t)
	equal(t, "interleaved Work response", response.Code, 200)
	result := h.decode(response)
	rows := items[workItem](t, result)
	if result.NextCursor != nil || result.Total != len(rows) || (len(rows) != 1 && len(rows) != 2) {
		t.Fatalf("Work count/page mixed snapshots across a real worker commit: total=%d items=%d", result.Total, len(rows))
	}
	found := false
	for _, row := range rows {
		found = found || row.ID == prior.ID
	}
	if !found {
		t.Fatal("consistent Work response lost the pre-existing finding")
	}
}

func securityRevoke(h *harness, analyst actor) bool {
	h.t.Helper()
	response := securityRequest(h, h.admin, "PATCH", "/api/v1/users/"+analyst.user.ID,
		object{"role": "viewer"}, 3*time.Second, "").wait(h.t)
	if response.Code != 200 {
		h.t.Errorf("required authenticated selected-workspace role revocation is unavailable: status %d", response.Code)
		return false
	}
	equal(h.t, "role update identifies affected user", h.decode(response).User.ID, analyst.user.ID)
	session := h.json(analyst, "GET", "/api/v1/session", nil, 200)
	for _, membership := range session.Workspaces {
		if membership.ID == analyst.workspace {
			equal(h.t, "revocation is committed, not cosmetic", membership.Role, "viewer")
			return true
		}
	}
	h.t.Fatal("role revocation unexpectedly removed the selected read membership")
	return false
}

func securityRevocationInput(h *harness) (actor, imported, securitySnapshot) {
	h.t.Helper()
	input, run, prior := h.seed()
	h.json(h.admin, "PATCH", "/api/v1/findings/"+prior.ID,
		object{"ownerId": h.admin.user.ID, "workflowState": "in-progress"}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+prior.ID+"/notes", object{"text": "Decision preceding revocation."}, 201)
	before := securityRemember(h, h.finding(h.admin, prior.ID), run)
	analyst := h.addUser(h.admin, "analyst")
	input["scanId"], input["sourceScanAt"] = "analyst-run-awaiting-authorization", sourceTime.Add(24*time.Hour)
	input["report"] = string(sarif(h.t, func(_ object, result object) {
		result["level"] = "error"
		result["message"] = object{"text": "Synthetic later observation that must not commit after revocation."}
	}))
	return analyst, securityUpload(h, analyst, input), before
}

func securityRevokedResult(h *harness, queued imported, before securitySnapshot) {
	h.t.Helper()
	result := h.json(h.admin, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import
	if result.State != "failed" || result.Failure == nil || result.Failure.Code != "authorization-revoked" {
		h.t.Errorf("revoked import needs terminal failed/authorization-revoked, never success or retry: state=%s", result.State)
	}
	securityUnchanged(h, before)
	equal(h.t, "revocation did not create additional work", len(h.work(h.admin, "")), 1)
}

func TestSecurityM06_RoleRevocationCancelsActualStorageReadAndPreventsCommit(t *testing.T) {
	h, storage := securityHarness(t, 4, nil)
	analyst, queued, before := securityRevocationInput(h)
	storage.arm("GET", 1)
	worker := securityProcess(t, h.services.ctx, h.app, "")
	readContext := securityEntered(t, storage.gate, "actual analyst import S3 GET")
	if !securityRevoke(h, analyst) {
		worker.cancel()
		storage.gate.open()
		_ = worker.wait(t)
		securityUnchanged(h, before)
		return
	}
	storage.revoked.Store(true)
	select {
	case <-readContext.Done():
	case <-time.After(2 * time.Second):
		t.Error("committed write-role revocation did not cancel the in-flight storage read")
	}
	storage.gate.open()
	_ = worker.wait(t)
	if storage.gets.Load() != 1 || storage.lateBytes.Load() != 0 {
		t.Error("revoked import performed a subsequent storage read or received evidence bytes after revocation")
	}
	storage.revoked.Store(false)
	securityRevokedResult(h, queued, before)
	h.request(analyst, "GET", "/api/v1/imports/"+before.importID+"/evidence", nil, 200)
}

func TestSecurityM06_IndependentWorkerRechecksRoleAtFinalization(t *testing.T) {
	tracer := &securityTracer{beginGate: newSecurityGate()}
	h, storage := securityHarness(t, 4, tracer)
	analyst, queued, before := securityRevocationInput(h)
	workerApp, err := Production.OpenApplication(h.services.ctx, h.services.cfg)
	ok(t, "open independent real worker application", err)
	t.Cleanup(func() {
		tracer.beginGate.open()
		ok(t, "close independent worker application", workerApp.Close())
	})
	storage.arm("", 0)
	worker := securityProcess(t, h.services.ctx, workerApp, "worker-finalize")
	securityEntered(t, tracer.beginGate, "finalization BEGIN after actual source read")
	if !securityRevoke(h, analyst) {
		worker.cancel()
		tracer.beginGate.open()
		_ = worker.wait(t)
		securityUnchanged(h, before)
		return
	}
	tracer.beginGate.open()
	_ = worker.wait(t)
	securityRevokedResult(h, queued, before)
}
