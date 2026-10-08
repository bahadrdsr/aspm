//go:build integration

package acceptance

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
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

func TestM08_V23HistoryRetentionMigrationIsExactAndAdditive(t *testing.T) {
	h := newNotificationHarness(t)
	ledger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	wantLedger := []string{
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11",
		"12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23",
	}
	if !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("V23 migration ledger got %v, want %v", ledger, wantLedger)
	}
	names := slices.Clone(sourcecompat.V23Tables())
	for _, name := range sourcecompat.V22Tables() {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	current := notificationDefinitions(t, h, names)
	sourcecompat.ValidateCurrentCatalog(t, current)
	prior := priorV23Catalog(t, current)
	sourcecompat.ValidateV23Catalog(t,
		catalogForTables(prior, sourcecompat.V23Tables()),
		catalogForTables(current, sourcecompat.V23Tables()))
	v22Catalog := catalogForTables(prior, sourcecompat.V22Tables())
	if differences := v21CatalogDifferences(v22Catalog, sourcecompat.ExpectedV22Catalog()); len(differences) != 0 {
		t.Fatalf("V23 changed the exact projected V22 notification-policy catalog:\n%s",
			strings.Join(differences, "\n"))
	}
	sourcecompat.ValidateV22Catalog(t, v22Catalog)

	runItemConstraints := notificationCatalogRows(t, h, `SELECT
		conname||'|'||pg_get_constraintdef(oid,true)
		FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype<>'n' ORDER BY conname`,
		notificationTable(h, "retention_run_items"))
	wantRunKind := "app_retention_run_items_resource_kind_check|" +
		sourcecompat.V22RetentionPreviewResourceKindCheck
	if !slices.Contains(runItemConstraints, wantRunKind) {
		t.Fatal("V23 widened or removed the retention run item resource-kind gate")
	}
	for _, table := range []string{
		"finding_decision_events", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events",
	} {
		for _, column := range current[table+"/columns"] {
			name, _, _ := strings.Cut(column, "|")
			if strings.HasPrefix(name, "archive_") || name == "retention_transition" ||
				name == "detail_availability" || name == "evidence_availability" {
				t.Fatalf("V23 added deferred archive execution column %s.%s", table, name)
			}
		}
	}

	h.restart()
	reopenedLedger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	if !reflect.DeepEqual(reopenedLedger, wantLedger) {
		t.Fatal("V23 reopen repeated or skipped a migration")
	}
	reopened := notificationDefinitions(t, h, names)
	if !reflect.DeepEqual(reopened, current) {
		t.Fatal("V23 reopen changed the exact current catalog")
	}
}
