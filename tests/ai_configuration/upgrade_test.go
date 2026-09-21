//go:build integration

package ai_configuration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"
)

const previousPublishedCommit = "24556fd41090628f391a728457f9c377ca624983"
const previousDDLHash = "507a2366bf23b541f3cdf1bca85723ae84f24bab9e9dbdcacf30d01d68b51a55"

type historicalDDL struct {
	OriginCommit string
	LedgerDDL    string
	LegacyTables []string
	Migrations   []struct {
		Version                             int
		SQL, SQLSHA256, SourcePath, GitBlob string
	}
	BusinessRowsSeeded bool
}

func catalogRows(t *testing.T, f *fixture, query string, args ...any) []string {
	t.Helper()
	rows, err := f.db.Query(f.ctx, query, args...)
	must(t, "inspect real owned catalog", err)
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		must(t, "read actual catalog row", rows.Scan(&value))
		result = append(result, value)
	}
	must(t, "finish catalog read", rows.Err())
	return result
}
func tableNames(t *testing.T, f *fixture) []string {
	return catalogRows(t, f, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname`, f.database.Schema)
}
func versions(t *testing.T, f *fixture) []string {
	return catalogRows(t, f, "SELECT version::text FROM "+f.table("schema_versions")+" ORDER BY version")
}
func definitions(t *testing.T, f *fixture, names []string) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for _, name := range names {
		result[name+"/columns"] = catalogRows(t, f, `SELECT a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull::text||'|'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, f.table(name))
		result[name+"/constraints"] = catalogRows(t, f, `SELECT conname||'|'||pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=to_regclass($1) ORDER BY conname`, f.table(name))
		result[name+"/indexes"] = catalogRows(t, f, `SELECT indexname||'|'||indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename=$2 ORDER BY indexname`, f.database.Schema, "app_"+name)
		check(t, len(result[name+"/columns"]) > 0, "a published legacy relation is missing")
	}
	return result
}
func publishedV6(t *testing.T, f *fixture) (historicalDDL, map[string][]string) {
	t.Helper()
	fixturePath := os.Getenv("ASPM_AI_CONFIG_FIXTURE_PATH")
	if fixturePath == "" {
		fixturePath = filepath.Join("testdata", "published-24556-schema-v6.json")
	}
	data, err := os.ReadFile(fixturePath)
	must(t, "read exact published DDL fixture", err)
	sum := sha256.Sum256(data)
	check(t, hex.EncodeToString(sum[:]) == previousDDLHash, "historical v6 fixture changed")
	var baseline historicalDDL
	must(t, "decode pinned v6 schema fixture", json.Unmarshal(data, &baseline))
	check(t, baseline.OriginCommit == previousPublishedCommit && len(baseline.Migrations) == 6 && !baseline.BusinessRowsSeeded, "upgrade fixture is not the exact previous schema-only publication")
	check(t, len(tableNames(t, f)) == 0, "legacy baseline must start empty, never by deleting current tables")
	placeholder := regexp.MustCompile(`\{\{([a-z_]+)\}\}`)
	render := func(sql string) string {
		return placeholder.ReplaceAllStringFunc(sql, func(value string) string { return f.table(value[2 : len(value)-2]) })
	}
	tx, err := f.db.Begin(f.ctx)
	must(t, "begin full owned historical DDL construction", err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	_, err = tx.Exec(f.ctx, render(baseline.LedgerDDL))
	must(t, "create real historical migration ledger", err)
	for index, migration := range baseline.Migrations {
		hash := sha256.Sum256([]byte(migration.SQL))
		check(t, migration.Version == index+1 && hex.EncodeToString(hash[:]) == migration.SQLSHA256, "historical DDL fragment/hash/order mismatch")
		_, err = tx.Exec(f.ctx, render(migration.SQL))
		must(t, "execute complete published v1-v6 DDL", err)
		_, err = tx.Exec(f.ctx, "INSERT INTO "+f.table("schema_versions")+" (version) VALUES ($1)", migration.Version)
		must(t, "record only actual applied historical DDL", err)
	}
	must(t, "commit actual previous v6 schema", tx.Commit(f.ctx))
	want := []string{}
	for _, name := range baseline.LegacyTables {
		want = append(want, "app_"+name)
		if name != "schema_versions" {
			var count int
			must(t, "confirm empty legacy business tables", f.db.QueryRow(f.ctx, "SELECT count(*) FROM "+f.table(name)).Scan(&count))
			check(t, count == 0, "schema-only baseline invented legacy business data")
		}
	}
	sort.Strings(want)
	check(t, reflect.DeepEqual(tableNames(t, f), want) && reflect.DeepEqual(versions(t, f), []string{"1", "2", "3", "4", "5", "6"}), "actual published v6 relation/ledger state is incomplete")
	return baseline, definitions(t, f, baseline.LegacyTables)
}
func testPublishedUpgrade(t *testing.T) {
	f := newFixture(t)
	baseline, before := publishedV6(t, f)
	h := &harness{fixture: f}
	h.open()
	afterVersions := versions(t, f)
	afterTables := tableNames(t, f)
	if output := os.Getenv("ASPM_AI_CONFIG_ARTIFACT_DIR"); output != "" {
		observation := struct {
			Origin, Schema                           string
			VerifiedStartingVersion, LegacyRelations int
			CurrentCoreOpened                        bool
			AfterVersions, AfterTables               []string
			LegacyBusinessRowsSeeded                 bool
		}{previousPublishedCommit, f.database.Schema, 6, len(baseline.LegacyTables), true, afterVersions, afterTables, false}
		file := filepath.Join(output, "published-v6-upgrade-"+nonce(t)+".json")
		must(t, "record actual published-v6 upgrade observation", os.WriteFile(file, encode(t, observation), 0600))
		t.Log("actual schema upgrade observation:", file)
	}
	check(t, reflect.DeepEqual(afterVersions, []string{"1", "2", "3", "4", "5", "6", "7", "8"}), "CURRENT core startup did not apply required migrations through v8 exactly once over real published v6")
	check(t, reflect.DeepEqual(before, definitions(t, f, baseline.LegacyTables)), "AI upgrade changed a legacy table definition")
	h.enroll()
	key := secret(t)
	p := h.create(h.admin, "openai", h.tlsTripwire.URL+"/v1", key)
	pol := h.mode(h.admin, "approved-hosted")
	g := h.authorize(h.admin, p, pol)
	r := h.resolver()
	assertResolved(t, r, h.ctx, h.admin, p, pol, &g, key)
	h.reopen()
	must(t, "close resolver for upgrade persistence probe", r.Close())
	fresh := h.resolver()
	assertResolved(t, fresh, h.ctx, h.admin, h.get(h.admin, p.ID), h.json(h.admin, "GET", policyPath, nil, 200).Policy, &g, key)
	check(t, reflect.DeepEqual(versions(t, f), []string{"1", "2", "3", "4", "5", "6", "7", "8"}) && reflect.DeepEqual(before, definitions(t, f, baseline.LegacyTables)), "v8 reopen repeated migration or changed old schema definitions")
	h.networkNone()
	t.Log("real schema-only v6 upgrade and API-created post-upgrade configuration persistence; no legacy customer-data migration claim")
}
