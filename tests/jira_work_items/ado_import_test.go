//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func (h *adoHarness) adoCollectedReport(who actor, source adoSource, selection adoSelection, token string, raw []byte, key string) (adoCollection, app.SourceCollectionRecord) {
	h.t.Helper()
	h.native.arm(*source.AzureDevOps, selection, token, raw, "", "")
	job := h.adoQueue(who, source.ID, selection, key, 202)
	worker := h.adoWorker(h.adoWorkerConfig())
	adoStep(h.t, h.ctx, worker, true)
	must(h.t, "close independent collection step", worker.Close())
	done := h.adoJob(who, job.ID)
	check(h.t, done.State == "succeeded" && done.AssetID != nil && done.CollectedAt != nil, "selected report was not actually collected")
	return done, h.adoReport(who, done.ID)
}

func TestADOA3ExplicitSARIFIntakeAndCanonicalFindings(t *testing.T) {
	t.Run("explicit-review-replay-and-provenance", func(t *testing.T) {
		h := newADO(t, true)
		writer, viewer := h.user("analyst"), h.user("viewer")
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		raw := adoSARIF(false, false)
		job, report := h.adoCollectedReport(h.admin, source, selection, token, raw, "collect-first")
		check(t, h.json(writer, "GET", "/api/v1/work", nil, 200).Total == 0 && h.raw.puts.Load() == 0,
			"collect/open report automatically created findings or raw intake")
		h.request(h.ctx, viewer, "GET", adoEvidencePath(job.ID, report.ID), nil, 200)
		same(t, "opening evidence auto-enqueued intake", h.rows("SELECT id FROM "+h.table("imports")), []string{})
		beforeInvalid := h.reader.calls.Load()
		path := adoBridgePath(job.ID, report.ID)
		input := adoBridgeInput()
		h.json(actor{}, "POST", path, input, 401)
		h.json(viewer, "POST", path, input, 403)
		h.request(h.ctx, writer, "POST", path, input, 403, "Origin", "https://not-approved.invalid")
		foreign := h.otherWorkspace()
		h.json(foreign, "POST", path, input, 404)
		for _, field := range []string{"apiVersion", "format", "scope", "sourceStatus", "scanKind", "completeness"} {
			bad := adoBridgeInput()
			delete(bad, field)
			h.json(writer, "POST", path, bad, 400)
		}
		for _, field := range []string{"report", "assetId", "sourceId", "scanId", "sourceScanAt", "collectedAt", "submittedBy", "idempotencyKey"} {
			bad := adoBridgeInput()
			bad[field] = "not-caller-authority"
			h.json(writer, "POST", path, bad, 400)
		}
		for _, format := range []string{"auto", "trivy", "generic-json"} {
			bad := adoBridgeInput()
			bad["format"] = format
			h.json(writer, "POST", path, bad, 415)
		}
		badMapping := adoBridgeInput()
		badMapping["mapping"] = object{"sourceFindingId": "id", "title": "title"}
		h.json(writer, "POST", path, badMapping, 400)
		for _, record := range h.adoRecords(writer, job.ID) {
			if record.Kind != "report" {
				h.json(writer, "POST", adoBridgePath(job.ID, record.ID), input, 400)
			}
		}
		same(t, "rejected review created an import", h.rows("SELECT id FROM "+h.table("imports")), []string{})
		check(t, h.reader.calls.Load() == beforeInvalid && h.raw.puts.Load() == 0,
			"denied/invalid bridge review fetched or republished report bytes")
		body, _ := h.request(h.ctx, writer, "POST", path, input, 202)
		queued := decoded[reply](t, body).Import
		sourceID, scanID := adoNativeIdentity(target, selection)
		check(t, queued.ID != "" && queued.RunID != "" && queued.State == "queued" &&
			queued.AssetID == *job.AssetID && queued.SourceID == sourceID && queued.ScanID == scanID &&
			queued.SourceScanAt == nil && queued.CollectedAt.Equal(*job.CollectedAt) &&
			queued.ReportDigest == "sha256:"+digest(raw) && queued.Format == "sarif" &&
			queued.SourceStatus == "succeeded" && queued.ScanKind == "delta" && queued.Completeness == "unknown",
			"bridge did not bind exact collected bytes/native identity/user-reviewed scan semantics")
		check(t, !bytes.Contains(body, []byte(adoReportCanary)) && !bytes.Contains(body, raw), "mutation ACK exposed raw report")
		same(t, "bridge substituted the collector for the current import requester",
			h.rows("SELECT submitted_by FROM "+h.table("imports")+" WHERE id=$1", queued.ID), []string{writer.ID})
		same(t, "queued bridge replay created another intake", h.json(writer, "POST", path, input, 202).Import, queued)
		check(t, h.json(writer, "GET", "/api/v1/work", nil, 200).Total == 0, "core processed imports inline")
		original, _ := h.request(h.ctx, writer, "GET", "/api/v1/imports/"+queued.ID+"/evidence", nil, 200)
		check(t, bytes.Equal(original, raw), "bridge changed CRLF/Unicode/trailing UTF-8 report bytes")
		h.adoIngest()
		finished := h.json(writer, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import
		check(t, finished.State == "succeeded" && finished.ObservationCount == 1, "independent ingestion did not produce a canonical finding")
		work := h.json(viewer, "GET", "/api/v1/work", nil, 200)
		check(t, work.Total == 1 && len(work.Items) == 1, "canonical ADO finding is not visible in Work")
		finding := h.finding(viewer, decoded[struct{ ID string }](t, work.Items[0]).ID)
		check(t, finding.AssetID == *job.AssetID && finding.SourceScanAt == nil && !finding.VerifiedResolution &&
			finding.WorkflowState != "resolved" && len(finding.Observations) == 1, "canonical finding lost provenance or fabricated closure")
		observation := finding.Observations[0]
		check(t, observation.SourceID == sourceID && observation.ScanID == scanID && observation.RunID == queued.RunID &&
			observation.SourceScanAt == nil && observation.EvidenceDigest == queued.ReportDigest &&
			observation.SourceLocation.URI == "src/selected.go" && observation.SourceLocation.Line == 17,
			"actual finding history lost native report/run/location provenance")
		h.json(h.admin, "POST", path, input, 409)
		for _, field := range []string{"scope", "sourceStatus", "scanKind", "completeness"} {
			bad := adoBridgeInput()
			switch field {
			case "scope":
				bad[field] = object{"id": "selected-security-scope", "revision": "source-revision-1", "branch": "refs/heads/other"}
			case "sourceStatus":
				bad[field] = "failed"
			case "scanKind":
				bad[field] = "full"
			case "completeness":
				bad[field] = "complete"
			}
			h.json(writer, "POST", path, bad, 409)
		}
		rawPuts := h.raw.puts.Load()
		duplicateSource := h.adoSource(h.admin, target, token)
		repeated, repeatedRecord := h.adoCollectedReport(h.admin, duplicateSource, selection, token, raw, "collect-second")
		check(t, repeated.ID != job.ID && repeatedRecord.ID != report.ID, "collection attempts were not distinct durable observations")
		replay := h.json(writer, "POST", adoBridgePath(repeated.ID, repeatedRecord.ID), input, 200).Import
		same(t, "same immutable report created a second native scan or reset accepted time", replay, finished)
		check(t, h.raw.puts.Load() == rawPuts, "accepted replay republished raw intake")
		h.adoIngest()
		same(t, "replayed native report duplicated a run", h.rows("SELECT id FROM "+h.table("imports")), []string{queued.ID})
		same(t, "replayed native report changed finding history", h.finding(viewer, finding.ID), finding)
		h.json(writer, "POST", adoBridgePath(job.ID, repeatedRecord.ID), input, 404)
		h.reopen()
		same(t, "native intake replay did not survive core reopen", h.json(writer, "POST", path, input, 200).Import, finished)
	})

	t.Run("same-native-identity-mutated-bytes", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		first, report := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(true, false), "first")
		input := adoBridgeInput()
		accepted := h.json(h.admin, "POST", adoBridgePath(first.ID, report.ID), input, 202).Import
		check(t, accepted.SourceScanAt != nil && accepted.SourceScanAt.Format(time.RFC3339) == "2026-09-29T09:00:00Z",
			"declared scan time was replaced by build/artifact/import time")
		changed := bytes.ReplaceAll(adoSARIF(true, false), []byte("Selected ADO report finding"), []byte("Changed immutable report"))
		second, mutation := h.adoCollectedReport(h.admin, source, selection, token, changed, "second")
		h.json(h.admin, "POST", adoBridgePath(second.ID, mutation.ID), input, 409)
		same(t, "mutated same build/report replaced its first accepted intake", h.json(h.admin, "GET", "/api/v1/imports/"+accepted.ID, nil, 200).Import, accepted)
		same(t, "mutated same identity created another import", h.rows("SELECT id FROM "+h.table("imports")), []string{accepted.ID})
	})

	t.Run("empty-delta-does-not-close", func(t *testing.T) {
		h := newADO(t, true)
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		first, report := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(true, false), "first")
		input := adoBridgeInput()
		input["scanKind"], input["completeness"] = "full", "complete"
		h.json(h.admin, "POST", adoBridgePath(first.ID, report.ID), input, 202)
		h.adoIngest()
		work := h.json(h.admin, "GET", "/api/v1/work", nil, 200)
		check(t, len(work.Items) == 1, "initial explicit full import did not produce a finding")
		before := h.finding(h.admin, decoded[struct{ ID string }](t, work.Items[0]).ID)
		selection.BuildID = "82"
		second, empty := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(false, true), "second")
		partial := adoBridgeInput()
		partial["sourceStatus"] = "failed"
		next := h.json(h.admin, "POST", adoBridgePath(second.ID, empty.ID), partial, 202).Import
		check(t, next.SourceStatus == "failed" && next.ScanKind == "delta" && next.Completeness == "unknown",
			"successful build inferred scan success/full completeness")
		h.adoIngest()
		check(t, h.json(h.admin, "GET", "/api/v1/imports/"+next.ID, nil, 200).Import.State == "succeeded",
			"valid empty explicitly partial scan was not processed")
		after := h.finding(h.admin, before.ID)
		check(t, after.WorkflowState == before.WorkflowState && after.SourceState == before.SourceState &&
			after.Disposition == before.Disposition && !after.VerifiedResolution, "collection/empty delta inferred finding closure")
	})

	t.Run("accepted-time-window-never-slides", func(t *testing.T) {
		h := newADO(t, false)
		var clock atomic.Int64
		clock.Store(time.Now().UTC().UnixMicro())
		h.cfg.Now = func() time.Time { return time.UnixMicro(clock.Load()).UTC() }
		h.cfg.SessionTTL = 48 * time.Hour
		h.open()
		h.enroll()
		target, selection, token := adoDefaultTarget(), adoDefaultSelection(), secret(t)
		source := h.adoSource(h.admin, target, token)
		job, record := h.adoCollectedReport(h.admin, source, selection, token, adoSARIF(false, false), "first")
		path, input := adoBridgePath(job.ID, record.ID), adoBridgeInput()
		first := h.json(h.admin, "POST", path, input, 202).Import
		clock.Store(first.ImportedAt.Add(23 * time.Hour).UnixMicro())
		same(t, "replay reset first accepted timestamps", h.json(h.admin, "POST", path, input, 202).Import, first)
		clock.Store(first.ImportedAt.Add(24 * time.Hour).UnixMicro())
		expired := h.json(h.admin, "POST", path, input, 409)
		check(t, expired.Error != nil && expired.Error.Code == "replay-expired", "existing 24-hour replay policy was bypassed or slid")
		same(t, "expired replay modified stored acceptance", h.json(h.admin, "GET", "/api/v1/imports/"+first.ID, nil, 200).Import, first)
	})
}
