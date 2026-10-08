//go:build integration

package acceptance

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

func notificationCatalogRows(t *testing.T, h *harness, query string, args ...any) []string {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, query, args...)
	ok(t, "read V21 notification-policy catalog", err)
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		ok(t, "scan V21 notification-policy catalog", rows.Scan(&value))
		quoted := `"` + h.services.cfg.Schema + `".`
		value = strings.ReplaceAll(value, quoted, "")
		value = strings.ReplaceAll(value, h.services.cfg.Schema+".", "")
		result = append(result, value)
	}
	ok(t, "finish V21 notification-policy catalog read", rows.Err())
	return result
}

func notificationDefinitions(t *testing.T, h *harness, names []string) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, name := range names {
		table := notificationTable(h, name)
		result[name+"/columns"] = notificationCatalogRows(t, h, `SELECT
			a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull::text||'|'||
			COALESCE(pg_get_expr(d.adbin,d.adrelid),'')
			FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
			WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, table)
		result[name+"/constraints"] = notificationCatalogRows(t, h, `SELECT
			conname||'|'||pg_get_constraintdef(oid,true)
			FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype<>'n' ORDER BY conname`, table)
		result[name+"/indexes"] = notificationCatalogRows(t, h, `SELECT indexname||'|'||indexdef
			FROM pg_indexes WHERE schemaname=$1 AND tablename=$2 ORDER BY indexname`,
			h.services.cfg.Schema, "app_"+name)
	}
	return result
}

func v21CatalogDifferences(got, want map[string][]string) []string {
	keys := make([]string, 0, len(got)+len(want))
	seen := map[string]bool{}
	for key := range got {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range want {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var differences []string
	for _, key := range keys {
		gotRows, gotPresent := got[key]
		wantRows, wantPresent := want[key]
		if gotPresent != wantPresent || !reflect.DeepEqual(gotRows, wantRows) {
			differences = append(differences, fmt.Sprintf(
				"%s: got(present=%t)=%q want(present=%t)=%q",
				key, gotPresent, gotRows, wantPresent, wantRows,
			))
		}
	}
	return differences
}

func TestM08_V21NotificationPolicyCatalogRemainsExactThroughV25(t *testing.T) {
	h := newNotificationHarness(t)
	ledger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	wantLedger := []string{
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11",
		"12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25",
	}
	if !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("V25 migration ledger got %v, want %v", ledger, wantLedger)
	}
	currentCatalog := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, currentCatalog)
	v24 := sourcecompat.ProjectV25Current(t, currentCatalog)
	v23 := sourcecompat.ProjectV24Current(t, catalogForTables(v24, sourcecompat.V24CurrentTables()))
	v22 := priorV23Catalog(t, catalogForTables(v23, sourcecompat.V23CurrentTables()))
	catalog := priorV22Catalog(t, catalogForTables(v22, sourcecompat.V22Tables()))
	if differences := v21CatalogDifferences(catalog, sourcecompat.ExpectedV21Catalog()); len(differences) != 0 {
		t.Fatalf("projected V21 catalog differences:\n%s", strings.Join(differences, "\n"))
	}
	sourcecompat.ValidateV21Catalog(t, catalog)

	currentLegacy := notificationDefinitions(t, h, []string{"workspaces", "finding_deliveries"})
	preV22 := priorV22Catalog(t, currentLegacy)
	legacy := sourcecompat.ProjectV22(t, preV22, currentLegacy)
	before := map[string][]string{}
	for key, rows := range legacy {
		before[key] = slices.Clone(rows)
	}
	before["workspaces/columns"] = before["workspaces/columns"][:len(before["workspaces/columns"])-1]
	before["finding_deliveries/columns"] =
		before["finding_deliveries/columns"][:len(before["finding_deliveries/columns"])-4]
	removeNamed := func(key string, names ...string) {
		kept := make([]string, 0, len(before[key]))
		for _, row := range before[key] {
			remove := false
			for _, name := range names {
				remove = remove || strings.HasPrefix(row, name+"|")
			}
			if !remove {
				kept = append(kept, row)
			}
		}
		before[key] = kept
	}
	removeNamed("workspaces/constraints", "app_workspaces_notification_policy_epoch_check")
	removeNamed("finding_deliveries/constraints",
		"app_finding_deliveries_policy_binding_check", "app_finding_deliveries_trigger_kind_check")
	removeNamed("finding_deliveries/indexes", "app_finding_deliveries_policy_effect_key")
	if projected := sourcecompat.ProjectV21(t, before, legacy); !reflect.DeepEqual(projected, before) {
		t.Fatal("V21 legacy-table catalog did not project to the exact pre-V21 shape")
	}
}
