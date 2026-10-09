//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	coverageDrilldownPath               = "/api/v1/reports/coverage-assets"
	coverageDrilldownVerificationReason = "Coverage drill-down reflects successful complete full-scan intake; it is not verification of asset safety."
)

var coverageDrilldownNow = time.Date(2026, 10, 8, 15, 40, 20, 0, time.UTC)

type coverageDrilldownWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Days int       `json:"days"`
}

type coverageDrilldownVerification struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type coverageDrilldownAsset struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Environment string   `json:"environment"`
	Criticality string   `json:"criticality"`
	Tags        []string `json:"tags"`
	OwnerID     *string  `json:"ownerId"`
}

type coverageDrilldownFlags struct {
	Scanned            bool       `json:"scanned"`
	Stale              bool       `json:"stale"`
	UnknownFreshness   bool       `json:"unknownFreshness"`
	LatestSourceScanAt *time.Time `json:"latestSourceScanAt"`
}

type coverageDrilldownItem struct {
	Asset    coverageDrilldownAsset `json:"asset"`
	Coverage coverageDrilldownFlags `json:"coverage"`
}

type coverageDrilldown struct {
	WorkspaceID     string                        `json:"workspaceId"`
	State           string                        `json:"state"`
	FreshnessWindow coverageDrilldownWindow       `json:"freshnessWindow"`
	Items           []coverageDrilldownItem       `json:"items"`
	Total           int                           `json:"total"`
	NextCursor      *string                       `json:"nextCursor"`
	Verification    coverageDrilldownVerification `json:"verification"`
}

type coverageDrilldownEnvelope struct {
	APIVersion string            `json:"apiVersion"`
	DataOrigin string            `json:"dataOrigin"`
	Drilldown  coverageDrilldown `json:"drilldown"`
}

type coverageImportSeed struct {
	ID, WorkspaceID, AssetID, SourceID, ScanID, ScopeID, ScopeRevision, ScopeBranch string
	State, SourceStatus, ScanKind, Completeness                                     string
	SourceScanAt                                                                    *time.Time
	CollectedAt, ImportedAt                                                         time.Time
}

type coverageClock struct {
	armed atomic.Bool
	calls atomic.Int32
}

func (c *coverageClock) now() time.Time {
	if c.armed.Load() {
		c.calls.Add(1)
	}
	return coverageDrilldownNow
}

func (c *coverageClock) start() {
	c.calls.Store(0)
	c.armed.Store(true)
}

func (c *coverageClock) stop() int32 {
	c.armed.Store(false)
	return c.calls.Load()
}

func coverageJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	ok(t, "decode "+label+" object", json.Unmarshal(raw, &value))
	got := make(map[string]bool, len(value))
	for key := range value {
		got[key] = true
	}
	if len(got) != len(want) {
		t.Fatalf("%s keys got %v, want exactly %v", label, got, want)
	}
	for _, key := range want {
		if !got[key] {
			t.Fatalf("%s omitted required key %q", label, key)
		}
	}
	return value
}

func decodeCoverageDrilldown(t *testing.T, body []byte) coverageDrilldownEnvelope {
	t.Helper()
	envelope := coverageJSONKeys(t, body, "coverage drill-down envelope", "apiVersion", "dataOrigin", "drilldown")
	drilldown := coverageJSONKeys(t, envelope["drilldown"], "coverage drill-down",
		"workspaceId", "state", "freshnessWindow", "items", "total", "nextCursor", "verification")
	coverageJSONKeys(t, drilldown["freshnessWindow"], "coverage drill-down freshness window", "from", "to", "days")
	coverageJSONKeys(t, drilldown["verification"], "coverage drill-down verification", "state", "reason")
	var items []json.RawMessage
	ok(t, "decode coverage drill-down item array", json.Unmarshal(drilldown["items"], &items))
	for index, raw := range items {
		item := coverageJSONKeys(t, raw, fmt.Sprintf("coverage drill-down item %d", index), "asset", "coverage")
		coverageJSONKeys(t, item["asset"], "coverage drill-down asset",
			"id", "workspaceId", "name", "kind", "environment", "criticality", "tags", "ownerId")
		coverageJSONKeys(t, item["coverage"], "coverage drill-down flags",
			"scanned", "stale", "unknownFreshness", "latestSourceScanAt")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result coverageDrilldownEnvelope
	ok(t, "strictly decode coverage drill-down response", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("coverage drill-down response contains trailing JSON")
	}
	if result.Drilldown.Items == nil {
		t.Fatal("coverage drill-down items must be an array, not null")
	}
	return result
}

func coverageID(value int) string {
	return fmt.Sprintf("%032x", value)
}

func coverageTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func seedCoverageAsset(t *testing.T, h *harness, id, workspace, name string, owner *string, tags ...string) coverageDrilldownAsset {
	t.Helper()
	if tags == nil {
		tags = []string{}
	}
	asset := coverageDrilldownAsset{
		ID: id, WorkspaceID: workspace, Name: name, Kind: "repository", Environment: "test",
		Criticality: "medium", Tags: tags, OwnerID: owner,
	}
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+
		pgx.Identifier{h.services.cfg.Schema, "app_assets"}.Sanitize()+`
		(id,workspace_id,name,kind,environment,criticality,tags,owner_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		asset.ID, asset.WorkspaceID, asset.Name, asset.Kind, asset.Environment,
		asset.Criticality, asset.Tags, asset.OwnerID, coverageDrilldownNow.Add(-24*time.Hour))
	ok(t, "seed coverage drill-down asset", err)
	return asset
}

func seedCoverageImport(t *testing.T, h *harness, seed coverageImportSeed) {
	t.Helper()
	if seed.SourceID == "" {
		seed.SourceID = "coverage-source"
	}
	if seed.ScanID == "" {
		seed.ScanID = "scan-" + seed.ID
	}
	if seed.ScopeID == "" {
		seed.ScopeID = "coverage-scope"
	}
	if seed.ScopeRevision == "" {
		seed.ScopeRevision = "1"
	}
	if seed.ScopeBranch == "" {
		seed.ScopeBranch = "refs/heads/main"
	}
	if seed.State == "" {
		seed.State = "succeeded"
	}
	if seed.SourceStatus == "" {
		seed.SourceStatus = "succeeded"
	}
	if seed.ScanKind == "" {
		seed.ScanKind = "full"
	}
	if seed.Completeness == "" {
		seed.Completeness = "complete"
	}
	if seed.CollectedAt.IsZero() {
		seed.CollectedAt = coverageDrilldownNow.Add(-time.Hour)
	}
	if seed.ImportedAt.IsZero() {
		seed.ImportedAt = coverageDrilldownNow.Add(-30 * time.Minute)
	}
	sum := sha256.Sum256([]byte(seed.ID))
	digest := "sha256:" + fmt.Sprintf("%x", sum[:])
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+
		pgx.Identifier{h.services.cfg.Schema, "app_imports"}.Sanitize()+`
		(id,run_id,workspace_id,asset_id,source_id,scan_id,scope_id,scope_revision,scope_branch,
		 format,mapping,source_scan_at,collected_at,imported_at,source_status,scan_kind,completeness,
		 report_digest,report_key,report_size,metadata_digest,state,observation_count,submitted_by,
		 failure_code,failure_message)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'sarif','{}'::jsonb,$10,$11,$12,$13,$14,$15,
		 $16,$17,0,$18,$19,0,$20,$21,$22)`,
		seed.ID, "run-"+seed.ID, seed.WorkspaceID, seed.AssetID, seed.SourceID, seed.ScanID,
		seed.ScopeID, seed.ScopeRevision, seed.ScopeBranch, seed.SourceScanAt, seed.CollectedAt,
		seed.ImportedAt, seed.SourceStatus, seed.ScanKind, seed.Completeness, digest,
		"coverage/"+seed.ID+".json", digest, seed.State, h.admin.user.ID,
		func() string {
			if seed.State == "failed" {
				return "synthetic-failure"
			}
			return ""
		}(),
		func() string {
			if seed.State == "failed" {
				return "Synthetic failed import control."
			}
			return ""
		}(),
	)
	ok(t, "seed coverage drill-down import", err)
}

func newCoverageDrilldownHarness(t *testing.T, tracer pgx.QueryTracer, storage bool) (*harness, *historicalStorageTripwire, *coverageClock) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	clock := &coverageClock{}
	h.clock.Store(coverageDrilldownNow.UnixNano())
	h.services.cfg.Now = clock.now
	h.services.cfg.BootstrapToken = secret(t)
	h.services.cfg.LogOutput = io.Discard
	h.services.cfg.QueryTracer = tracer
	var tripwire *historicalStorageTripwire
	if storage {
		tripwire = newHistoricalStorageTripwire(t, h.services)
	}
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close coverage drill-down application", h.app.Close())
		}
	})
	h.enroll()
	return h, tripwire, clock
}

func requestCoverage(t *testing.T, h *harness, clock *coverageClock, who actor, path string, body []byte) *coverageDrilldownEnvelope {
	t.Helper()
	clock.start()
	response := h.request(who, "GET", path, body, 0)
	calls := clock.stop()
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; response withheld", path, response.Code)
	}
	if calls != 2 {
		t.Fatalf("coverage drill-down request must use Now once after authentication: calls=%d", calls)
	}
	result := decodeCoverageDrilldown(t, response.Body.Bytes())
	return &result
}

func coverageMember(flags coverageDrilldownFlags, state string) bool {
	switch state {
	case "scanned":
		return flags.Scanned
	case "unscanned":
		return !flags.Scanned
	case "stale":
		return flags.Stale
	case "unknown-freshness":
		return flags.UnknownFreshness
	default:
		return false
	}
}

func expectedCoveragePage(assets []coverageDrilldownAsset, flags map[string]coverageDrilldownFlags, state string, limit int, cursor string) ([]coverageDrilldownItem, int, *string) {
	all := make([]coverageDrilldownItem, 0, len(assets))
	for _, asset := range assets {
		if asset.ID <= cursor || !coverageMember(flags[asset.ID], state) {
			continue
		}
		all = append(all, coverageDrilldownItem{Asset: asset, Coverage: flags[asset.ID]})
	}
	total := 0
	for _, asset := range assets {
		if coverageMember(flags[asset.ID], state) {
			total++
		}
	}
	if len(all) <= limit {
		return all, total, nil
	}
	page := all[:limit]
	next := page[len(page)-1].Asset.ID
	return page, total, &next
}

func assertCoveragePage(t *testing.T, got coverageDrilldownEnvelope, workspace, state string, days int,
	wantItems []coverageDrilldownItem, wantTotal int, wantCursor *string) {
	t.Helper()
	equal(t, "coverage drill-down API version", got.APIVersion, apiVersion)
	equal(t, "coverage drill-down live origin", got.DataOrigin, "live")
	equal(t, "coverage drill-down workspace", got.Drilldown.WorkspaceID, workspace)
	equal(t, "coverage drill-down state", got.Drilldown.State, state)
	equal(t, "coverage drill-down freshness days", got.Drilldown.FreshnessWindow.Days, days)
	if !got.Drilldown.FreshnessWindow.From.Equal(coverageDrilldownNow.Add(-time.Duration(days)*24*time.Hour)) ||
		!got.Drilldown.FreshnessWindow.To.Equal(coverageDrilldownNow) {
		t.Fatal("coverage drill-down did not preserve its exact inclusive server-Now window")
	}
	if got.Drilldown.Verification.State != "not-run" ||
		got.Drilldown.Verification.Reason != coverageDrilldownVerificationReason {
		t.Fatal("coverage drill-down changed the fixed not-run verification statement")
	}
	equal(t, "coverage drill-down exact whole-filter total", got.Drilldown.Total, wantTotal)
	equal(t, "coverage drill-down native next cursor", got.Drilldown.NextCursor, wantCursor)
	if !reflect.DeepEqual(got.Drilldown.Items, wantItems) {
		t.Fatalf("coverage drill-down items got %#v, want exact %#v", got.Drilldown.Items, wantItems)
	}
	seen := make(map[string]bool, len(got.Drilldown.Items))
	last := ""
	for _, item := range got.Drilldown.Items {
		if len(item.Asset.ID) != 32 || strings.ToLower(item.Asset.ID) != item.Asset.ID ||
			strings.IndexFunc(item.Asset.ID, func(r rune) bool {
				return (r < '0' || r > '9') && (r < 'a' || r > 'f')
			}) >= 0 {
			t.Fatal("coverage drill-down returned a noncanonical asset ID")
		}
		if item.Asset.WorkspaceID != workspace || item.Asset.ID <= last || seen[item.Asset.ID] {
			t.Fatal("coverage drill-down asset order, uniqueness, or workspace authority is invalid")
		}
		if !coverageMember(item.Coverage, state) {
			t.Fatal("coverage drill-down item does not satisfy the requested state")
		}
		if !item.Coverage.Scanned &&
			(item.Coverage.Stale || item.Coverage.UnknownFreshness || item.Coverage.LatestSourceScanAt != nil) {
			t.Fatal("unscanned coverage flags fabricated stale, unknown, or source time state")
		}
		if (item.Coverage.Stale || item.Coverage.UnknownFreshness) && !item.Coverage.Scanned {
			t.Fatal("stale or unknown-freshness assets must also be scanned")
		}
		if item.Coverage.LatestSourceScanAt != nil &&
			item.Coverage.LatestSourceScanAt.After(got.Drilldown.FreshnessWindow.To) {
			t.Fatal("coverage drill-down exposed a future source scan time")
		}
		seen[item.Asset.ID], last = true, item.Asset.ID
	}
	if wantCursor != nil && (len(got.Drilldown.Items) == 0 ||
		*wantCursor != got.Drilldown.Items[len(got.Drilldown.Items)-1].Asset.ID) {
		t.Fatal("coverage drill-down nextCursor is not the last returned ID")
	}
}

func seedExactCoverageMembership(t *testing.T, h *harness, viewer actor, foreign actor) ([]coverageDrilldownAsset, map[string]coverageDrilldownFlags, coverageDrilldownAsset) {
	t.Helper()
	from := coverageDrilldownNow.Add(-7 * 24 * time.Hour)
	fresh := coverageDrilldownNow.Add(-time.Hour)
	stale := from.Add(-time.Second)
	assets := []coverageDrilldownAsset{
		seedCoverageAsset(t, h, coverageID(1), h.admin.workspace, "Fresh assessed repository", &h.admin.user.ID, "synthetic", "fresh"),
		seedCoverageAsset(t, h, coverageID(2), h.admin.workspace, "Stale assessed repository", &viewer.user.ID, "synthetic", "stale"),
		seedCoverageAsset(t, h, coverageID(3), h.admin.workspace, "Unknown freshness repository", nil, "synthetic", "unknown"),
		seedCoverageAsset(t, h, coverageID(4), h.admin.workspace, "Overlapping stale and unknown repository", nil, "synthetic", "overlap"),
		seedCoverageAsset(t, h, coverageID(5), h.admin.workspace, "Control-only unscanned repository", nil, "synthetic", "control"),
		seedCoverageAsset(t, h, coverageID(6), h.admin.workspace, "Inclusive freshness start repository", nil, "synthetic", "boundary"),
		seedCoverageAsset(t, h, coverageID(7), h.admin.workspace, "Inclusive freshness end repository", nil, "synthetic", "boundary"),
		seedCoverageAsset(t, h, coverageID(8), h.admin.workspace, "Latest grouped scan repository", nil, "synthetic", "grouped"),
		seedCoverageAsset(t, h, coverageID(9), h.admin.workspace, "Any stale scope repository", nil, "synthetic", "scopes"),
		seedCoverageAsset(t, h, coverageID(10), h.admin.workspace, "Future timestamp ignored repository", nil, "synthetic", "future-control"),
	}
	nextImport := 1000
	add := func(asset coverageDrilldownAsset, source, scope string, at *time.Time, change func(*coverageImportSeed)) {
		nextImport++
		seed := coverageImportSeed{
			ID: coverageID(nextImport), WorkspaceID: asset.WorkspaceID, AssetID: asset.ID,
			SourceID: source, ScanID: fmt.Sprintf("coverage-scan-%04d", nextImport),
			ScopeID: scope, ScopeRevision: "1", ScopeBranch: "refs/heads/main",
			SourceScanAt: at, CollectedAt: coverageDrilldownNow.Add(-20 * time.Minute),
			ImportedAt: coverageDrilldownNow.Add(-10 * time.Minute),
		}
		if change != nil {
			change(&seed)
		}
		seedCoverageImport(t, h, seed)
	}
	add(assets[0], "source-fresh", "scope-fresh", coverageTime(fresh), nil)
	add(assets[1], "source-stale", "scope-stale", coverageTime(stale), nil)
	add(assets[2], "source-unknown", "scope-unknown", nil, nil)
	add(assets[3], "source-overlap", "scope-stale", coverageTime(from.Add(-2*time.Hour)), nil)
	add(assets[3], "source-overlap", "scope-unknown", nil, nil)
	add(assets[5], "source-start", "scope-start", coverageTime(from), nil)
	add(assets[6], "source-end", "scope-end", coverageTime(coverageDrilldownNow), nil)
	add(assets[7], "source-grouped", "scope-grouped", coverageTime(from.Add(-time.Hour)), nil)
	groupedLatest := coverageDrilldownNow.Add(-30 * time.Minute)
	add(assets[7], "source-grouped", "scope-grouped", coverageTime(groupedLatest), nil)
	anyScopeLatest := coverageDrilldownNow.Add(-15 * time.Minute)
	add(assets[8], "source-scopes", "scope-current", coverageTime(anyScopeLatest), nil)
	add(assets[8], "source-scopes", "scope-old", coverageTime(from.Add(-time.Minute)), nil)
	knownBeforeFuture := from.Add(time.Hour)
	add(assets[9], "source-future", "scope-future", coverageTime(knownBeforeFuture), nil)
	add(assets[9], "source-future", "scope-future", coverageTime(coverageDrilldownNow.Add(time.Hour)), nil)
	add(assets[4], "source-state-failed", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.State = "failed"
	})
	add(assets[4], "source-status-failed", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.SourceStatus = "failed"
	})
	add(assets[4], "source-partial", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.Completeness = "partial"
	})
	add(assets[4], "source-unknown-completeness", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.Completeness = "unknown"
	})
	add(assets[4], "source-delta", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.ScanKind = "delta"
	})
	add(assets[4], "source-queued", "scope-control", coverageTime(fresh), func(seed *coverageImportSeed) {
		seed.State = "queued"
	})
	foreignAsset := seedCoverageAsset(t, h, coverageID(900), foreign.workspace,
		"Foreign workspace assessed repository", &foreign.user.ID, "synthetic", "foreign")
	add(foreignAsset, "source-foreign", "scope-foreign", coverageTime(fresh), nil)

	flags := map[string]coverageDrilldownFlags{
		assets[0].ID: {Scanned: true, LatestSourceScanAt: coverageTime(fresh)},
		assets[1].ID: {Scanned: true, Stale: true, LatestSourceScanAt: coverageTime(stale)},
		assets[2].ID: {Scanned: true, UnknownFreshness: true},
		assets[3].ID: {Scanned: true, Stale: true, UnknownFreshness: true, LatestSourceScanAt: coverageTime(from.Add(-2 * time.Hour))},
		assets[4].ID: {},
		assets[5].ID: {Scanned: true, LatestSourceScanAt: coverageTime(from)},
		assets[6].ID: {Scanned: true, LatestSourceScanAt: coverageTime(coverageDrilldownNow)},
		assets[7].ID: {Scanned: true, LatestSourceScanAt: coverageTime(groupedLatest)},
		assets[8].ID: {Scanned: true, Stale: true, LatestSourceScanAt: coverageTime(anyScopeLatest)},
		assets[9].ID: {Scanned: true, LatestSourceScanAt: coverageTime(knownBeforeFuture)},
	}
	return assets, flags, foreignAsset
}

func coverageTableCount(t *testing.T, h *harness) int {
	t.Helper()
	var count int
	ok(t, "count coverage cache rows", h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_coverage"}.Sanitize()+` WHERE workspace_id=$1`,
		h.admin.workspace).Scan(&count))
	return count
}

func TestM10_CoverageDrilldownProjectsExactCurrentMembershipWithoutSideEffects(t *testing.T) {
	h, storage, clock := newCoverageDrilldownHarness(t, nil, true)
	viewer := h.addUser(h.admin, "viewer")
	foreignAdmin := h.addWorkspace()
	foreignViewer := h.addUser(foreignAdmin, "viewer")
	assets, flags, foreignAsset := seedExactCoverageMembership(t, h, viewer, foreignViewer)

	completedAt := coverageDrilldownNow.Add(-2 * time.Hour)
	saved := historicalReport(h.admin.workspace, completedAt, 0)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: coverageID(950), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Coverage drill-down immutable snapshot control", State: "succeeded",
		CompletedAt: &completedAt, Report: &saved,
	})
	historyBefore := append([]byte(nil), h.request(h.admin, "GET", "/api/v1/reports/snapshots?limit=100", nil, 200).Body.Bytes()...)
	detailBefore := append([]byte(nil), h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+coverageID(950), nil, 200).Body.Bytes()...)
	overviewBefore := append([]byte(nil), h.request(h.admin, "GET", "/api/v1/reports/overview?freshnessDays=7", nil, 200).Body.Bytes()...)
	currentOverview := overview(t, h, h.admin)
	stateBefore := historicalState(t, h)
	coverageRowsBefore := coverageTableCount(t, h)
	wantVersions := make([]int, 26)
	for index := range wantVersions {
		wantVersions[index] = index + 1
	}
	equal(t, "coverage drill-down schema stays at V26", stateBefore.Versions, wantVersions)
	equal(t, "live overview scanned membership", currentOverview.Coverage.ScannedAssets, 9)
	equal(t, "live overview unscanned membership", currentOverview.Coverage.UnscannedAssets, 1)
	equal(t, "live overview stale membership", currentOverview.Coverage.StaleAssets, 3)
	equal(t, "live overview unknown-freshness membership", currentOverview.Coverage.UnknownFreshnessAssets, 2)
	storage.arm()

	results := make(map[string]coverageDrilldownEnvelope)
	for _, state := range []string{"scanned", "unscanned", "stale", "unknown-freshness"} {
		who := h.admin
		if state == "scanned" {
			who = viewer
		}
		result := requestCoverage(t, h, clock, who,
			coverageDrilldownPath+"?state="+state+"&freshnessDays=7&limit=100", nil)
		wantItems, wantTotal, wantCursor := expectedCoveragePage(assets, flags, state, 100, "")
		assertCoveragePage(t, *result, h.admin.workspace, state, 7, wantItems, wantTotal, wantCursor)
		results[state] = *result
	}
	equal(t, "drill-down scanned total matches live overview", results["scanned"].Drilldown.Total, currentOverview.Coverage.ScannedAssets)
	equal(t, "drill-down unscanned total matches live overview", results["unscanned"].Drilldown.Total, currentOverview.Coverage.UnscannedAssets)
	equal(t, "drill-down stale total matches live overview", results["stale"].Drilldown.Total, currentOverview.Coverage.StaleAssets)
	equal(t, "drill-down unknown total matches live overview", results["unknown-freshness"].Drilldown.Total, currentOverview.Coverage.UnknownFreshnessAssets)
	if len(results["stale"].Drilldown.Items) != 3 || len(results["unknown-freshness"].Drilldown.Items) != 2 {
		t.Fatal("coverage drill-down did not preserve exact state memberships")
	}
	overlap := assets[3].ID
	for _, state := range []string{"scanned", "stale", "unknown-freshness"} {
		found := false
		for _, item := range results[state].Drilldown.Items {
			if item.Asset.ID == overlap {
				found = item.Coverage.Scanned && item.Coverage.Stale && item.Coverage.UnknownFreshness
			}
		}
		if !found {
			t.Fatalf("overlap asset is missing from %s membership or lost independent flags", state)
		}
	}

	adminScanned := requestCoverage(t, h, clock, h.admin,
		coverageDrilldownPath+"?state=scanned&freshnessDays=7&limit=100", nil)
	if !reflect.DeepEqual(*adminScanned, results["scanned"]) {
		t.Fatal("coverage drill-down response changed by current workspace role")
	}
	foreignFlags := map[string]coverageDrilldownFlags{
		foreignAsset.ID: {Scanned: true, LatestSourceScanAt: coverageTime(coverageDrilldownNow.Add(-time.Hour))},
	}
	foreignResult := requestCoverage(t, h, clock, foreignViewer,
		coverageDrilldownPath+"?state=scanned&freshnessDays=7&limit=100",
		encode(t, object{"workspaceId": h.admin.workspace}))
	foreignItems, foreignTotal, foreignCursor := expectedCoveragePage(
		[]coverageDrilldownAsset{foreignAsset}, foreignFlags, "scanned", 100, "")
	assertCoveragePage(t, *foreignResult, foreignAdmin.workspace, "scanned", 7,
		foreignItems, foreignTotal, foreignCursor)

	if after := historicalState(t, h); !reflect.DeepEqual(after, stateBefore) {
		t.Fatal("coverage drill-down read mutated assets, imports, snapshots, findings, provider, retention, archive, or schema state")
	}
	equal(t, "coverage drill-down did not write coverage cache rows", coverageTableCount(t, h), coverageRowsBefore)
	if !bytes.Equal(historyBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots?limit=100", nil, 200).Body.Bytes()) ||
		!bytes.Equal(detailBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+coverageID(950), nil, 200).Body.Bytes()) {
		t.Fatal("coverage drill-down changed existing snapshot history or detail bytes")
	}
	if !bytes.Equal(overviewBefore, h.request(h.admin, "GET", "/api/v1/reports/overview?freshnessDays=7", nil, 200).Body.Bytes()) {
		t.Fatal("coverage drill-down changed the existing live overview response")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("coverage drill-down or its report controls performed object-store I/O")
	}
}

func TestM10_CoverageDrilldownValidatesQueriesAuthorityPagingAndFutureOnlyControls(t *testing.T) {
	t.Run("query and cursor contract", func(t *testing.T) {
		h, _, clock := newCoverageDrilldownHarness(t, nil, false)
		assets := make([]coverageDrilldownAsset, 0, 101)
		flags := make(map[string]coverageDrilldownFlags, 101)
		for index := 1; index <= 101; index++ {
			asset := seedCoverageAsset(t, h, coverageID(index), h.admin.workspace,
				fmt.Sprintf("Paged unscanned repository %03d", index), nil, "synthetic", "paged")
			assets = append(assets, asset)
			flags[asset.ID] = coverageDrilldownFlags{}
		}
		first := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=unscanned&freshnessDays=1", nil)
		wantItems, wantTotal, wantCursor := expectedCoveragePage(assets, flags, "unscanned", 100, "")
		assertCoveragePage(t, *first, h.admin.workspace, "unscanned", 1, wantItems, wantTotal, wantCursor)
		if wantCursor == nil || *wantCursor != coverageID(100) {
			t.Fatal("coverage drill-down default limit did not produce the exact native cursor")
		}
		second := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=unscanned&freshnessDays=1&cursor="+*wantCursor, nil)
		secondItems, secondTotal, secondCursor := expectedCoveragePage(assets, flags, "unscanned", 100, *wantCursor)
		assertCoveragePage(t, *second, h.admin.workspace, "unscanned", 1,
			secondItems, secondTotal, secondCursor)
		pastEndCursor := strings.Repeat("f", 32)
		pastEnd := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=unscanned&freshnessDays=1&cursor="+pastEndCursor, nil)
		pastEndItems, pastEndTotal, pastEndNext := expectedCoveragePage(
			assets, flags, "unscanned", 100, pastEndCursor)
		assertCoveragePage(t, *pastEnd, h.admin.workspace, "unscanned", 1,
			pastEndItems, pastEndTotal, pastEndNext)
		one := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=unscanned&freshnessDays=1&limit=1", nil)
		oneItems, oneTotal, oneCursor := expectedCoveragePage(assets, flags, "unscanned", 1, "")
		assertCoveragePage(t, *one, h.admin.workspace, "unscanned", 1, oneItems, oneTotal, oneCursor)
		empty := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=scanned&freshnessDays=365&limit=100", nil)
		assertCoveragePage(t, *empty, h.admin.workspace, "scanned", 365,
			[]coverageDrilldownItem{}, 0, nil)

		for _, path := range []string{
			coverageDrilldownPath,
			coverageDrilldownPath + "?freshnessDays=7",
			coverageDrilldownPath + "?state=scanned",
			coverageDrilldownPath + "?state=&freshnessDays=7",
			coverageDrilldownPath + "?state=Scanned&freshnessDays=7",
			coverageDrilldownPath + "?state=unknown&freshnessDays=7",
			coverageDrilldownPath + "?state=scanned&state=stale&freshnessDays=7",
			coverageDrilldownPath + "?state=scanned&freshnessDays=",
			coverageDrilldownPath + "?state=scanned&freshnessDays=0",
			coverageDrilldownPath + "?state=scanned&freshnessDays=366",
			coverageDrilldownPath + "?state=scanned&freshnessDays=-1",
			coverageDrilldownPath + "?state=scanned&freshnessDays=1.0",
			coverageDrilldownPath + "?state=scanned&freshnessDays=1e2",
			coverageDrilldownPath + "?state=scanned&freshnessDays=%2B1",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&freshnessDays=8",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=0",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=101",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=-1",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=1.0",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=1e2",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=%2B1",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&limit=1&limit=2",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&cursor=",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&cursor=abc",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&cursor=" + strings.Repeat("A", 32),
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&cursor=" + coverageID(1) + "&cursor=" + coverageID(2),
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&workspaceId=" + h.admin.workspace,
			coverageDrilldownPath + "?State=scanned&freshnessDays=7",
			coverageDrilldownPath + "?state=scanned&FreshnessDays=7",
			coverageDrilldownPath + "?state=scanned&freshnessDays=7&page=1",
		} {
			h.denied(h.admin, "GET", path, nil, 400, "invalid-input")
		}
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			h.denied(h.admin, method,
				coverageDrilldownPath+"?state=scanned&freshnessDays=7",
				object{"workspaceId": h.admin.workspace}, 405, "method-not-allowed")
		}
	})

	t.Run("authentication and membership", func(t *testing.T) {
		h, _, _ := newCoverageDrilldownHarness(t, nil, false)
		path := coverageDrilldownPath + "?state=unscanned&freshnessDays=7"
		h.denied(actor{}, "GET", path, nil, 401, "unauthorized")

		expired := h.addUser(h.admin, "viewer")
		expiredHash := sha256.Sum256([]byte(expired.cookie.Value))
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
			pgx.Identifier{h.services.cfg.Schema, "app_sessions"}.Sanitize()+`
			SET expires_at=$2 WHERE token_hash=$1`, expiredHash[:], coverageDrilldownNow)
		ok(t, "expire coverage drill-down session", err)
		h.denied(expired, "GET", path, nil, 401, "unauthorized")

		revoked := h.addUser(h.admin, "viewer")
		revokedHash := sha256.Sum256([]byte(revoked.cookie.Value))
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+
			pgx.Identifier{h.services.cfg.Schema, "app_sessions"}.Sanitize()+`
			SET revoked_at=$2 WHERE token_hash=$1`, revokedHash[:], coverageDrilldownNow)
		ok(t, "revoke coverage drill-down session", err)
		h.denied(revoked, "GET", path, nil, 401, "unauthorized")

		unavailable := h.addUser(h.admin, "viewer")
		_, err = h.services.db.Exec(h.services.ctx, `DELETE FROM `+
			pgx.Identifier{h.services.cfg.Schema, "app_memberships"}.Sanitize()+`
			WHERE workspace_id=$1 AND user_id=$2`, unavailable.workspace, unavailable.user.ID)
		ok(t, "remove coverage drill-down membership", err)
		h.denied(unavailable, "GET", path, nil, 403, "forbidden")

		other := h.addUser(h.addWorkspace(), "viewer")
		forged := other
		forged.workspace = h.admin.workspace
		h.denied(forged, "GET", path, nil, 403, "forbidden")
	})

	t.Run("future-only import does not establish coverage", func(t *testing.T) {
		h, _, clock := newCoverageDrilldownHarness(t, nil, false)
		asset := seedCoverageAsset(t, h, coverageID(1), h.admin.workspace,
			"Future-only source time repository", nil, "synthetic", "future-only")
		seedCoverageImport(t, h, coverageImportSeed{
			ID: coverageID(2000), WorkspaceID: h.admin.workspace, AssetID: asset.ID,
			SourceID: "source-future-only", ScanID: "coverage-future-only",
			ScopeID: "scope-future-only", ScopeRevision: "1", ScopeBranch: "refs/heads/main",
			SourceScanAt: coverageTime(coverageDrilldownNow.Add(time.Second)),
			CollectedAt:  coverageDrilldownNow.Add(-time.Hour), ImportedAt: coverageDrilldownNow.Add(-30 * time.Minute),
		})
		flags := map[string]coverageDrilldownFlags{asset.ID: {}}
		unscanned := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=unscanned&freshnessDays=7&limit=100", nil)
		items, total, cursor := expectedCoveragePage(
			[]coverageDrilldownAsset{asset}, flags, "unscanned", 100, "")
		assertCoveragePage(t, *unscanned, h.admin.workspace, "unscanned", 7, items, total, cursor)
		scanned := requestCoverage(t, h, clock, h.admin,
			coverageDrilldownPath+"?state=scanned&freshnessDays=7&limit=100", nil)
		assertCoveragePage(t, *scanned, h.admin.workspace, "scanned", 7,
			[]coverageDrilldownItem{}, 0, nil)
		current := overview(t, h, h.admin)
		equal(t, "future-only control stays unscanned in Live overview", current.Coverage.UnscannedAssets, 1)
		equal(t, "future-only control does not inflate Live overview scanned", current.Coverage.ScannedAssets, 0)
		equal(t, "future-only control does not fabricate stale membership", current.Coverage.StaleAssets, 0)
		equal(t, "future-only control does not fabricate unknown freshness", current.Coverage.UnknownFreshnessAssets, 0)
	})
}

type coverageTraceMarker struct{}

type coverageDrilldownTracer struct {
	gate          *securityGate
	once          sync.Once
	armed         atomic.Bool
	inTransaction atomic.Bool
	begins        atomic.Int32
	commits       atomic.Int32
	rollbacks     atomic.Int32
	selects       atomic.Int32
	writes        atomic.Int32
	locks         atomic.Int32
	readOnly      atomic.Bool
}

func newCoverageDrilldownTracer() *coverageDrilldownTracer {
	return &coverageDrilldownTracer{gate: newSecurityGate()}
}

func (t *coverageDrilldownTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.armed.Load() {
		return ctx
	}
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != "coverage-drilldown-read" {
		return ctx
	}
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.HasPrefix(sql, "begin") && strings.Contains(sql, "repeatable read") {
		t.begins.Add(1)
		t.inTransaction.Store(true)
		if strings.Contains(sql, "read only") {
			t.readOnly.Store(true)
		}
	}
	if t.inTransaction.Load() {
		if strings.Contains(sql, " insert ") || strings.HasPrefix(sql, "insert ") ||
			strings.Contains(sql, " update ") || strings.HasPrefix(sql, "update ") ||
			strings.Contains(sql, " delete ") || strings.HasPrefix(sql, "delete ") {
			t.writes.Add(1)
		}
		if strings.Contains(sql, "for update") || strings.Contains(sql, "for share") {
			t.locks.Add(1)
		}
	}
	if strings.Contains(sql, "app_assets") {
		t.selects.Add(1)
		return context.WithValue(ctx, coverageTraceMarker{}, true)
	}
	if strings.HasPrefix(sql, "commit") {
		t.commits.Add(1)
		t.inTransaction.Store(false)
	}
	if strings.HasPrefix(sql, "rollback") {
		t.rollbacks.Add(1)
		t.inTransaction.Store(false)
	}
	return ctx
}

func (t *coverageDrilldownTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(coverageTraceMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func TestM10_CoverageDrilldownUsesOneStableReadOnlyRepeatableReadSnapshot(t *testing.T) {
	tracer := newCoverageDrilldownTracer()
	h, _, clock := newCoverageDrilldownHarness(t, tracer, false)
	initial := seedCoverageAsset(t, h, coverageID(1), h.admin.workspace,
		"Stable coverage asset before concurrent change", nil, "synthetic", "stable")
	initialScan := coverageDrilldownNow.Add(-time.Hour)
	seedCoverageImport(t, h, coverageImportSeed{
		ID: coverageID(3000), WorkspaceID: h.admin.workspace, AssetID: initial.ID,
		SourceID: "source-stable", ScanID: "coverage-stable-initial",
		ScopeID: "scope-stable", ScopeRevision: "1", ScopeBranch: "refs/heads/main",
		SourceScanAt: &initialScan,
	})

	clock.start()
	tracer.armed.Store(true)
	future := securityRequest(h, h.admin, "GET",
		coverageDrilldownPath+"?state=scanned&freshnessDays=7&limit=100",
		nil, 15*time.Second, "coverage-drilldown-read")
	select {
	case <-tracer.gate.entered:
	case <-future.done:
		t.Fatalf("coverage drill-down endpoint returned status %d before a real asset query", future.result.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("coverage drill-down endpoint did not reach its bounded asset query")
	}
	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
		pgx.Identifier{h.services.cfg.Schema, "app_assets"}.Sanitize()+`
		SET name=$3 WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, initial.ID, "Concurrent changed coverage asset")
	ok(t, "commit concurrent coverage asset change", err)
	late := seedCoverageAsset(t, h, coverageID(2), h.admin.workspace,
		"Concurrent added coverage asset", nil, "synthetic", "late")
	lateScan := coverageDrilldownNow.Add(-30 * time.Minute)
	seedCoverageImport(t, h, coverageImportSeed{
		ID: coverageID(3001), WorkspaceID: h.admin.workspace, AssetID: late.ID,
		SourceID: "source-late", ScanID: "coverage-stable-late",
		ScopeID: "scope-late", ScopeRevision: "1", ScopeBranch: "refs/heads/main",
		SourceScanAt: &lateScan,
	})
	tracer.gate.open()
	response := future.wait(t)
	if response.Code != http.StatusOK {
		t.Fatalf("coverage drill-down stable read returned status %d", response.Code)
	}
	if calls := clock.stop(); calls != 2 {
		t.Fatalf("coverage drill-down stable request did not capture server Now once after authentication: calls=%d", calls)
	}
	result := decodeCoverageDrilldown(t, response.Body.Bytes())
	if result.Drilldown.Total != 1 || len(result.Drilldown.Items) != 1 ||
		result.Drilldown.Items[0].Asset.ID != initial.ID ||
		result.Drilldown.Items[0].Asset.Name != "Stable coverage asset before concurrent change" {
		t.Fatal("coverage drill-down mixed a concurrently committed asset/import change into its page or total")
	}
	if tracer.begins.Load() != 1 || !tracer.readOnly.Load() ||
		tracer.commits.Load() != 1 || tracer.rollbacks.Load() != 0 ||
		tracer.selects.Load() < 1 || tracer.writes.Load() != 0 || tracer.locks.Load() != 0 {
		t.Fatalf("coverage drill-down transaction trace was not one read-only repeatable-read snapshot: begins=%d commits=%d rollbacks=%d selects=%d writes=%d locks=%d readOnly=%t",
			tracer.begins.Load(), tracer.commits.Load(), tracer.rollbacks.Load(), tracer.selects.Load(),
			tracer.writes.Load(), tracer.locks.Load(), tracer.readOnly.Load())
	}

	tracer.armed.Store(false)
	next := requestCoverage(t, h, clock, h.admin,
		coverageDrilldownPath+"?state=scanned&freshnessDays=7&limit=100", nil)
	if next.Drilldown.Total != 2 || len(next.Drilldown.Items) != 2 ||
		next.Drilldown.Items[0].Asset.Name != "Concurrent changed coverage asset" ||
		next.Drilldown.Items[1].Asset.ID != late.ID {
		t.Fatal("a later coverage drill-down transaction did not observe committed asset/import changes")
	}
}
