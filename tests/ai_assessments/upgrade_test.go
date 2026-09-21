//go:build integration

package ai_assessments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

const publishedOrigin = "98a41ced71c09e71c8cc2bd158678fd52268498c"
const publishedDDLHash = "80ce37253c46cf61d37d88b2449ab0447e832a40852be30acb32a4a17b7358d4"

type publishedSchema struct {
	OriginCommit            string
	LedgerDDL               string
	LegacyTables            []string
	BusinessRowsSeededByDDL bool
	Migrations              []struct {
		Version                                           int
		SourcePath, GitBlob, SourceSHA256, SQLSHA256, SQL string
	}
}

func catalogRows(t *testing.T, f *fixture, query string, args ...any) []string {
	t.Helper()
	rows, err := f.db.Query(f.ctx, query, args...)
	must(t, "readonly owned catalog/legacy rows", err)
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		must(t, "read actual catalog/legacy row", rows.Scan(&value))
		result = append(result, value)
	}
	must(t, "finish readonly catalog/legacy query", rows.Err())
	return result
}
func ledger(t *testing.T, f *fixture) []string {
	return catalogRows(t, f, "SELECT version::text FROM "+f.table("schema_versions")+" ORDER BY version")
}
func definitions(t *testing.T, f *fixture, names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		result[name+"/columns"] = catalogRows(t, f, `SELECT a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull::text||'|'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, f.table(name))
		result[name+"/constraints"] = catalogRows(t, f, `SELECT conname||'|'||pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=to_regclass($1) ORDER BY conname`, f.table(name))
		result[name+"/indexes"] = catalogRows(t, f, `SELECT indexname||'|'||indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename=$2 ORDER BY indexname`, f.database.Schema, "app_"+name)
		check(t, len(result[name+"/columns"]) != 0, "published legacy relation is missing")
	}
	return result
}
func legacyData(t *testing.T, f *fixture, names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		if name != "schema_versions" {
			result[name] = catalogRows(t, f, "SELECT to_jsonb(v)::text FROM "+f.table(name)+" v ORDER BY to_jsonb(v)::text")
		}
	}
	return result
}
func buildPublishedV7(t *testing.T, f *fixture) publishedSchema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(required(t, "ASPM_ASSESSMENT_TESTDATA"), "published-98a41ce-schema-v7.json"))
	must(t, "read exact published V7 DDL", err)
	sum := sha256.Sum256(data)
	check(t, hex.EncodeToString(sum[:]) == publishedDDLHash, "published DDL fixture drifted")
	var baseline publishedSchema
	must(t, "decode published DDL provenance", json.Unmarshal(data, &baseline))
	check(t, baseline.OriginCommit == publishedOrigin && len(baseline.Migrations) == 7 && !baseline.BusinessRowsSeededByDDL,
		"historical fixture must be exact published V7, not current relabelled schema")
	tables := catalogRows(t, f, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname`, f.database.Schema)
	check(t, len(tables) == 0, "published fixture must start fresh, never by deleting current tables")
	placeholder := regexp.MustCompile(`\{\{([a-z_]+)\}\}`)
	render := func(sql string) string {
		return placeholder.ReplaceAllStringFunc(sql, func(value string) string { return f.table(value[2 : len(value)-2]) })
	}
	tx, err := f.db.Begin(f.ctx)
	must(t, "begin exact owned historical DDL", err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	_, err = tx.Exec(f.ctx, render(baseline.LedgerDDL))
	must(t, "create genuine published migration ledger", err)
	for index, migration := range baseline.Migrations {
		hash := sha256.Sum256([]byte(migration.SQL))
		check(t, migration.Version == index+1 && migration.GitBlob != "" && migration.SourcePath != "" &&
			hex.EncodeToString(hash[:]) == migration.SQLSHA256, "published DDL fragment/source/version differs")
		_, err = tx.Exec(f.ctx, render(migration.SQL))
		must(t, "apply exact actual published DDL", err)
		_, err = tx.Exec(f.ctx, "INSERT INTO "+f.table("schema_versions")+" (version) VALUES ($1)", migration.Version)
		must(t, "record only the DDL version actually applied", err)
	}
	must(t, "commit actual published schema V7", tx.Commit(f.ctx))
	check(t, reflect.DeepEqual(ledger(t, f), []string{"1", "2", "3", "4", "5", "6", "7"}), "published V7 starting ledger differs")
	return baseline
}

type legacySeed struct {
	UserID, WorkspaceID, FindingID, ImportID, AssetID string
	Cookie                                            *http.Cookie
	Profile                                           app.AIProfile
	Policy                                            app.AIPolicy
	Grant                                             app.AIEgressGrant
}

func seedPublishedV7(t *testing.T, f *fixture) (legacySeed, string) {
	t.Helper()
	password, key := secret(t), secret(t)
	f.remember(password)
	f.remember(key)
	input := struct {
		DatabaseURL, Schema, ApplicationName, BootstrapToken, Password string
		StorageEndpoint, Bucket, Prefix, AccessKey, SecretKey          string
		EncryptionKey                                                  []byte
		ProviderEndpoint, ProviderKey, Report                          string
	}{f.database.DatabaseURL, f.database.Schema, f.database.ApplicationName + "-v7", f.config.BootstrapToken, password,
		f.config.Storage.Endpoint, f.rawStorage.Bucket, f.rawStorage.Prefix, f.rawStorage.AccessKey, f.rawStorage.SecretKey,
		f.key, f.native.tls.URL, key, string(reportBytes(t, "SYNTHETIC-RAW-NOT-APPROVED: actual pre-upgrade finding."))}
	ctx, cancel := context.WithTimeout(f.ctx, 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, required(t, "ASPM_ASSESSMENT_V7_SEED_EXE"))
	command.Stdin = bytes.NewReader(encoded(t, input))
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") ||
			strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, variable)
	}
	command.Env = append(command.Env, "AWS_EC2_METADATA_DISABLED=true")
	var output, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostic
	must(t, "execute exact published-V7 legitimate API/intake seed", command.Run())
	f.noSecrets(diagnostic.Bytes())
	check(t, output.Len() <= 64<<10, "unbounded private V7 seed reply")
	var value legacySeed
	must(t, "decode private actual V7 seed identities", json.Unmarshal(output.Bytes(), &value))
	check(t, value.Cookie != nil && value.Cookie.HttpOnly && value.Cookie.Secure &&
		value.UserID != "" && value.WorkspaceID != "" && value.FindingID != "" && value.Profile.ID != "" &&
		value.Grant.ID != "" && f.native.count() == 0, "published V7 seed fabricated identity or used a provider")
	f.remember(value.Cookie.Value)
	return value, key
}

func TestAA8PublishedV7ToV8KeepsLegacyAPIDataAndReopens(t *testing.T) {
	t.Run("fresh-v8-and-reopen", func(t *testing.T) {
		h := newHarness(t, false)
		check(t, reflect.DeepEqual(ledger(t, h.fixture), []string{"1", "2", "3", "4", "5", "6", "7", "8"}), "fresh current core did not apply V8")
		finding := h.seed()
		p, key := h.profile("local", true)
		pol, grant := h.approve(p)
		v := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
		job := h.enqueue(h.admin, v, "fresh-v8", 202)
		h.reopen()
		h.native.arm(p, key, v, job.ID, "ok", "inconclusive", false)
		w := h.worker(h.configForWorker("fresh-v8"))
		process(t, h.ctx, w, true)
		h.assertResult(h.job(h.admin, job.ID), p, v, "inconclusive")
		check(t, reflect.DeepEqual(ledger(t, h.fixture), []string{"1", "2", "3", "4", "5", "6", "7", "8"}), "reopen repeated or skipped a migration")
	})
	t.Run("published-v7-upgrade", func(t *testing.T) {
		f := newFixture(t)
		baseline := buildPublishedV7(t, f)
		legacy, key := seedPublishedV7(t, f)
		check(t, reflect.DeepEqual(ledger(t, f), []string{"1", "2", "3", "4", "5", "6", "7"}), "published helper is not actual V7")
		oldDefinitions, oldData := definitions(t, f, baseline.LegacyTables), legacyData(t, f, baseline.LegacyTables)
		for _, name := range []string{"users", "sessions", "assets", "findings", "observations", "notes", "ai_profiles", "ai_policies", "ai_egress_grants"} {
			check(t, len(oldData[name]) != 0, "claimed legacy business data was not created by actual V7 APIs")
		}
		current, err := app.Open(f.ctx, f.config)
		must(t, "open actual current core over genuine populated V7", err)
		t.Cleanup(func() { must(t, "close current migration probe", current.Close()) })
		after := ledger(t, f)
		observation := map[string]any{"origin": publishedOrigin, "startingVersions": []string{"1", "2", "3", "4", "5", "6", "7"},
			"afterCurrentOpen": after, "legacyRelations": len(baseline.LegacyTables), "legacyBusinessRowsCreatedByPublishedAPIs": true,
			"noBusinessSQLSeeds": true, "noLedgerRelabel": true, "oldDataSHA256": digest(encoded(t, oldData))}
		target := filepath.Join(required(t, "ASPM_ASSESSMENT_ARTIFACT_DIR"), "published-v7-upgrade-"+nonce(t)+".json")
		must(t, "record nonsecret actual V7 upgrade observation", os.WriteFile(target, encoded(t, observation), 0600))
		t.Log("actual published-v7 upgrade observation:", target)
		check(t, reflect.DeepEqual(after, []string{"1", "2", "3", "4", "5", "6", "7", "8"}),
			"CURRENT core did not apply the NEW required V8 exactly once over actual published populated V7")
		check(t, reflect.DeepEqual(oldDefinitions, definitions(t, f, baseline.LegacyTables)), "V8 changed a legacy definition")
		check(t, reflect.DeepEqual(oldData, legacyData(t, f, baseline.LegacyTables)), "V8 changed actual pre-upgrade API-created legacy data")
		must(t, "close migration probe before enabled core", current.Close())
		h := &harness{fixture: f, admin: actor{ID: legacy.UserID, Workspace: legacy.WorkspaceID, Cookie: legacy.Cookie}}
		h.open()
		finding := h.finding(h.admin, legacy.FindingID)
		check(t, finding.WorkflowState == "in-progress" && finding.Disposition == "accepted-risk" &&
			len(finding.Notes) == 1 && !finding.VerifiedResolution, "legacy human/source contracts were not preserved")
		v := h.preview(h.admin, finding, legacy.Profile, legacy.Policy, legacy.Grant, reviewedContext())
		job := h.enqueue(h.admin, v, "upgraded-v8", 202)
		before, storage := h.domainSnapshot(), h.storageCalls.Load()
		h.native.arm(legacy.Profile, key, v, job.ID, "ok", "supported", false)
		w := h.worker(h.configForWorker("upgraded-worker"))
		process(t, h.ctx, w, true)
		h.assertResult(h.job(h.admin, job.ID), legacy.Profile, v, "supported")
		h.assertReadonly(before, storage)
		h.reopen()
		fresh := h.worker(h.configForWorker("upgraded-reopened-worker"))
		process(t, h.ctx, fresh, false)
		check(t, reflect.DeepEqual(ledger(t, f), []string{"1", "2", "3", "4", "5", "6", "7", "8"}) &&
			reflect.DeepEqual(oldDefinitions, definitions(t, f, baseline.LegacyTables)), "V8 reopen changed legacy definitions or exact ledger")
		check(t, h.job(h.admin, job.ID).State == "succeeded", "upgraded durable advisory lost on independent reopen")
		t.Log("AA8 exact populated published-V7 -> V8 and legacy contracts/reopen verified")
	})
}
