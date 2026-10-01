//go:build integration

package jira_work_items

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/deliverycompat"
)

func TestJiraV10SchemaPredicateCalibration(t *testing.T) {
	f := newFixture(t)
	table := f.table("schema_contract_probe")
	_, err := f.db.Exec(f.ctx, "CREATE TABLE "+table+` (
		profile text NOT NULL, channel text NOT NULL, jira_target jsonb,
		CONSTRAINT fixture_profile_check `+deliverycompat.ProfileCheck+`,
		CONSTRAINT fixture_target_check `+deliverycompat.TargetCheck+`)`)
	must(t, "create empty owned predicate-calibration relation, not application V10", err)
	actual := f.rows(`SELECT pg_get_constraintdef(oid,true) FROM pg_constraint
		WHERE conrelid=to_regclass($1) AND contype='c' ORDER BY conname`, table)
	want := []string{deliverycompat.ProfileCheck, deliverycompat.TargetCheck}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("approved CHECK canonicalization differs: got %q, want %q", actual, want)
	}
	legacy := f.table("schema_legacy_profile_probe")
	_, err = f.db.Exec(f.ctx, "CREATE TABLE "+legacy+" (profile text NOT NULL, CONSTRAINT fixture_legacy_check "+deliverycompat.LegacyProfileCheck+")")
	must(t, "calibrate unchanged historical profile CHECK without a historical schema imitation", err)
	actualLegacy := f.rows(`SELECT pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=to_regclass($1) AND contype='c'`, legacy)
	if !reflect.DeepEqual(actualLegacy, []string{deliverycompat.LegacyProfileCheck}) {
		t.Fatalf("historical profile CHECK canonicalization differs: got %q", actualLegacy)
	}
	var mismatches int
	profile := strings.TrimSuffix(strings.TrimPrefix(deliverycompat.ProfileCheck, "CHECK ("), ")")
	target := strings.TrimSuffix(strings.TrimPrefix(deliverycompat.TargetCheck, "CHECK ("), ")")
	must(t, "evaluate approved CHECK predicates without business SQL rows", f.db.QueryRow(f.ctx, `
		SELECT count(*) FROM (VALUES
			('slack-workspace-bot','C123',NULL::jsonb,true),
			('slack-workspace-bot','G456',NULL::jsonb,true),
			('slack-workspace-bot','C123','{}'::jsonb,false),
			('slack-workspace-bot','',NULL::jsonb,false),
			('slack-workspace-bot','#general',NULL::jsonb,false),
			('jira-cloud-v3','','{}'::jsonb,true),
			('jira-cloud-v3','C123','{}'::jsonb,false),
			('jira-cloud-v3','',NULL::jsonb,false),
			('jira-cloud-v3','','null'::jsonb,false),
			('jira-cloud-v3','','[]'::jsonb,false),
			('jira-cloud-v3','','"not-object"'::jsonb,false),
			('other-profile','C123',NULL::jsonb,false)
		) AS tuples(profile,channel,jira_target,want)
		WHERE ((`+profile+`) AND (`+target+`)) IS DISTINCT FROM want`).Scan(&mismatches))
	check(t, mismatches == 0, "approved typed CHECKs admitted a mixed/invalid tuple or rejected valid Slack/Jira")
	check(t, len(f.rows("SELECT profile FROM "+table)) == 0, "predicate calibration inserted business rows")
	t.Log("CALIBRATED: exact PostgreSQL CHECK deparse and 12 read-only tuple evaluations; empty non-application probe, not V10 migration acceptance")
}
