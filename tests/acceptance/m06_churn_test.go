//go:build integration

package acceptance

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

func relationBytes(t *testing.T, h *harness, relation string) int64 {
	t.Helper()
	var value int64
	ok(t, "measure owned PostgreSQL relation", h.services.db.QueryRow(h.services.ctx,
		`SELECT pg_total_relation_size(to_regclass($1))`, relation).Scan(&value))
	return value
}

func TestM06_RecurringUnchangedScansHaveBoundedDatabaseAndWALChurn(t *testing.T) {
	h := newHarness(t, true)
	asset := h.asset(h.admin, "Recurring scan churn repository", nil)
	report := fixture(t, "sarif.json")
	run := func(index int) {
		input := h.input(asset.ID, "sarif", report)
		input["sourceId"] = "recurring-churn-source"
		input["scanId"] = "recurring-churn-scan-" + string(rune('a'+index))
		observed := sourceTime.Add(time.Duration(index) * time.Hour)
		input["sourceScanAt"], input["collectedAt"] = observed, observed.Add(time.Minute)
		h.finish(h.upload(input).ID, "succeeded")
	}
	run(0)

	findingsTable := pgx.Identifier{h.services.cfg.Schema, "app_findings"}.Sanitize()
	observationsTable := pgx.Identifier{h.services.cfg.Schema, "app_observations"}.Sanitize()
	candidateIndex := pgx.Identifier{h.services.cfg.Schema, "app_findings_correlation_candidate_idx"}.Sanitize()
	changeIndex := pgx.Identifier{h.services.cfg.Schema, "app_findings_meaningful_change_idx"}.Sanitize()
	baselineTotal := relationBytes(t, h, findingsTable) + relationBytes(t, h, observationsTable)
	baselineCandidate := relationBytes(t, h, candidateIndex)
	baselineChange := relationBytes(t, h, changeIndex)
	var walStart string
	ok(t, "read owned WAL start", h.services.db.QueryRow(h.services.ctx,
		`SELECT pg_current_wal_lsn()::text`).Scan(&walStart))

	const scans = 25
	for index := 1; index < scans; index++ {
		run(index)
	}
	var findingCount, observationCount int
	var changeRevision int64
	var changeKind string
	ok(t, "count recurring findings", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+findingsTable+` WHERE workspace_id=$1`, h.admin.workspace).Scan(&findingCount))
	ok(t, "count recurring observations", h.services.db.QueryRow(h.services.ctx,
		`SELECT count(*) FROM `+observationsTable+` WHERE workspace_id=$1`, h.admin.workspace).Scan(&observationCount))
	ok(t, "read recurring lifecycle projection", h.services.db.QueryRow(h.services.ctx,
		`SELECT change_revision,change_kind FROM `+findingsTable+` WHERE workspace_id=$1`,
		h.admin.workspace).Scan(&changeRevision, &changeKind))
	equal(t, "recurring scans retain one finding", findingCount, 1)
	equal(t, "recurring scans retain every observation", observationCount, scans)
	equal(t, "recurring lifecycle revision", changeRevision, int64(scans))
	equal(t, "last recurring scan is unchanged", changeKind, "unchanged")
	equal(t, "unchanged recurring scans create no meaningful Work",
		h.json(h.admin, "GET", "/api/v1/work?change=meaningful", nil, 200).Total, 0)

	afterTotal := relationBytes(t, h, findingsTable) + relationBytes(t, h, observationsTable)
	afterCandidate := relationBytes(t, h, candidateIndex)
	afterChange := relationBytes(t, h, changeIndex)
	var walBytes int64
	ok(t, "measure owned WAL growth", h.services.db.QueryRow(h.services.ctx,
		`SELECT pg_wal_lsn_diff(pg_current_wal_lsn(),$1::pg_lsn)::bigint`, walStart).Scan(&walBytes))
	tableGrowth := afterTotal - baselineTotal
	candidateGrowth := afterCandidate - baselineCandidate
	changeGrowth := afterChange - baselineChange
	if tableGrowth < 0 || tableGrowth > 8<<20 {
		t.Fatalf("25-scan table growth %d bytes exceeded the explicit 8 MiB engineering bound", tableGrowth)
	}
	if candidateGrowth < 0 || candidateGrowth > 1<<20 {
		t.Fatalf("candidate index growth %d bytes exceeded the explicit 1 MiB engineering bound", candidateGrowth)
	}
	if changeGrowth < 0 || changeGrowth > 1<<20 {
		t.Fatalf("meaningful-change index growth %d bytes exceeded the explicit 1 MiB engineering bound", changeGrowth)
	}
	if walBytes < 0 || walBytes > 32<<20 {
		t.Fatalf("25-scan WAL growth %d bytes exceeded the explicit 32 MiB engineering bound", walBytes)
	}

	page, err := h.services.s3.ListObjectsV2(h.services.ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(h.services.cfg.Storage.Bucket),
		Prefix:  aws.String(h.services.cfg.Storage.Prefix + h.admin.workspace + "/"),
		MaxKeys: aws.Int32(100),
	})
	ok(t, "list exact recurring raw evidence objects", err)
	equal(t, "one immutable raw object per distinct scan", len(page.Contents), scans)
	if aws.ToBool(page.IsTruncated) {
		t.Fatal("bounded recurring raw object listing unexpectedly truncated")
	}
	t.Logf("25 scans: table growth=%d bytes candidate-index growth=%d change-index growth=%d WAL=%d bytes",
		tableGrowth, candidateGrowth, changeGrowth, walBytes)
}
