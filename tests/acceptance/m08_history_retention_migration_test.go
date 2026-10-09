//go:build integration

package acceptance

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
	"github.com/jackc/pgx/v5"
)

func replaceV23CatalogConstraint(t *testing.T, rows []string, name, current, prior string) {
	t.Helper()
	matches := 0
	for index, row := range rows {
		for _, separator := range []string{"|", "|c|"} {
			if row == name+separator+current {
				rows[index] = name + separator + prior
				matches++
			}
		}
	}
	if matches != 1 {
		t.Fatalf("V23 catalog constraint %s was missing or ambiguous", name)
	}
}

func priorV23Catalog(t *testing.T, current map[string][]string) map[string][]string {
	t.Helper()
	prior := make(map[string][]string, len(current))
	for key, rows := range current {
		prior[key] = slices.Clone(rows)
	}
	replaceV23CatalogConstraint(t, prior["retention_holds/constraints"],
		"app_retention_holds_resource_kind_check",
		sourcecompat.V23RetentionHoldResourceKindCheck,
		sourcecompat.V22RetentionHoldResourceKindCheck)
	replaceV23CatalogConstraint(t, prior["retention_preview_items/constraints"],
		"app_retention_preview_items_resource_kind_check",
		sourcecompat.V23RetentionPreviewResourceKindCheck,
		sourcecompat.V22RetentionPreviewResourceKindCheck)
	indexNames := sourcecompat.V23IndexNames()
	for _, table := range []string{
		"finding_decision_events", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events",
	} {
		key := table + "/indexes"
		kept := make([]string, 0, len(prior[key])-1)
		removed := 0
		for _, row := range prior[key] {
			name, _, _ := strings.Cut(row, "|")
			if slices.Contains(indexNames, name) {
				removed++
				continue
			}
			kept = append(kept, row)
		}
		if removed != 1 {
			t.Fatalf("V23 %s candidate index was missing or ambiguous", table)
		}
		prior[key] = kept
	}
	return prior
}

func catalogForTables(source map[string][]string, tables []string) map[string][]string {
	result := map[string][]string{}
	for _, table := range tables {
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			if rows, present := source[key]; present {
				result[key] = slices.Clone(rows)
			}
		}
	}
	return result
}

func TestM08_V24HistoryArchiveMigrationRemainsExactThroughV26(t *testing.T) {
	h := newNotificationHarness(t)
	ledger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	wantLedger := []string{
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11",
		"12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26",
	}
	if !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("V26 migration ledger got %v, want %v", ledger, wantLedger)
	}
	names := sourcecompat.CurrentTables()
	current := notificationDefinitions(t, h, names)
	sourcecompat.ValidateCurrentCatalog(t, current)
	v25Expanded := sourcecompat.ProjectV26Current(t, current)
	sourcecompat.ValidateV26Catalog(t, v25Expanded, current)
	v24Expanded := sourcecompat.ProjectV25Current(t, v25Expanded)
	sourcecompat.ValidateV25Catalog(t, v24Expanded, v25Expanded)
	v24Current := catalogForTables(v24Expanded, sourcecompat.V24CurrentTables())
	v23Expanded := sourcecompat.ProjectV24Current(t, v24Current)
	sourcecompat.ValidateV24Catalog(t, v23Expanded, v24Current)
	v23Current := catalogForTables(v23Expanded, sourcecompat.V23CurrentTables())
	sourcecompat.ValidateV23CurrentCatalog(t, v23Current)
	prior := priorV23Catalog(t, v23Current)
	sourcecompat.ValidateV23Catalog(t,
		catalogForTables(prior, sourcecompat.V23Tables()),
		catalogForTables(v23Current, sourcecompat.V23Tables()))
	v22Catalog := catalogForTables(prior, sourcecompat.V22Tables())
	if differences := v21CatalogDifferences(v22Catalog, sourcecompat.ExpectedV22Catalog()); len(differences) != 0 {
		t.Fatalf("V24 changed the exact projected V22 notification-policy catalog:\n%s",
			strings.Join(differences, "\n"))
	}
	sourcecompat.ValidateV22Catalog(t, v22Catalog)

	runItemConstraints := notificationCatalogRows(t, h, `SELECT
		conname||'|'||pg_get_constraintdef(oid,true)
		FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype<>'n' ORDER BY conname`,
		notificationTable(h, "retention_run_items"))
	wantRunKind := "app_retention_run_items_resource_kind_check|" +
		sourcecompat.V24RetentionRunItemResourceKindCheck
	if !slices.Contains(runItemConstraints, wantRunKind) {
		t.Fatal("V24 did not widen the retention run item resource-kind gate exactly")
	}
	publicationConstraints := notificationCatalogRows(t, h, `SELECT
		conname||'|'||pg_get_constraintdef(oid,true)
		FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype<>'n' ORDER BY conname`,
		notificationTable(h, "archive_publications"))
	wantPublicationKind := "app_archive_publications_resource_kind_check|" +
		sourcecompat.V24ArchivePublicationResourceKindCheck
	if !slices.Contains(publicationConstraints, wantPublicationKind) {
		t.Fatal("V24 did not widen the archive publication resource-kind gate exactly")
	}
	if !slices.Contains(current["retention_holds/constraints"],
		"app_retention_holds_resource_kind_check|"+sourcecompat.V23RetentionHoldResourceKindCheck) ||
		!slices.Contains(current["retention_preview_items/constraints"],
			"app_retention_preview_items_resource_kind_check|"+sourcecompat.V23RetentionPreviewResourceKindCheck) {
		t.Fatal("V24 changed the exact V23 hold or preview resource-kind gate")
	}

	h.restart()
	reopenedLedger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	if !reflect.DeepEqual(reopenedLedger, wantLedger) {
		t.Fatal("V26 reopen repeated or skipped a migration")
	}
	reopened := notificationDefinitions(t, h, names)
	if !reflect.DeepEqual(reopened, current) {
		t.Fatal("V26 reopen changed the exact current catalog")
	}
}

func v24OriginalHistoryRows(t *testing.T, h *harness) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, table := range []string{
		"finding_decision_events", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events",
	} {
		result[table] = notificationCatalogRows(t, h, `SELECT
			(to_jsonb(v)-'detail_availability'-'detail_revision'-'archive_key'-
			 'archive_digest'-'archive_size'-'archived_at')::text
			FROM `+notificationTable(h, table)+` AS v
			WHERE workspace_id=$1 ORDER BY id`, h.admin.workspace)
	}
	return result
}

func TestM08_V24MigrationPreservesCompleteV23HistoryRows(t *testing.T) {
	h, _ := newV23HistoryHarness(t)
	fixture := seedV23HistoryRetention(t, h, &providerTripwire{})
	var version int
	ok(t, "read current V26 migration version", h.services.db.QueryRow(h.services.ctx,
		`SELECT max(version) FROM `+notificationTable(h, "schema_versions")).Scan(&version))
	if version != 26 {
		t.Fatalf("V26 current fixture version=%d, want 26 before exact downgrade", version)
	}
	beforeRows := v24OriginalHistoryRows(t, h)
	beforePayloads := map[string]string{}
	for id, payload := range fixture.payloads {
		beforePayloads[id] = v23ArchiveDigest(payload)
	}

	ok(t, "close V24 application before exact V23 downgrade", h.app.Close())
	h.app = Application{}
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin exact V24 downgrade fixture", err)
	defer tx.Rollback(h.services.ctx)
	_, err = tx.Exec(h.services.ctx, `DROP TABLE `+
		notificationTable(h, "report_exports")+`;
		DELETE FROM `+notificationTable(h, "schema_versions")+` WHERE version=26;
		DROP TABLE `+
		notificationTable(h, "report_sla_policy_revisions")+`, `+
		notificationTable(h, "report_sla_policies")+`;
		DROP INDEX `+pgx.Identifier{h.services.cfg.Schema, "app_findings_sla_candidate_idx"}.Sanitize()+`;
		ALTER TABLE `+notificationTable(h, "findings")+` DROP COLUMN first_observed_at;
		DELETE FROM `+notificationTable(h, "schema_versions")+` WHERE version=25`)
	ok(t, "remove only the V25 remediation SLA schema", err)
	for _, table := range []string{
		"finding_decision_events", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events",
	} {
		_, err = tx.Exec(h.services.ctx, `ALTER TABLE `+notificationTable(h, table)+`
			DROP CONSTRAINT app_`+table+`_archive_reference_check,
			DROP CONSTRAINT app_`+table+`_detail_availability_check,
			DROP CONSTRAINT app_`+table+`_detail_revision_check,
			DROP COLUMN archived_at,
			DROP COLUMN archive_size,
			DROP COLUMN archive_digest,
			DROP COLUMN archive_key,
			DROP COLUMN detail_revision,
			DROP COLUMN detail_availability`)
		ok(t, "remove only the V24 history archive columns and checks", err)
	}
	_, err = tx.Exec(h.services.ctx, `ALTER TABLE `+notificationTable(h, "retention_run_items")+`
		DROP CONSTRAINT app_retention_run_items_resource_kind_check,
		ADD CONSTRAINT app_retention_run_items_resource_kind_check
		CHECK(resource_kind IN ('import','observation','correlation-event','archive-object'))`)
	ok(t, "restore exact V23 retention run item resource kinds", err)
	_, err = tx.Exec(h.services.ctx, `ALTER TABLE `+notificationTable(h, "archive_publications")+`
		DROP CONSTRAINT app_archive_publications_resource_kind_check,
		ADD CONSTRAINT app_archive_publications_resource_kind_check
		CHECK(resource_kind IN ('observation','correlation-event'))`)
	ok(t, "restore exact V23 archive publication resource kinds", err)
	_, err = tx.Exec(h.services.ctx, `DELETE FROM `+notificationTable(h, "schema_versions")+`
		WHERE version=24`)
	ok(t, "remove only the V24 migration receipt", err)
	ok(t, "commit exact V23 downgrade fixture", tx.Commit(h.services.ctx))

	v23Catalog := notificationDefinitions(t, h, sourcecompat.V23CurrentTables())
	sourcecompat.ValidateV23CurrentCatalog(t, v23Catalog)
	h.open()
	currentCatalog := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, currentCatalog)
	ledger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+`
		AS ledger ORDER BY ledger.version`)
	wantLedger := []string{
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11",
		"12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26",
	}
	if !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("V26 migration ledger got %v, want %v", ledger, wantLedger)
	}
	afterRows := v24OriginalHistoryRows(t, h)
	for table, before := range beforeRows {
		if !reflect.DeepEqual(afterRows[table], before) {
			t.Fatalf("V24 migration changed complete V23 %s rows:\nbefore=%v\nafter=%v",
				table, before, afterRows[table])
		}
	}
	for id, before := range beforePayloads {
		resource := v24HistoryResource{kind: fixture.kinds[id], id: id}
		if v23ArchiveDigest(v24CanonicalHistoryBytes(t, h, resource)) != before {
			t.Fatalf("V24 migration changed canonical V23 payload %s", id)
		}
		metadata := v24ReadHistoryMetadata(t, h, resource)
		if metadata.availability != "available" || metadata.revision != 1 ||
			metadata.key != "" || metadata.digest != "" || metadata.size != 0 ||
			metadata.archivedAt != nil {
			t.Fatalf("V24 migration did not apply exact default archive metadata to %s", id)
		}
	}
	h.restart()
	if !reflect.DeepEqual(notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+`
		AS ledger ORDER BY ledger.version`), wantLedger) ||
		!reflect.DeepEqual(v24OriginalHistoryRows(t, h), beforeRows) {
		t.Fatal("V26 reopen repeated migration or changed complete V23 history rows")
	}
}
