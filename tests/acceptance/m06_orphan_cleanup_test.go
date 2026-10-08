//go:build integration

package acceptance

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

func putArchiveControl(t *testing.T, h *harness, key string, data []byte) {
	t.Helper()
	_, err := h.services.s3.PutObject(h.services.ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
		ContentType: aws.String("application/json"),
	})
	ok(t, "put exact owned archive control", err)
}

func archiveControlExists(t *testing.T, h *harness, key string) bool {
	t.Helper()
	_, err := h.services.s3.HeadObject(h.services.ctx, &s3.HeadObjectInput{
		Bucket: aws.String(h.services.cfg.Storage.Bucket), Key: aws.String(key),
	})
	return err == nil
}

func TestM06_ArchivePublicationLedgerCleansOnlyOldUnreferencedProductObjects(t *testing.T) {
	h := newHarness(t, true)
	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 1, "rawReportDays": 2,
		"archivedEvidenceDays": 3, "auditDays": 4,
	}, 200)
	asset := h.asset(h.admin, "Archive publication ledger repository", nil)
	input := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "orphan-ledger-source", "orphan-ledger-scan"
	imported := h.finish(h.upload(input).ID, "succeeded")
	finding := retentionFindingForRun(t, h, imported.RunID)

	h.clock.Add(int64(10 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	firstPreview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	firstApproved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+firstPreview.ID+"/approvals", object{
		"revision": firstPreview.Revision, "snapshotDigest": firstPreview.SnapshotDigest,
		"rationale":      "Create a referenced archive publication control.",
		"idempotencyKey": "orphan-ledger-first-approval",
	}, 201).RetentionPreview
	firstRun := h.json(h.admin, "POST", "/api/v1/retention/previews/"+firstApproved.ID+"/executions", object{
		"revision": firstApproved.Revision, "snapshotDigest": firstApproved.SnapshotDigest,
		"rationale":      "Archive the referenced observation.",
		"idempotencyKey": "orphan-ledger-first-run",
	}, 202).RetentionRun
	firstWorker := openRetentionWorker(t, h)
	drainRetention(t, h, firstWorker)
	ok(t, "close archive publication control worker", firstWorker.Close())
	equal(t, "archive publication control run succeeded",
		retentionRunByID(t, h, h.admin, firstRun.ID, 200).State, "succeeded")

	publications := pgx.Identifier{h.services.cfg.Schema, "app_archive_publications"}.Sanitize()
	var referencedID, referencedKey, referencedState string
	err := h.services.db.QueryRow(h.services.ctx, `SELECT id,object_key,state FROM `+publications+`
		WHERE workspace_id=$1 AND resource_kind='observation' AND resource_id=$2`,
		h.admin.workspace, finding.Observations[0].ID).Scan(&referencedID, &referencedKey, &referencedState)
	ok(t, "read referenced archive publication", err)
	equal(t, "archive operation finalized publication reference", referencedState, "referenced")

	now := h.services.cfg.Now().UTC()
	old, fresh := now.Add(-48*time.Hour), now.Add(-time.Hour)
	type publication struct {
		id, resource, key string
		data              []byte
		created           time.Time
		put               bool
	}
	values := []publication{
		{"a1000000000000000000000000000001", "b1000000000000000000000000000001", "", []byte(`{"orphan":"delete"}`), old, true},
		{"a2000000000000000000000000000002", "b2000000000000000000000000000002", "", []byte(`{"orphan":"revision"}`), old, true},
		{"a3000000000000000000000000000003", "b3000000000000000000000000000003", "", []byte(`{"orphan":"missing"}`), old, false},
		{"a4000000000000000000000000000004", "b4000000000000000000000000000004", "", []byte(`{"orphan":"fresh"}`), fresh, true},
	}
	for index := range values {
		value := &values[index]
		objectDigest := digest(value.data)
		value.key = h.services.cfg.ArchiveStorage.Prefix + h.admin.workspace + "/observations/" +
			value.resource + "/" + strings.TrimPrefix(objectDigest, "sha256:")
		if value.put {
			putArchiveControl(t, h, value.key, value.data)
		}
		_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+publications+`
			(id,workspace_id,object_key,resource_kind,resource_id,object_digest,object_size,state,revision,created_at,updated_at)
			VALUES($1,$2,$3,'observation',$4,$5,$6,'orphan',1,$7,$7)`,
			value.id, h.admin.workspace, value.key, value.resource, objectDigest, len(value.data), value.created)
		ok(t, "insert owned archive publication control", err)
	}
	foreign := h.addWorkspace()
	foreignData := []byte(`{"orphan":"foreign"}`)
	foreignDigest := digest(foreignData)
	foreignKey := h.services.cfg.ArchiveStorage.Prefix + foreign.workspace + "/observations/" +
		"b5000000000000000000000000000005/" + strings.TrimPrefix(foreignDigest, "sha256:")
	putArchiveControl(t, h, foreignKey, foreignData)
	_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+publications+`
		(id,workspace_id,object_key,resource_kind,resource_id,object_digest,object_size,state,revision,created_at,updated_at)
		VALUES('a5000000000000000000000000000005',$1,$2,'observation',
		'b5000000000000000000000000000005',$3,$4,'orphan',1,$5,$5)`,
		foreign.workspace, foreignKey, foreignDigest, len(foreignData), old)
	ok(t, "insert foreign archive publication control", err)
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+publications+`
		SET state='orphan',revision=revision+1,referenced_at=NULL,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, referencedID, old)
	ok(t, "stage referenced publication as protected orphan control", err)

	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	summary := retentionSummary(t, preview, "orphan-archive")
	equal(t, "orphan preview total", summary.TotalCount, 4)
	equal(t, "orphan preview eligible", summary.EligibleCount, 3)
	equal(t, "orphan preview protected", summary.ProtectedCount, 1)
	referenced := retentionItem(t, preview, "orphan-archive", referencedID)
	equal(t, "referenced archive protection", referenced.ProtectedReasons, []string{"archive-reference"})
	if referenced.ObjectKey == nil || *referenced.ObjectKey != referencedKey {
		t.Fatal("orphan preview omitted referenced product key")
	}
	for _, value := range values[:3] {
		item := retentionItem(t, preview, "orphan-archive", value.id)
		if item.ObjectKey == nil || *item.ObjectKey != value.key {
			t.Fatalf("orphan preview omitted exact key for %s", value.id)
		}
	}
	for _, item := range preview.Items {
		if item.ResourceID == values[3].id || item.ResourceID == "a5000000000000000000000000000005" {
			t.Fatal("orphan preview crossed grace or workspace boundary")
		}
	}

	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale":      "Delete only exact old unreferenced publication keys.",
		"idempotencyKey": "orphan-ledger-delete-approval",
	}, 201).RetentionPreview
	run := h.json(h.admin, "POST", "/api/v1/retention/previews/"+approved.ID+"/executions", object{
		"revision": approved.Revision, "snapshotDigest": approved.SnapshotDigest,
		"rationale":      "Apply the exact orphan publication preview.",
		"idempotencyKey": "orphan-ledger-delete-run",
	}, 202).RetentionRun
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+publications+`
		SET revision=revision+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, values[1].id, now)
	ok(t, "change orphan publication after approval", err)

	worker := openRetentionWorker(t, h)
	drainRetention(t, h, worker)
	ok(t, "close orphan cleanup worker", worker.Close())
	completed := retentionRunByID(t, h, h.admin, run.ID, 200)
	equal(t, "orphan cleanup run state", completed.State, "succeeded")
	equal(t, "orphan cleanup succeeded items", completed.Succeeded, 2)
	equal(t, "orphan cleanup protected items", completed.Protected, 2)
	equal(t, "eligible orphan deleted", archiveControlExists(t, h, values[0].key), false)
	equal(t, "revision-changed orphan retained", archiveControlExists(t, h, values[1].key), true)
	equal(t, "referenced archive retained", archiveControlExists(t, h, referencedKey), true)
	equal(t, "fresh orphan retained", archiveControlExists(t, h, values[3].key), true)
	equal(t, "foreign orphan retained", archiveControlExists(t, h, foreignKey), true)
	equal(t, "missing orphan outcome",
		findRetentionRunItem(t, completed, "archive-object", values[2].id, "delete-orphan").Outcome, "already-missing")
	var deletedState, missingState, healedState string
	ok(t, "read deleted publication state", h.services.db.QueryRow(h.services.ctx,
		`SELECT state FROM `+publications+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, values[0].id).Scan(&deletedState))
	ok(t, "read missing publication state", h.services.db.QueryRow(h.services.ctx,
		`SELECT state FROM `+publications+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, values[2].id).Scan(&missingState))
	ok(t, "read healed referenced publication state", h.services.db.QueryRow(h.services.ctx,
		`SELECT state FROM `+publications+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, referencedID).Scan(&healedState))
	equal(t, "deleted publication finalized", deletedState, "deleted")
	equal(t, "missing publication finalized", missingState, "deleted")
	equal(t, "referenced publication ledger healed", healedState, "referenced")
}
