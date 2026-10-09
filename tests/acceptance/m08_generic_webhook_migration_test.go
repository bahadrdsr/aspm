//go:build integration

package acceptance

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

func replaceV22CatalogConstraint(t *testing.T, rows []string, name, current, prior string) {
	t.Helper()
	matches := 0
	for index, row := range rows {
		if row == name+"|"+current {
			rows[index] = name + "|" + prior
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("V22 catalog constraint %s was missing or ambiguous", name)
	}
	slices.SortFunc(rows, func(left, right string) int {
		leftName, _, _ := strings.Cut(left, "|")
		rightName, _, _ := strings.Cut(right, "|")
		return strings.Compare(leftName, rightName)
	})
}

func priorV22Catalog(t *testing.T, current map[string][]string) map[string][]string {
	t.Helper()
	prior := make(map[string][]string, len(current))
	for key, rows := range current {
		prior[key] = slices.Clone(rows)
	}
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		columnsKey, constraintsKey := table+"/columns", table+"/constraints"
		if columns, present := prior[columnsKey]; present {
			if len(columns) == 0 || columns[len(columns)-1] != "webhook_target|jsonb|false|" {
				t.Fatalf("V22 %s webhook_target column was not appended exactly", table)
			}
			prior[columnsKey] = columns[:len(columns)-1]
			replaceV22CatalogConstraint(t, prior[constraintsKey],
				"app_"+table+"_profile_check",
				sourcecompat.V22DeliveryProfileCheck, sourcecompat.V21DeliveryProfileCheck)
			replaceV22CatalogConstraint(t, prior[constraintsKey],
				"app_"+table+"_profile_target_check",
				sourcecompat.V22DeliveryTargetCheck, sourcecompat.V21DeliveryTargetCheck)
		}
	}
	for _, table := range []string{"notification_policies", "notification_policy_revisions"} {
		key := table + "/constraints"
		if _, present := prior[key]; present {
			replaceV22CatalogConstraint(t, prior[key],
				"app_"+table+"_connection_profile_check",
				sourcecompat.V22NotificationProfileCheck, sourcecompat.V21NotificationProfileCheck)
		}
	}
	return prior
}

func TestM08_V22GenericWebhookMigrationRemainsExactThroughV27(t *testing.T) {
	h := newNotificationHarness(t)
	ledger := notificationCatalogRows(t, h,
		`SELECT ledger.version::text FROM `+notificationTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
	wantLedger := []string{
		"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11",
		"12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22", "23", "24", "25", "26", "27",
	}
	if !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("V27 migration ledger got %v, want %v", ledger, wantLedger)
	}

	names := append(sourcecompat.CurrentTables(), "integration_connections", "finding_deliveries")
	currentCatalog := notificationDefinitions(t, h, names)
	core := catalogForTables(currentCatalog, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, core)
	v26Core := sourcecompat.ProjectV27Current(t, core)
	v25Core := sourcecompat.ProjectV26Current(t, v26Core)
	v24Core := sourcecompat.ProjectV25Current(t, v25Core)
	v23Core := sourcecompat.ProjectV24Current(t,
		catalogForTables(v24Core, sourcecompat.V24CurrentTables()))
	v23 := make(map[string][]string, len(currentCatalog))
	for key, rows := range currentCatalog {
		v23[key] = slices.Clone(rows)
	}
	for key, rows := range v23Core {
		v23[key] = slices.Clone(rows)
	}
	v22 := priorV23Catalog(t, v23)
	policyCatalog := catalogForTables(v22, sourcecompat.V22Tables())
	if differences := v21CatalogDifferences(policyCatalog, sourcecompat.ExpectedV22Catalog()); len(differences) != 0 {
		t.Fatalf("V22 notification-policy catalog differences:\n%s", strings.Join(differences, "\n"))
	}
	sourcecompat.ValidateV22Catalog(t, policyCatalog)

	current := catalogForTables(v22, []string{
		"integration_connections", "finding_deliveries",
		"notification_policies", "notification_policy_revisions",
	})
	prior := priorV22Catalog(t, current)
	if projected := sourcecompat.ProjectV22(t, prior, current); !reflect.DeepEqual(projected, prior) {
		t.Fatal("V22 catalog did not project to the exact pre-V22 shape")
	}
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		columns := current[table+"/columns"]
		if columns[len(columns)-1] != "webhook_target|jsonb|false|" {
			t.Fatalf("V22 %s target column was not the final nullable addition", table)
		}
	}
}
