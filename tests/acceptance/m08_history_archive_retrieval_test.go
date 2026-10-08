//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func v24HistoryPath(resource v24HistoryResource) string {
	return "/api/v1/retention/history/" + resource.kind + "/" + resource.id
}

func v24RequireHistoryResponse(t *testing.T, response *httptest.ResponseRecorder,
	resource v24HistoryResource, metadata v24HistoryMetadata) {
	t.Helper()
	if !bytes.Equal(response.Body.Bytes(), resource.payload) || !json.Valid(response.Body.Bytes()) {
		t.Fatal("V24 retrieval did not return exact canonical JSON bytes")
	}
	headers := response.Header()
	wantDisposition := `attachment; filename="history-` + resource.kind + `-` + resource.id + `.json"`
	if headers.Get("Content-Type") != "application/json" ||
		headers.Get("Content-Disposition") != wantDisposition ||
		headers.Get("Content-Length") != strconv.Itoa(len(resource.payload)) ||
		headers.Get("X-ASPM-History-Resource-Kind") != resource.kind ||
		headers.Get("X-ASPM-History-Resource-ID") != resource.id ||
		headers.Get("X-ASPM-History-Availability") != "archived" ||
		headers.Get("X-ASPM-Archive-Digest") != metadata.digest ||
		headers.Get("X-ASPM-Archive-Size") != strconv.FormatInt(metadata.size, 10) ||
		headers.Get("X-ASPM-Detail-Revision") != strconv.FormatInt(metadata.revision, 10) ||
		headers.Get("Cache-Control") != "no-store" ||
		headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("V24 retrieval metadata headers are incomplete: %#v", headers)
	}
}

func TestM08_V24HistoryRetrievalRequiresArchivedCurrentWorkspaceResource(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	resource := v24EligibleHistory(fixture)[0]
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	objectStore.arm()

	h.denied(h.admin, "GET", v24HistoryPath(resource), nil, 409, "history-not-archived")
	h.denied(viewer, "GET", v24HistoryPath(resource), nil, 409, "history-not-archived")
	h.denied(foreign, "GET", v24HistoryPath(resource), nil, 404, "not-found")
	h.denied(h.admin, "GET", "/api/v1/retention/history/finding-change-event/"+resource.id,
		nil, 404, "not-found")
	h.denied(h.admin, "GET", "/api/v1/retention/history/future-history-event/"+resource.id,
		nil, 404, "not-found")
	h.denied(h.admin, "GET", "/api/v1/retention/history/"+resource.kind+"/abc",
		nil, 400, "invalid-input")
	h.denied(actor{workspace: h.admin.workspace}, "GET", v24HistoryPath(resource),
		nil, 401, "unauthorized")
	if objectStore.calls.Load() != 0 {
		t.Fatal("V24 available, foreign or malformed history retrieval performed S3 I/O")
	}
}

func TestM08_V24HistoryRetrievalVerifiesBytesAndPersistsMissingCorruptRecovery(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	resource := v24EligibleHistory(fixture)[0]
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	_, queued, _ := v24QueueHistory(t, h)
	itemsTable := notificationTable(h, "retention_run_items")
	result, err := h.services.db.Exec(h.services.ctx, `UPDATE `+itemsTable+`
		SET state='protected',outcome='fixture-precompleted',protected_reasons='[]'::jsonb,
			started_at=clock_timestamp(),completed_at=clock_timestamp()
		WHERE workspace_id=$1 AND run_id=$2 AND NOT
			(resource_kind=$3 AND resource_id=$4 AND action='archive-audit')`,
		h.admin.workspace, queued.ID, resource.kind, resource.id)
	ok(t, "precomplete unrelated V24 retrieval items", err)
	if result.RowsAffected() != int64(queued.Total-1) {
		t.Fatal("V24 retrieval fixture did not isolate one archived history item")
	}
	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close V24 retrieval archive worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, queued.ID, 200)
	item := findRetentionRunItem(t, completed, resource.kind, resource.id, "archive-audit")
	if item.State != "succeeded" || item.Outcome != "archived" {
		t.Fatalf("V24 retrieval fixture was not archived: %+v", item)
	}
	metadata := v24ReadHistoryMetadata(t, h, resource)

	objectStore.arm()
	h.denied(foreign, "GET", v24HistoryPath(resource), nil, 404, "not-found")
	if objectStore.calls.Load() != 0 {
		t.Fatal("V24 foreign history retrieval reached archive storage")
	}
	adminResponse := h.request(h.admin, "GET", v24HistoryPath(resource), nil, 200)
	v24RequireHistoryResponse(t, adminResponse, resource, metadata)
	viewerResponse := h.request(viewer, "GET", v24HistoryPath(resource), nil, 200)
	v24RequireHistoryResponse(t, viewerResponse, resource, metadata)

	_, err = h.services.s3.DeleteObject(h.services.ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(metadata.key),
	})
	ok(t, "delete exact V24 missing history fixture", err)
	h.denied(h.admin, "GET", v24HistoryPath(resource), nil, 424, "evidence-missing")
	missing := v24ReadHistoryMetadata(t, h, resource)
	if missing.availability != "missing" || missing.revision != metadata.revision+1 ||
		missing.key != metadata.key || missing.digest != metadata.digest ||
		missing.size != metadata.size || missing.archivedAt == nil ||
		v24ArchiveCount(t, h, resource) != 0 {
		t.Fatalf("V24 missing retrieval did not persist exact reference truth: %+v", missing)
	}

	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(metadata.key),
		Body: bytes.NewReader(resource.payload), ContentLength: aws.Int64(int64(len(resource.payload))),
		ContentType: aws.String("application/json"),
		Metadata: map[string]string{
			"sha256": strings.TrimPrefix(metadata.digest, "sha256:"),
		},
	})
	ok(t, "restore exact V24 missing history fixture", err)
	recoveredResponse := h.request(viewer, "GET", v24HistoryPath(resource), nil, 200)
	recovered := v24ReadHistoryMetadata(t, h, resource)
	if recovered.availability != "archived" || recovered.revision != missing.revision+1 {
		t.Fatalf("V24 valid read did not revision-fence missing history recovery: %+v", recovered)
	}
	v24RequireHistoryResponse(t, recoveredResponse, resource, recovered)

	corruptBytes := append([]byte(nil), resource.payload...)
	corruptBytes[len(corruptBytes)/2] ^= 0x01
	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(metadata.key),
		Body: bytes.NewReader(corruptBytes), ContentLength: aws.Int64(int64(len(corruptBytes))),
		ContentType: aws.String("application/json"),
	})
	ok(t, "replace exact V24 corrupt history fixture", err)
	h.denied(h.admin, "GET", v24HistoryPath(resource), nil, 422, "evidence-corrupt")
	corrupt := v24ReadHistoryMetadata(t, h, resource)
	if corrupt.availability != "corrupt" || corrupt.revision != recovered.revision+1 ||
		corrupt.key != metadata.key || corrupt.digest != metadata.digest ||
		corrupt.size != metadata.size || corrupt.archivedAt == nil {
		t.Fatalf("V24 corrupt retrieval did not persist exact reference truth: %+v", corrupt)
	}
	_, storedCorrupt := retentionArchiveObject(t, h, resource.directory, resource.id)
	if !bytes.Equal(storedCorrupt, corruptBytes) {
		t.Fatal("V24 corrupt retrieval silently rewrote the immutable object")
	}

	_, err = h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(metadata.key),
		Body: bytes.NewReader(resource.payload), ContentLength: aws.Int64(int64(len(resource.payload))),
		ContentType: aws.String("application/json"),
	})
	ok(t, "restore exact V24 corrupt history fixture", err)
	finalResponse := h.request(h.admin, "GET", v24HistoryPath(resource), nil, 200)
	final := v24ReadHistoryMetadata(t, h, resource)
	if final.availability != "archived" || final.revision != corrupt.revision+1 {
		t.Fatalf("V24 valid read did not revision-fence corrupt history recovery: %+v", final)
	}
	v24RequireHistoryResponse(t, finalResponse, resource, final)
}
