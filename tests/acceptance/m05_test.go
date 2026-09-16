//go:build integration

package acceptance

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func sarif(t *testing.T, change func(object, object)) []byte {
	t.Helper()
	var report object
	ok(t, "decode owned SARIF fixture", json.Unmarshal(fixture(t, "sarif.json"), &report))
	run := report["runs"].([]any)[0].(map[string]any)
	change(run, run["results"].([]any)[0].(map[string]any))
	return encode(t, report)
}

func TestM05_QueuedSARIFSurvivesReopenAndProducesExactEvidence(t *testing.T) {
	h := newHarness(t, true)
	equal(t, "new workspace is genuinely empty", len(h.work(h.admin, "")), 0)
	a := h.asset(h.admin, "Owned repository", &h.admin.user.ID)
	raw := fixture(t, "sarif.json")
	input := h.input(a.ID, "sarif", raw)
	queued := h.upload(input)
	equal(t, "new import is queued, not successful", queued.State, "queued")
	equal(t, "parsing is not inline", len(h.work(h.admin, "")), 0)
	h.restart()
	equal(t, "queue persisted", h.json(h.admin, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import.State, "queued")
	equal(t, "reopen did not pretend processing completed", len(h.work(h.admin, "")), 0)
	done := h.finish(queued.ID, "succeeded")
	equal(t, "report format", done.Format, "sarif")
	equal(t, "source identity", done.SourceID, "acceptance-source")
	equal(t, "scan identity", done.ScanID, "scan-1")
	equal(t, "run identity survives processing", done.RunID, queued.RunID)
	equal(t, "scope retained", done.Scope, scope{"owned-repository", "1", "refs/heads/main"})
	equal(t, "report digest", done.ReportDigest, digest(raw))
	equal(t, "reconciled observation count", done.ObservationCount, 1)
	sameTime(t, "source scan time", done.SourceScanAt, &sourceTime)
	collected := sourceTime.Add(time.Hour)
	sameTime(t, "collection time", &done.CollectedAt, &collected)
	if done.ImportedAt.IsZero() || done.ImportedAt.Equal(sourceTime) {
		t.Fatal("import time must be recorded independently from source scan time")
	}
	work := h.work(h.admin, "")
	equal(t, "one usable work item", len(work), 1)
	w := work[0]
	equal(t, "source title", w.Title, "Synthetic transport setting")
	equal(t, "normalized severity", w.Severity, "medium")
	equal(t, "asset name", w.AssetName, a.Name)
	equal(t, "human workflow default", w.WorkflowState, "open")
	if w.OwnerName == nil || *w.OwnerName != h.admin.user.Name {
		t.Fatal("work item lost explicit asset ownership")
	}
	sameTime(t, "work source time", w.SourceScanAt, &sourceTime)
	f := h.finding(h.admin, w.ID)
	equal(t, "detail identity", f.ID, w.ID)
	equal(t, "detail asset", f.AssetID, a.ID)
	equal(t, "detail workspace", f.WorkspaceID, h.admin.workspace)
	equal(t, "source description", f.Description, "Synthetic configuration description.")
	equal(t, "source remediation", f.Remediation, "Review the synthetic setting.")
	equal(t, "evidence text", f.Evidence.Text, "Synthetic configuration requires review.")
	equal(t, "evidence source", f.Evidence.SourceLabel, "Acceptance SARIF")
	equal(t, "not independently verified", f.Evidence.VerificationState, "not-run")
	equal(t, "one observation in detail", len(f.Observations), 1)
	equal(t, "source severity was not overwritten", f.Observations[0].SourceSeverity, "warning")
	equal(t, "observation points at immutable report", f.Observations[0].EvidenceDigest, digest(raw))
	if f.ScopeLabel == "" {
		t.Fatal("finding detail needs human-readable scope")
	}
	evidence := h.request(h.admin, "GET", "/api/v1/imports/"+done.ID+"/evidence", nil, 200)
	if !bytes.Equal(evidence.Body.Bytes(), raw) {
		t.Fatal("authorized evidence download changed original report bytes")
	}
	h.services.storedReport(t, h.admin.workspace, raw)
}

func TestM05_M01QueryAndCSVExportHaveIdenticalMembership(t *testing.T) {
	h := newHarness(t, true)
	_, _, first := h.seed()
	secondAsset := h.asset(h.admin, "Secondary repository", nil)
	raw := sarif(t, func(run, result object) {
		driver := run["tool"].(map[string]any)["driver"].(map[string]any)
		driver["rules"].([]any)[0].(map[string]any)["shortDescription"] = object{"text": "Synthetic build flag"}
		result["guid"], result["level"] = "22222222-2222-4222-8222-222222222222", "note"
	})
	input := h.input(secondAsset.ID, "sarif", raw)
	input["sourceId"] = "second-source"
	h.finish(h.upload(input).ID, "succeeded")
	all := h.work(h.admin, "")
	equal(t, "two distinct findings", len(all), 2)
	secondID := all[0].ID
	if secondID == first.ID {
		secondID = all[1].ID
	}
	for _, query := range []struct {
		q   string
		ids []string
	}{
		{"TRANSPORT", []string{first.ID}}, {"secondary repository", []string{secondID}},
		{"synthetic admin", []string{first.ID}}, {"no fixture match", nil}, {"", []string{first.ID, secondID}},
	} {
		got := h.work(h.admin, query.q)
		equal(t, "q matches expected fields", len(got), len(query.ids))
		byID := map[string]workItem{}
		for _, w := range got {
			byID[w.ID] = w
		}
		for _, id := range query.ids {
			if _, present := byID[id]; !present {
				t.Fatal("q membership differs from M01 title/assetName/ownerName semantics")
			}
		}
		rr := h.request(h.admin, "GET", "/api/v1/work/export?format=csv&q="+url.QueryEscape(query.q), nil, 200)
		if !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/csv") {
			t.Fatal("CSV export must identify its media type")
		}
		rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
		ok(t, "decode CSV export", err)
		equal(t, "export count equals list count", len(rows), len(got)+1)
		columns := map[string]int{}
		for i, name := range rows[0] {
			columns[name] = i
		}
		for _, name := range []string{"id", "title", "assetName", "severity", "ownerName", "workflowState", "sourceScanAt", "collectedAt", "importedAt"} {
			if _, present := columns[name]; !present {
				t.Fatalf("CSV is missing the M01 %s column", name)
			}
		}
		for _, row := range rows[1:] {
			w, present := byID[row[columns["id"]]]
			if !present {
				t.Fatal("export contains a duplicate or nonmatching finding")
			}
			equal(t, "export title", row[columns["title"]], w.Title)
			equal(t, "export asset", row[columns["assetName"]], w.AssetName)
			equal(t, "export severity", row[columns["severity"]], w.Severity)
			equal(t, "export workflow", row[columns["workflowState"]], w.WorkflowState)
			owner := ""
			if w.OwnerName != nil {
				owner = *w.OwnerName
			}
			equal(t, "export owner", row[columns["ownerName"]], owner)
			scanned, err := time.Parse(time.RFC3339, row[columns["sourceScanAt"]])
			ok(t, "parse exported source time", err)
			sameTime(t, "export scan time", &scanned, w.SourceScanAt)
			for field, want := range map[string]time.Time{"collectedAt": w.CollectedAt, "importedAt": w.ImportedAt} {
				got, err := time.Parse(time.RFC3339, row[columns[field]])
				ok(t, "parse exported "+field, err)
				sameTime(t, "export "+field, &got, &want)
			}
			delete(byID, w.ID)
		}
		equal(t, "all filtered items exported once", len(byID), 0)
	}
}

func TestM05_IntakeAndEvidenceRejectUnauthorizedAndMalformedInput(t *testing.T) {
	h := newHarness(t, true)
	input, run, f := h.seed()
	for _, path := range []string{"/api/v1/work", "/api/v1/work/export?format=csv", "/api/v1/findings/" + f.ID,
		"/api/v1/imports/" + run.ID, "/api/v1/imports/" + run.ID + "/evidence"} {
		h.denied(actor{}, "GET", path, nil, 401, "unauthorized")
	}
	h.denied(actor{}, "POST", "/api/v1/imports", input, 401, "unauthorized")
	viewer := h.addUser(h.admin, "viewer")
	h.denied(viewer, "POST", "/api/v1/imports", input, 403, "forbidden")
	equal(t, "viewer can inspect findings", h.finding(viewer, f.ID).ID, f.ID)
	h.request(viewer, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200)
	other := h.addUser(h.addWorkspace(), "admin")
	equal(t, "foreign workspace has no findings", len(h.work(other, "")), 0)
	for _, path := range []string{"/api/v1/findings/" + f.ID, "/api/v1/imports/" + run.ID, "/api/v1/imports/" + run.ID + "/evidence"} {
		h.denied(other, "GET", path, nil, 404, "not-found")
	}
	h.denied(other, "POST", "/api/v1/imports", input, 404, "not-found")
	h.decode(h.request(h.admin, "POST", "/api/v1/imports", []byte("{"), 400))
	bad := h.input(f.AssetID, "unsupported", []byte("{}"))
	h.denied(h.admin, "POST", "/api/v1/imports", bad, 415, "unsupported-format")
	bad["format"], bad["report"] = "sarif", strings.Repeat("x", int(h.services.cfg.MaxUploadBytes)+1)
	h.denied(h.admin, "POST", "/api/v1/imports", bad, 413, "too-large")
	bad["scanId"], bad["report"] = "malformed-report", `{"version":"2.1.0","runs":[`
	failed := h.finish(h.upload(bad).ID, "failed")
	if failed.Failure == nil || failed.Failure.Code != "invalid-report" || failed.Failure.Message == "" {
		t.Fatal("malformed report must expose a parser failure, not successful empty results")
	}
	equal(t, "rejected intake did not change work", len(h.work(h.admin, "")), 1)
}
