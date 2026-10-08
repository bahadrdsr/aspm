//go:build integration

package saved_work_views

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/tests/internal/deliverycompat"
	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

const publishedCommit = "629bebd727b448fb89eb732485457fd60cbffbfd"

type legacySeed struct {
	Admin, Viewer actor
	Asset         app.Asset
	Import        app.Import
	Finding       app.Finding
	APICalls      int
	StorageWrites int32
}

func (f *fixture) publishedSeed() legacySeed {
	f.t.Helper()
	path := os.Getenv("ASPM_SAVED_VIEWS_V8_RECEIPT")
	check(f.t, path != "", "BLOCKED: prepare exact published V8 constructor/helper offline first")
	raw, err := os.ReadFile(path)
	must(f.t, "read pinned published fixture build receipt", err)
	var receipt struct {
		Origin, Executable, ExecutableSHA256, HelperSHA256 string
		BusinessSQLSeeds, ProviderCalls, DataExecution     bool
		ExitCode                                           int
		Files                                              []struct{ Path, SHA256, GitBlob string }
	}
	must(f.t, "decode published fixture provenance", json.Unmarshal(raw, &receipt))
	check(f.t, receipt.Origin == publishedCommit && receipt.ExitCode == 0 && len(receipt.Files) > 0 &&
		!receipt.BusinessSQLSeeds && !receipt.ProviderCalls && !receipt.DataExecution,
		"published fixture must be exact production build, never a DDL/business-row imitation")
	executable, err := os.ReadFile(receipt.Executable)
	must(f.t, "read exact compiled published seed", err)
	helper, err := os.ReadFile(filepath.Join("testdata", "seed-v8.main.go.txt"))
	must(f.t, "read authored historical API helper", err)
	check(f.t, digest(executable) == receipt.ExecutableSHA256 && digest(helper) == receipt.HelperSHA256,
		"published constructor/helper executable drifted after build")
	for _, file := range receipt.Files {
		check(f.t, !strings.Contains(file.Path, "..") && !filepath.IsAbs(file.Path) && file.GitBlob != "",
			"published source provenance has an invalid path or no git identity")
		data, err := os.ReadFile(filepath.Join(filepath.Dir(receipt.Executable), file.Path))
		must(f.t, "read pinned fixture source input", err)
		check(f.t, digest(data) == file.SHA256, "published fixture source input drifted")
	}
	password, viewerPassword := secret(f.t), secret(f.t)
	f.remember(password)
	f.remember(viewerPassword)
	st := f.config.Storage
	input := struct {
		DatabaseURL, Schema, ApplicationName, BootstrapToken, Password, ViewerPassword string
		StorageEndpoint, Bucket, Prefix, AccessKey, SecretKey, Report                  string
		Now                                                                            time.Time
	}{f.config.DatabaseURL, f.config.Schema, f.config.ApplicationName + "-v8", f.config.BootstrapToken,
		password, viewerPassword, st.Endpoint, st.Bucket, st.Prefix, st.AccessKey, st.SecretKey,
		string(report(f.t, "Legacy setting")), f.now()}
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, receipt.Executable)
	command.Stdin = bytes.NewReader(encode(f.t, input))
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			strings.HasPrefix(upper, "AZURE_") || strings.HasPrefix(upper, "OPENAI_") ||
			strings.HasPrefix(upper, "ANTHROPIC_") || strings.HasPrefix(upper, "GH_") ||
			strings.HasPrefix(upper, "GITHUB_") || strings.HasPrefix(upper, "SLACK_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, variable)
	}
	command.Env = append(command.Env, "AWS_EC2_METADATA_DISABLED=true")
	var output, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostic
	err = command.Run()
	f.private(diagnostic.Bytes())
	if err != nil {
		f.t.Log(strings.TrimSpace(diagnostic.String()))
	}
	must(f.t, "run actual published V8 HTTP/intake seed", err)
	check(f.t, output.Len() <= 64<<10, "published fixture reply exceeded private budget")
	var value legacySeed
	must(f.t, "decode private legacy identities", json.Unmarshal(output.Bytes(), &value))
	check(f.t, value.Admin.Cookie != nil && value.Viewer.Cookie != nil &&
		value.Admin.User.ID != value.Viewer.User.ID && value.Finding.ID != "" &&
		len(value.Finding.Notes) == 1 && value.APICalls <= 24 && value.StorageWrites <= 40,
		"published helper omitted actual API-created legacy state")
	f.remember(value.Admin.Cookie.Value)
	f.remember(value.Viewer.Cookie.Value)
	f.t.Logf("actual published V8 helper completed: API=%d/24 S3-writes=%d/40", value.APICalls, value.StorageWrites)
	return value
}
func (f *fixture) definitions(names []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range names {
		result[name+"/columns"] = f.rows(`SELECT a.attname||'|'||format_type(a.atttypid,a.atttypmod)||'|'||a.attnotnull::text||'|'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=to_regclass($1) AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, f.table(name))
		result[name+"/constraints"] = f.rows(`SELECT conname||'|'||pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=to_regclass($1) ORDER BY conname`, f.table(name))
		result[name+"/indexes"] = f.rows(`SELECT indexname||'|'||indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename=$2 ORDER BY indexname`, f.config.Schema, "app_"+name)
	}
	return result
}

func TestSavedWorkViewsActualPopulatedPublishedV8Upgrade(t *testing.T) {
	f := newFixture(t)
	same(t, "published fixture must start empty, not by dropping current tables",
		f.rows(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r'`, f.config.Schema), []string{})
	legacy := f.publishedSeed()
	same(t, "published binary did not create actual V8", f.ledger(), []string{"1", "2", "3", "4", "5", "6", "7", "8"})
	names := f.rows(`SELECT substr(c.relname,5) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind='r' AND c.relname<>'app_schema_versions' ORDER BY c.relname`, f.config.Schema)
	before, definitions := f.snapshot(names), f.definitions(names)
	for _, required := range []string{"users", "memberships", "sessions", "assets", "imports", "findings", "observations", "notes"} {
		check(t, len(before[required]) != 0, "claimed legacy data was not actually API-created before upgrade")
	}
	t.Log("actual populated published V8 ledger, assets/findings/notes/roles and legacy definitions verified before current open")
	h := &harness{fixture: f, admin: legacy.Admin}
	h.open()
	after := f.ledger()
	observation := object{"origin": publishedCommit, "startingVersion": 8, "afterCurrentOpen": after,
		"legacyRelations": len(names), "legacyAPIDataSHA256": digest(encode(t, before)),
		"legacyDefinitionsSHA256": digest(encode(t, definitions)), "businessSQLSeeds": false}
	output := filepath.Join("..", "..", ".artifacts", "saved-work-views-v1", "published-v8-upgrade-"+nonce(t)+".json")
	must(t, "record nonsecret actual migration observation", os.WriteFile(output, encode(t, observation), 0600))
	same(t, "current production open did not apply through additive V18 over actual populated published V8",
		after, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18"})
	same(t, "V11 changed a published legacy definition", deliverycompat.ProjectCurrent(t, definitions, f.definitions(names)), definitions)
	same(t, "V18 changed actual published API-created business rows", sourcecompat.ProjectRowsCurrent(t, before, f.snapshot(names)), before)
	checkLegacy := func() {
		t.Helper()
		same(t, "upgraded asset changed", h.json(h.admin, "GET", "/api/v1/assets/"+legacy.Asset.ID, nil, 200).Asset, legacy.Asset)
		same(t, "upgraded import/source provenance changed", h.json(h.admin, "GET", "/api/v1/imports/"+legacy.Import.ID, nil, 200).Import, legacy.Import)
		same(t, "upgraded finding, notes or ownership changed", h.json(legacy.Viewer, "GET", "/api/v1/findings/"+legacy.Finding.ID, nil, 200).Finding, legacy.Finding)
		for _, row := range []struct {
			who  actor
			role string
		}{{legacy.Admin, "admin"}, {legacy.Viewer, "viewer"}} {
			session := h.json(row.who, "GET", "/api/v1/session", nil, 200)
			check(t, len(session.Workspaces) == 1 && session.Workspaces[0].ID == row.who.Workspace &&
				session.Workspaces[0].Role == row.role, "upgrade altered existing workspace role/session")
		}
		h.denied(legacy.Viewer, "PATCH", "/api/v1/findings/"+legacy.Finding.ID, object{"workflowState": "resolved"}, 403, "forbidden")
	}
	checkLegacy()
	v := h.create(legacy.Viewer, "After real upgrade", "Legacy setting", "title")
	same(t, "upgraded saved query lost legacy finding identity", ids(h.work(legacy.Viewer, "viewId="+v.ID)), []string{legacy.Finding.ID})
	h.reopen()
	same(t, "new saved preference lost on upgraded app reopen", h.get(legacy.Viewer, v.ID), v)
	same(t, "upgraded reopen repeated/skipped migration", f.ledger(), after)
	same(t, "upgraded reopen altered legacy definitions", deliverycompat.ProjectCurrent(t, definitions, f.definitions(names)), definitions)
	same(t, "saved preference/reopen changed pre-upgrade legacy rows", sourcecompat.ProjectRowsCurrent(t, before, f.snapshot(names)), before)
	checkLegacy()
	t.Log("actual populated published V8 -> V10, canonical saved view reopen and original human/source/role state reached")
}
