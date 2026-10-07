//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

func openRetentionWorker(t *testing.T, h *harness) RetentionWorker {
	t.Helper()
	return openRetentionWorkerWithStorage(t, h, h.services.cfg.Storage, h.services.cfg.ArchiveStorage)
}

func openRetentionWorkerWithStorage(t *testing.T, h *harness, raw, archive StorageConfig) RetentionWorker {
	t.Helper()
	requireRetentionWorker(t)
	worker, err := Production.OpenRetentionWorker(h.services.ctx, RetentionWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: h.services.cfg.ApplicationName + "-retention", MaxConnections: 2,
		RawStorage: raw, ArchiveStorage: archive, Now: h.services.cfg.Now,
		LogOutput: h.services.cfg.LogOutput, Lease: 2 * time.Second,
	})
	ok(t, "open independent retention worker", err)
	if worker.ProcessNext == nil || worker.Close == nil {
		t.Fatal("retention worker binding omitted processing or close")
	}
	return worker
}

func blockingRetentionDeleteProxy(t *testing.T, endpoint, key string) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	target, err := url.Parse(endpoint)
	ok(t, "parse retention storage endpoint", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/"+key) {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			http.Error(w, "cancelled retention delete", http.StatusGatewayTimeout)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, started
}

func drainRetention(t *testing.T, h *harness, worker RetentionWorker) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		ctx, cancel := context.WithTimeout(h.services.ctx, 20*time.Second)
		worked, err := worker.ProcessNext(ctx)
		cancel()
		ok(t, "process one bounded retention item", err)
		if !worked {
			return
		}
	}
	t.Fatal("retention worker did not drain within 100 bounded items")
}

func retentionRawKey(h *harness, run imported) string {
	return h.services.cfg.Storage.Prefix + h.admin.workspace + "/" + run.ID + "/" +
		strings.TrimPrefix(run.ReportDigest, "sha256:")
}

func retentionArchiveObject(t *testing.T, h *harness, kind, id string) (string, []byte) {
	t.Helper()
	prefix := h.services.cfg.ArchiveStorage.Prefix + h.admin.workspace + "/" + kind + "/" + id + "/"
	page, err := h.services.s3.ListObjectsV2(h.services.ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(2),
	})
	ok(t, "list exact owned archive object", err)
	if len(page.Contents) != 1 || aws.ToBool(page.IsTruncated) {
		t.Fatalf("archive resource %s/%s did not have exactly one bounded object", kind, id)
	}
	key := aws.ToString(page.Contents[0].Key)
	result, err := h.services.s3.GetObject(h.services.ctx, &s3.GetObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
	})
	ok(t, "read exact owned archive object", err)
	data, readErr := io.ReadAll(io.LimitReader(result.Body, 1<<20))
	closeErr := result.Body.Close()
	ok(t, "read bounded archive bytes", readErr)
	ok(t, "close archive object", closeErr)
	return key, data
}

func retentionRunByID(t *testing.T, h *harness, who actor, id string, status int) retentionRun {
	t.Helper()
	return h.json(who, "GET", "/api/v1/retention/runs/"+id, nil, status).RetentionRun
}

func findRetentionRunItem(t *testing.T, run retentionRun, resourceKind, resourceID, action string) retentionRunItem {
	t.Helper()
	for _, item := range run.Items {
		if item.ResourceKind == resourceKind && item.ResourceID == resourceID && item.Action == action {
			return item
		}
	}
	t.Fatalf("retention run omitted %s %s %s", action, resourceKind, resourceID)
	return retentionRunItem{}
}

func TestM06_RetentionExecutionArchivesExpiresResumesAndRestores(t *testing.T) {
	h := newHarness(t, true)
	foreign := h.addWorkspace()
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	policy = h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200).RetentionPolicy

	asset := h.asset(h.admin, "Retention execution repository", nil)
	upload := func(source, scan string, report []byte) imported {
		input := h.input(asset.ID, "sarif", report)
		input["sourceId"], input["scanId"] = source, scan
		run := h.finish(h.upload(input).ID, "succeeded")
		equal(t, "new raw evidence availability", run.EvidenceAvailability, "available")
		return run
	}
	eligible := upload("retention-exec-eligible", "retention-exec-eligible-scan", fixture(t, "sarif.json"))
	lateHeld := upload("retention-exec-late-hold", "retention-exec-late-hold-scan", fixture(t, "sarif.json"))
	lateDecision := upload("retention-exec-late-decision", "retention-exec-late-decision-scan", fixture(t, "sarif.json"))
	missing := upload("retention-exec-missing", "retention-exec-missing-scan", fixture(t, "sarif.json"))
	corrupt := upload("retention-exec-corrupt", "retention-exec-corrupt-scan", fixture(t, "sarif.json"))
	sharedReport := sarif(t, func(run, result object) {
		second := object{}
		for key, value := range result {
			second[key] = value
		}
		second["guid"] = "44444444-4444-4444-8444-444444444444"
		second["message"] = object{"text": "Second shared raw-report observation."}
		run["results"] = append(run["results"].([]any), second)
	})
	shared := upload("retention-exec-shared", "retention-exec-shared-scan", sharedReport)
	auditA := upload("retention-exec-audit-a", "retention-exec-audit-a-scan", fixture(t, "sarif.json"))
	auditB := upload("retention-exec-audit-b", "retention-exec-audit-b-scan", fixture(t, "sarif.json"))

	eligibleFinding := retentionFindingForRun(t, h, eligible.RunID)
	lateHeldFinding := retentionFindingForRun(t, h, lateHeld.RunID)
	lateDecisionFinding := retentionFindingForRun(t, h, lateDecision.RunID)
	missingFinding := retentionFindingForRun(t, h, missing.RunID)
	corruptFinding := retentionFindingForRun(t, h, corrupt.RunID)
	auditAFinding := retentionFindingForRun(t, h, auditA.RunID)
	auditBFinding := retentionFindingForRun(t, h, auditB.RunID)

	merge := h.json(h.admin, "POST", "/api/v1/findings/"+auditAFinding.ID+"/merge-previews",
		object{"otherFindingId": auditBFinding.ID}, 200).MergePreview
	correlation := h.json(h.admin, "POST", "/api/v1/findings/"+auditAFinding.ID+"/merges", object{
		"otherFindingId":          auditBFinding.ID,
		"primaryDecisionRevision": merge.Primary.DecisionRevision,
		"primaryEvidenceRevision": merge.Primary.EvidenceRevision,
		"otherDecisionRevision":   merge.Other.DecisionRevision,
		"otherEvidenceRevision":   merge.Other.EvidenceRevision,
		"decision":                correlationDecision(nil, "open", "none", nil),
		"rationale":               "Create execution audit history.", "idempotencyKey": "retention-exec-merge",
	}, 201).Correlation
	split := h.json(h.admin, "POST", "/api/v1/findings/"+auditAFinding.ID+"/split-previews",
		object{"memberFindingId": auditBFinding.ID}, 200).SplitPreview
	h.json(h.admin, "POST", "/api/v1/findings/"+auditAFinding.ID+"/splits", object{
		"memberFindingId": auditBFinding.ID, "correlationRevision": correlation.Revision,
		"primaryDecisionRevision": split.Primary.DecisionRevision,
		"primaryEvidenceRevision": split.Primary.EvidenceRevision,
		"memberDecisionRevision":  split.Member.DecisionRevision,
		"memberEvidenceRevision":  split.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(nil, "open", "none", nil),
		"memberDecision":          correlationDecision(nil, "open", "none", nil),
		"rationale":               "Release execution audit variants.", "idempotencyKey": "retention-exec-split",
	}, 201)

	availableObservation := h.request(h.admin, "GET",
		"/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/evidence", nil, 200).Body.Bytes()

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	foreign = h.login(h.admin.user.Email, h.password, foreign.workspace)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale":      "Approve exact retention execution cohort.",
		"idempotencyKey": "retention-execution-approval",
	}, 201).RetentionPreview

	h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "observation", "resourceId": lateHeldFinding.Observations[0].ID,
		"reason": "Added after approval and rechecked by execution.",
	}, 201)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+lateDecisionFinding.ID,
		object{"workflowState": "in-progress"}, 200)
	_, err := h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(retentionRawKey(h, missing)),
	})
	ok(t, "delete exact owned missing-evidence control", err)
	corruptBytes := bytes.Repeat([]byte{'x'}, len(fixture(t, "sarif.json")))
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(retentionRawKey(h, corrupt)),
		Body: bytes.NewReader(corruptBytes), ContentLength: aws.Int64(int64(len(corruptBytes))),
	})
	ok(t, "replace exact owned corrupt-evidence control", err)
	beforeWork := h.work(h.admin, "")
	beforeDecision := h.finding(h.admin, lateDecisionFinding.ID)

	queueInput := object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Apply the exact approved retention cohort.",
		"idempotencyKey": "retention-execution-run",
	}
	queued := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		queueInput, 202).RetentionRun
	equal(t, "execution starts queued", queued.State, "queued")
	replayed := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		queueInput, 200).RetentionRun
	equal(t, "execution queue exact replay", replayed.ID, queued.ID)
	changed := object{}
	for key, value := range queueInput {
		changed[key] = value
	}
	changed["snapshotDigest"] = "sha256:" + strings.Repeat("f", 64)
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions",
		changed, 409, "conflict")
	h.denied(foreign, "GET", "/api/v1/retention/runs/"+queued.ID, nil, 404, "not-found")

	firstWorker := openRetentionWorker(t, h)
	ctx, cancel := context.WithTimeout(h.services.ctx, 20*time.Second)
	worked, err := firstWorker.ProcessNext(ctx)
	cancel()
	ok(t, "process first retained execution item", err)
	if !worked {
		t.Fatal("queued retention execution produced no work")
	}
	ok(t, "close retention worker after one durable item", firstWorker.Close())
	progress := retentionRunByID(t, h, h.admin, queued.ID, 200)
	if progress.State != "queued" && progress.State != "processing" {
		t.Fatal("interrupted retention execution lost resumable state")
	}

	secondWorker := openRetentionWorker(t, h)
	drainRetention(t, h, secondWorker)
	ok(t, "close resumed retention worker", secondWorker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	if completed.State != "partial" {
		t.Fatalf("execution terminal state=%s counts total=%d succeeded=%d protected=%d missing=%d corrupt=%d",
			completed.State, completed.Total, completed.Succeeded, completed.Protected, completed.Missing, completed.Corrupt)
	}
	if completed.Total == 0 || completed.Succeeded == 0 || completed.Protected < 3 ||
		completed.Missing != 1 || completed.Corrupt != 1 || completed.Failed != 0 || completed.CompletedAt == nil {
		t.Fatalf("retention execution counts are incomplete: %+v", completed)
	}
	equal(t, "late hold rechecked at execution",
		findRetentionRunItem(t, completed, "observation", lateHeldFinding.Observations[0].ID, "archive-history").State,
		"protected")
	equal(t, "late decision rechecked at execution",
		findRetentionRunItem(t, completed, "import", lateDecision.ID, "expire-raw-report").State,
		"protected")
	equal(t, "shared raw object protected",
		findRetentionRunItem(t, completed, "import", shared.ID, "expire-raw-report").State,
		"protected")
	equal(t, "missing raw object recorded",
		findRetentionRunItem(t, completed, "import", missing.ID, "expire-raw-report").State, "missing")
	equal(t, "corrupt raw object recorded",
		findRetentionRunItem(t, completed, "import", corrupt.ID, "expire-raw-report").State, "corrupt")

	eligibleImport := h.json(h.admin, "GET", "/api/v1/imports/"+eligible.ID, nil, 200).Import
	equal(t, "eligible raw report expired", eligibleImport.EvidenceAvailability, "expired")
	h.denied(h.admin, "GET", "/api/v1/imports/"+eligible.ID+"/evidence", nil, 410, "evidence-expired")
	equal(t, "missing raw report labeled",
		h.json(h.admin, "GET", "/api/v1/imports/"+missing.ID, nil, 200).Import.EvidenceAvailability, "missing")
	h.denied(h.admin, "GET", "/api/v1/imports/"+missing.ID+"/evidence", nil, 424, "evidence-missing")
	equal(t, "corrupt raw report labeled",
		h.json(h.admin, "GET", "/api/v1/imports/"+corrupt.ID, nil, 200).Import.EvidenceAvailability, "corrupt")
	h.denied(h.admin, "GET", "/api/v1/imports/"+corrupt.ID+"/evidence", nil, 422, "evidence-corrupt")
	importsTable := pgx.Identifier{h.services.cfg.Schema, "app_imports"}.Sanitize()
	result, err := h.services.db.Exec(h.services.ctx, "UPDATE "+importsTable+
		" SET retention_transition='expiring',evidence_revision=evidence_revision+1 WHERE workspace_id=$1 AND id=$2",
		h.admin.workspace, lateHeld.ID)
	ok(t, "stage exact raw expiry transition control", err)
	equal(t, "stage one raw expiry transition control", result.RowsAffected(), int64(1))
	h.denied(h.admin, "GET", "/api/v1/imports/"+lateHeld.ID+"/evidence", nil, 503, "unavailable")
	_, err = h.services.db.Exec(h.services.ctx, "UPDATE "+importsTable+
		" SET retention_transition=NULL,evidence_revision=evidence_revision+1 WHERE workspace_id=$1 AND id=$2",
		h.admin.workspace, lateHeld.ID)
	ok(t, "clear exact raw expiry transition control", err)

	archivedFinding := h.finding(h.admin, eligibleFinding.ID)
	equal(t, "normalized observation archived", archivedFinding.Observations[0].EvidenceAvailability, "archived")
	archivedBytes := h.request(h.admin, "GET",
		"/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/evidence", nil, 200).Body.Bytes()
	if !bytes.Equal(archivedBytes, availableObservation) {
		t.Fatal("archive retrieval changed normalized observation bytes")
	}
	h.denied(foreign, "GET", "/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/evidence",
		nil, 404, "not-found")
	auditArchived := 0
	for _, item := range completed.Items {
		if item.Action == "archive-audit" && item.State == "succeeded" {
			auditArchived++
		}
	}
	equal(t, "correlation audit events archived", auditArchived, 2)
	freshArchivePreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "new archive starts its own retention clock",
		retentionSummary(t, freshArchivePreview, "archived-evidence").TotalCount, 0)

	h.clock.Add(int64(4 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+eligibleFinding.ID,
		object{"workflowState": "in-progress"}, 200)
	agedArchivePreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "archived evidence rechecks a later active decision",
		retentionItem(t, agedArchivePreview, "archived-evidence",
			eligibleFinding.Observations[0].ID).ProtectedReasons, []string{"active-decision"})
	h.json(h.admin, "PATCH", "/api/v1/findings/"+eligibleFinding.ID,
		object{"workflowState": "open"}, 200)

	observationsTable := pgx.Identifier{h.services.cfg.Schema, "app_observations"}.Sanitize()
	result, err = h.services.db.Exec(h.services.ctx, "UPDATE "+observationsTable+
		" SET retention_transition='expiring',evidence_revision=evidence_revision+1 WHERE workspace_id=$1 AND id=$2",
		h.admin.workspace, eligibleFinding.Observations[0].ID)
	ok(t, "stage exact archive expiry transition control", err)
	equal(t, "stage one archive expiry transition control", result.RowsAffected(), int64(1))
	h.denied(h.admin, "GET", "/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/evidence",
		nil, 503, "unavailable")
	h.denied(h.admin, "POST",
		"/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/restorations", object{
			"rationale":      "Do not race an active archive expiry.",
			"idempotencyKey": "restore-during-expiry",
		}, 409, "conflict")
	_, err = h.services.db.Exec(h.services.ctx, "UPDATE "+observationsTable+
		" SET retention_transition=NULL,evidence_revision=evidence_revision+1 WHERE workspace_id=$1 AND id=$2",
		h.admin.workspace, eligibleFinding.Observations[0].ID)
	ok(t, "clear exact archive expiry transition control", err)

	restore := h.json(h.admin, "POST",
		"/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/restorations", object{
			"rationale":      "Restore exact normalized observation for current review.",
			"idempotencyKey": "restore-eligible-observation",
		}, 202).RetentionRun
	restoreWorker := openRetentionWorker(t, h)
	drainRetention(t, h, restoreWorker)
	ok(t, "close restoration worker", restoreWorker.Close())
	equal(t, "restoration succeeded", retentionRunByID(t, h, h.admin, restore.ID, 200).State, "succeeded")
	restoredFinding := h.finding(h.admin, eligibleFinding.ID)
	equal(t, "restored observation is available", restoredFinding.Observations[0].EvidenceAvailability, "available")
	restoredBytes := h.request(h.admin, "GET",
		"/api/v1/observations/"+eligibleFinding.Observations[0].ID+"/evidence", nil, 200).Body.Bytes()
	if !bytes.Equal(restoredBytes, availableObservation) {
		t.Fatal("restoration changed normalized observation bytes")
	}

	missingArchiveKey, _ := retentionArchiveObject(t, h, "observations", missingFinding.Observations[0].ID)
	_, err = h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(missingArchiveKey),
	})
	ok(t, "delete exact owned archived missing control", err)
	h.denied(h.admin, "GET", "/api/v1/observations/"+missingFinding.Observations[0].ID+"/evidence",
		nil, 424, "evidence-missing")
	equal(t, "archived missing state persisted",
		h.finding(h.admin, missingFinding.ID).Observations[0].EvidenceAvailability, "missing")

	corruptArchiveKey, corruptArchiveBytes := retentionArchiveObject(t, h, "observations", corruptFinding.Observations[0].ID)
	if len(corruptArchiveBytes) == 0 {
		t.Fatal("corrupt archive control has no bytes")
	}
	corruptArchiveBytes[0] ^= 0xff
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(corruptArchiveKey),
		Body: bytes.NewReader(corruptArchiveBytes), ContentLength: aws.Int64(int64(len(corruptArchiveBytes))),
	})
	ok(t, "replace exact owned archived corrupt control", err)
	h.denied(h.admin, "GET", "/api/v1/observations/"+corruptFinding.Observations[0].ID+"/evidence",
		nil, 422, "evidence-corrupt")
	equal(t, "archived corrupt state persisted",
		h.finding(h.admin, corruptFinding.ID).Observations[0].EvidenceAvailability, "corrupt")

	equal(t, "retention execution did not change Work membership", h.work(h.admin, ""), beforeWork)
	afterDecision := h.finding(h.admin, lateDecisionFinding.ID)
	equal(t, "retention preserved decision owner", afterDecision.OwnerID, beforeDecision.OwnerID)
	equal(t, "retention preserved decision workflow", afterDecision.WorkflowState, beforeDecision.WorkflowState)
	equal(t, "retention preserved decision disposition", afterDecision.Disposition, beforeDecision.Disposition)
	equal(t, "retention preserved notes", afterDecision.Notes, beforeDecision.Notes)
	equal(t, "retention does not refresh source scan time", afterDecision.SourceScanAt, beforeDecision.SourceScanAt)

	sum := sha256.Sum256(availableObservation)
	t.Logf("retention execution resumed, archived/restored sha256:%x, protected=%d missing=%d corrupt=%d",
		sum, completed.Protected, completed.Missing, completed.Corrupt)
}

func TestM06_RetentionExecutionBoundsAbandonedOwnershipAndContinues(t *testing.T) {
	h := newHarness(t, true)
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200)
	asset := h.asset(h.admin, "Abandoned retention ownership repository", nil)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "retention-abandoned", "retention-abandoned-scan"
	run := h.finish(h.upload(input).ID, "succeeded")

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	if len(preview.Items) != 2 {
		t.Fatalf("abandoned-ownership fixture expected two independent items, got %d", len(preview.Items))
	}
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale":      "Approve the bounded abandonment fixture.",
		"idempotencyKey": "retention-abandoned-approval",
	}, 201).RetentionPreview
	queued := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions", object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Exercise bounded abandoned ownership.",
		"idempotencyKey": "retention-abandoned-execution",
	}, 202).RetentionRun

	runsTable := pgx.Identifier{h.services.cfg.Schema, "app_retention_runs"}.Sanitize()
	itemsTable := pgx.Identifier{h.services.cfg.Schema, "app_retention_run_items"}.Sanitize()
	result, err := h.services.db.Exec(h.services.ctx, "UPDATE "+runsTable+`
		SET state='processing',worker_id='abandoned-worker',fence=fence+1,attempts=3,
			lease_until=clock_timestamp()-interval '1 second'
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, queued.ID)
	ok(t, "stage abandoned run ownership", err)
	equal(t, "stage one abandoned run", result.RowsAffected(), int64(1))
	result, err = h.services.db.Exec(h.services.ctx, "UPDATE "+itemsTable+`
		SET state='processing',attempts=3,started_at=clock_timestamp()-interval '1 minute'
		WHERE workspace_id=$1 AND run_id=$2 AND ordinal=0`, h.admin.workspace, queued.ID)
	ok(t, "stage abandoned item ownership", err)
	equal(t, "stage one abandoned item", result.RowsAffected(), int64(1))

	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close abandonment recovery worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	equal(t, "abandoned ownership produces a partial run", completed.State, "partial")
	equal(t, "abandoned ownership fails exactly one item", completed.Failed, 1)
	equal(t, "work continues after an abandoned item", completed.Succeeded, 1)
	equal(t, "abandoned run retains both item receipts", completed.Total, 2)
	if completed.Items[0].State != "failed" || completed.Items[0].Failure == nil ||
		completed.Items[0].Failure.Code != "attempt-limit" {
		t.Fatalf("abandoned item did not retain its terminal attempt-limit failure: %+v", completed.Items[0])
	}
	equal(t, "later raw item still expired", h.json(h.admin, "GET",
		"/api/v1/imports/"+run.ID, nil, 200).Import.EvidenceAvailability, "expired")
}

func TestM06_RetentionExecutionRenewsLeaseDuringStorageAndHonorsLaterHold(t *testing.T) {
	h := newHarness(t, true)
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200)
	asset := h.asset(h.admin, "Retention storage lease repository", nil)
	report := fixture(t, "sarif.json")
	input := h.input(asset.ID, "sarif", report)
	input["sourceId"], input["scanId"] = "retention-storage-lease", "retention-storage-lease-scan"
	imported := h.finish(h.upload(input).ID, "succeeded")

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale":      "Approve the storage lease fixture.",
		"idempotencyKey": "retention-storage-lease-approval",
	}, 201).RetentionPreview
	queued := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions", object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Exercise storage lease renewal and cancellation.",
		"idempotencyKey": "retention-storage-lease-execution",
	}, 202).RetentionRun

	itemsTable := pgx.Identifier{h.services.cfg.Schema, "app_retention_run_items"}.Sanitize()
	result, err := h.services.db.Exec(h.services.ctx, "UPDATE "+itemsTable+`
		SET state='succeeded',outcome='fixture-precompleted',started_at=clock_timestamp(),
			completed_at=clock_timestamp()
		WHERE workspace_id=$1 AND run_id=$2 AND action='archive-history'`,
		h.admin.workspace, queued.ID)
	ok(t, "precomplete unrelated archive item", err)
	equal(t, "precomplete one unrelated archive item", result.RowsAffected(), int64(1))

	raw := h.services.cfg.Storage
	archive := h.services.cfg.ArchiveStorage
	proxy, deleteStarted := blockingRetentionDeleteProxy(t, raw.Endpoint, retentionRawKey(h, imported))
	raw.Endpoint, archive.Endpoint = proxy.URL, proxy.URL
	firstWorker := openRetentionWorkerWithStorage(t, h, raw, archive)
	secondWorker := openRetentionWorkerWithStorage(t, h, raw, archive)
	type processResult struct {
		worked bool
		err    error
	}
	firstContext, cancelFirst := context.WithCancel(h.services.ctx)
	firstResult := make(chan processResult, 1)
	go func() {
		worked, processErr := firstWorker.ProcessNext(firstContext)
		firstResult <- processResult{worked: worked, err: processErr}
	}()
	select {
	case <-deleteStarted:
	case <-time.After(20 * time.Second):
		t.Fatal("retention worker did not reach the held storage delete")
	}

	time.Sleep(3 * time.Second)
	probeContext, cancelProbe := context.WithTimeout(h.services.ctx, 2*time.Second)
	probeWorked, probeErr := secondWorker.ProcessNext(probeContext)
	cancelProbe()
	ok(t, "probe competing worker during held storage operation", probeErr)
	if probeWorked {
		t.Fatal("held storage operation lost its renewed run lease to another worker")
	}

	cancelFirst()
	select {
	case result := <-firstResult:
		if !result.worked || result.err == nil {
			t.Fatalf("cancelled storage operation result=%+v, require worked with explicit uncertainty", result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled retention storage operation did not return")
	}
	ok(t, "close cancelled storage worker", firstWorker.Close())
	h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "import", "resourceId": imported.ID,
		"reason": "Protect bytes before an abandoned transition is reclaimed.",
	}, 201)

	time.Sleep(3 * time.Second)
	drainRetention(t, h, secondWorker)
	ok(t, "close reclaiming storage worker", secondWorker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	equal(t, "held storage cancellation completes safely", completed.State, "succeeded")
	equal(t, "precompleted archive item is retained", completed.Succeeded, 1)
	equal(t, "later hold protects the reclaimed delete", completed.Protected, 1)
	rawItem := findRetentionRunItem(t, completed, "import", imported.ID, "expire-raw-report")
	equal(t, "reclaimed raw delete is protected", rawItem.State, "protected")
	if !slices.Contains(rawItem.ProtectedReasons, "legal-hold") {
		t.Fatalf("reclaimed raw delete omitted the later legal hold: %+v", rawItem.ProtectedReasons)
	}
	available := h.json(h.admin, "GET", "/api/v1/imports/"+imported.ID, nil, 200).Import
	equal(t, "cancelled delete keeps raw evidence available", available.EvidenceAvailability, "available")
	if body := h.request(h.admin, "GET", "/api/v1/imports/"+imported.ID+"/evidence", nil, 200).Body.Bytes(); !bytes.Equal(body, report) {
		t.Fatal("cancelled held delete changed raw evidence bytes")
	}
}

func TestM06_RetentionExecutionFinalizesRecordedDeleteBeforeLaterHold(t *testing.T) {
	h := newHarness(t, true)
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200)
	asset := h.asset(h.admin, "Retention completed-delete repository", nil)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "retention-completed-delete", "retention-completed-delete-scan"
	imported := h.finish(h.upload(input).ID, "succeeded")

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale":      "Approve the completed-delete fixture.",
		"idempotencyKey": "retention-completed-delete-approval",
	}, 201).RetentionPreview
	queued := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions", object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Resume after a recorded delete completed.",
		"idempotencyKey": "retention-completed-delete-execution",
	}, 202).RetentionRun

	itemsTable := pgx.Identifier{h.services.cfg.Schema, "app_retention_run_items"}.Sanitize()
	result, err := h.services.db.Exec(h.services.ctx, "UPDATE "+itemsTable+`
		SET state='succeeded',outcome='fixture-precompleted',started_at=clock_timestamp(),
			completed_at=clock_timestamp()
		WHERE workspace_id=$1 AND run_id=$2 AND action='archive-history'`,
		h.admin.workspace, queued.ID)
	ok(t, "precomplete unrelated archive item for completed delete", err)
	equal(t, "precomplete one archive item for completed delete", result.RowsAffected(), int64(1))
	importsTable := pgx.Identifier{h.services.cfg.Schema, "app_imports"}.Sanitize()
	result, err = h.services.db.Exec(h.services.ctx, "UPDATE "+importsTable+`
		SET retention_transition='expiring',evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, imported.ID)
	ok(t, "record raw delete transition before simulated interruption", err)
	equal(t, "record one raw delete transition", result.RowsAffected(), int64(1))
	_, err = h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(retentionRawKey(h, imported)),
	})
	ok(t, "complete raw delete before simulated database finalization", err)
	h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "import", "resourceId": imported.ID,
		"reason": "This hold arrived after the recorded delete had already completed.",
	}, 201)

	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close completed-delete recovery worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	equal(t, "recorded completed delete finishes the run", completed.State, "succeeded")
	equal(t, "recorded completed delete retains both success receipts", completed.Succeeded, 2)
	equal(t, "post-delete hold is not mislabeled as pre-delete protection", completed.Protected, 0)
	equal(t, "recorded completed delete is finalized as expired",
		h.json(h.admin, "GET", "/api/v1/imports/"+imported.ID, nil, 200).Import.EvidenceAvailability, "expired")
	h.denied(h.admin, "GET", "/api/v1/imports/"+imported.ID+"/evidence", nil, 410, "evidence-expired")
}

func TestM06_RetentionExecutionFinalizesRecordedArchiveDeleteBeforeLaterHold(t *testing.T) {
	h := newHarness(t, true)
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200)
	asset := h.asset(h.admin, "Retention completed archive-delete repository", nil)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "retention-completed-archive-delete", "retention-completed-archive-delete-scan"
	imported := h.finish(h.upload(input).ID, "succeeded")
	finding := retentionFindingForRun(t, h, imported.RunID)
	observationID := finding.Observations[0].ID

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	firstPreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	firstApproved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+firstPreview.ID+"/approvals", object{
		"revision": firstPreview.Revision, "snapshotDigest": firstPreview.SnapshotDigest,
		"rationale":      "Archive the observation before the completed archive-delete fixture.",
		"idempotencyKey": "retention-completed-archive-delete-first-approval",
	}, 201).RetentionPreview
	firstRun := h.json(h.admin, "POST", "/api/v1/retention/previews/"+firstApproved.ID+"/executions", object{
		"revision": firstApproved.Revision, "snapshotDigest": firstApproved.SnapshotDigest,
		"rationale":      "Create the verified archive fixture.",
		"idempotencyKey": "retention-completed-archive-delete-first-execution",
	}, 202).RetentionRun
	firstWorker := openRetentionWorker(t, h)
	drainRetention(t, h, firstWorker)
	ok(t, "close archive fixture worker", firstWorker.Close())
	equal(t, "archive fixture run succeeded", retentionRunByID(t, h, h.admin, firstRun.ID, 200).State, "succeeded")

	h.clock.Add(int64(4 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	secondPreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "aged archive is selected for expiry",
		retentionItem(t, secondPreview, "archived-evidence", observationID).ProtectedReasons, []string{})
	secondApproved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+secondPreview.ID+"/approvals", object{
		"revision": secondPreview.Revision, "snapshotDigest": secondPreview.SnapshotDigest,
		"rationale":      "Approve the completed archive-delete fixture.",
		"idempotencyKey": "retention-completed-archive-delete-second-approval",
	}, 201).RetentionPreview
	secondRun := h.json(h.admin, "POST", "/api/v1/retention/previews/"+secondApproved.ID+"/executions", object{
		"revision": secondApproved.Revision, "snapshotDigest": secondApproved.SnapshotDigest,
		"rationale":      "Resume after a recorded archive delete completed.",
		"idempotencyKey": "retention-completed-archive-delete-second-execution",
	}, 202).RetentionRun

	observationsTable := pgx.Identifier{h.services.cfg.Schema, "app_observations"}.Sanitize()
	result, err := h.services.db.Exec(h.services.ctx, "UPDATE "+observationsTable+`
		SET retention_transition='expiring',evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, observationID)
	ok(t, "record archive delete transition before simulated interruption", err)
	equal(t, "record one archive delete transition", result.RowsAffected(), int64(1))
	archiveKey, _ := retentionArchiveObject(t, h, "observations", observationID)
	_, err = h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(archiveKey),
	})
	ok(t, "complete archive delete before simulated database finalization", err)
	h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "observation", "resourceId": observationID,
		"reason": "This hold arrived after the recorded archive delete had already completed.",
	}, 201)

	secondWorker := openRetentionWorker(t, h)
	drainRetention(t, h, secondWorker)
	ok(t, "close completed archive-delete recovery worker", secondWorker.Close())
	completed := retentionRunByID(t, h, h.admin, secondRun.ID, 200)
	equal(t, "recorded completed archive delete finishes the run", completed.State, "succeeded")
	equal(t, "recorded completed archive delete is one success", completed.Succeeded, 1)
	equal(t, "post-delete observation hold is not pre-delete protection", completed.Protected, 0)
	equal(t, "recorded completed archive delete is finalized as expired",
		h.finding(h.admin, finding.ID).Observations[0].EvidenceAvailability, "expired")
	h.denied(h.admin, "GET", "/api/v1/observations/"+observationID+"/evidence", nil, 410, "evidence-expired")
}
