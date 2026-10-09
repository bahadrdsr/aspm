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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	findingMetricPath               = "/api/v1/reports/finding-metrics"
	findingMetricAsOf               = "X-ASPM-Report-As-Of"
	findingMetricVerificationReason = "Finding metric drill-down reflects current canonical finding state; it does not verify safety or historical membership."
)

var (
	findingMetricNow = time.Date(2026, 10, 8, 21, 39, 48, 0, time.UTC)
	findingMetrics   = []string{
		"findings", "open-findings", "accepted-risk", "expired-accepted-risk",
		"suppressed", "expired-suppression", "false-positive", "inferred-resolved",
		"critical", "high", "medium", "low", "info",
	}
)

type findingMetricVerification struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type findingMetricItem struct {
	FindingID             string     `json:"findingId"`
	Title                 string     `json:"title"`
	AssetID               string     `json:"assetId"`
	AssetName             string     `json:"assetName"`
	Severity              string     `json:"severity"`
	OwnerID               *string    `json:"ownerId"`
	OwnerName             *string    `json:"ownerName"`
	WorkflowState         string     `json:"workflowState"`
	Disposition           string     `json:"disposition"`
	AcceptedRiskExpiresAt *time.Time `json:"acceptedRiskExpiresAt"`
	RiskAcceptanceExpired bool       `json:"riskAcceptanceExpired"`
	SourceState           string     `json:"sourceState"`
	SourceFreshnessAt     *time.Time `json:"sourceFreshnessAt"`
}

type findingMetricDrilldown struct {
	WorkspaceID  string                    `json:"workspaceId"`
	Metric       string                    `json:"metric"`
	AsOf         time.Time                 `json:"asOf"`
	Items        []findingMetricItem       `json:"items"`
	Total        int                       `json:"total"`
	NextCursor   *string                   `json:"nextCursor"`
	Verification findingMetricVerification `json:"verification"`
}

type findingMetricEnvelope struct {
	APIVersion string                 `json:"apiVersion"`
	DataOrigin string                 `json:"dataOrigin"`
	Drilldown  findingMetricDrilldown `json:"drilldown"`
}

type findingMetricSeed struct {
	ID                    string
	WorkspaceID           string
	Asset                 asset
	Title                 string
	Severity              string
	Owner                 *actor
	WorkflowState         string
	Disposition           string
	AcceptedRiskExpiresAt *time.Time
	SourceState           string
	SourceFreshnessAt     *time.Time
	SuppressionApprovals  []time.Time
}

type findingMetricClock struct {
	nanos atomic.Int64
	armed atomic.Bool
	calls atomic.Int32
}

func newFindingMetricClock(now time.Time) *findingMetricClock {
	clock := &findingMetricClock{}
	clock.set(now)
	return clock
}

func (c *findingMetricClock) set(now time.Time) {
	c.nanos.Store(now.UTC().UnixNano())
}

func (c *findingMetricClock) now() time.Time {
	if c.armed.Load() {
		c.calls.Add(1)
	}
	return time.Unix(0, c.nanos.Load()).UTC()
}

func (c *findingMetricClock) start() {
	c.calls.Store(0)
	c.armed.Store(true)
}

func (c *findingMetricClock) stop(t *testing.T, label string) {
	t.Helper()
	if calls := c.stopCalls(); calls != 2 {
		t.Fatalf("%s must capture application Now once after authentication: calls=%d", label, calls)
	}
}

func (c *findingMetricClock) stopCalls() int32 {
	c.armed.Store(false)
	return c.calls.Load()
}

func findingMetricJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
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

func decodeFindingMetric(t *testing.T, body []byte) findingMetricEnvelope {
	t.Helper()
	envelope := findingMetricJSONKeys(t, body, "finding metric envelope", "apiVersion", "dataOrigin", "drilldown")
	drilldown := findingMetricJSONKeys(t, envelope["drilldown"], "finding metric drill-down",
		"workspaceId", "metric", "asOf", "items", "total", "nextCursor", "verification")
	findingMetricJSONKeys(t, drilldown["verification"], "finding metric verification", "state", "reason")
	var items []json.RawMessage
	ok(t, "decode finding metric item array", json.Unmarshal(drilldown["items"], &items))
	for index, raw := range items {
		findingMetricJSONKeys(t, raw, fmt.Sprintf("finding metric item %d", index),
			"findingId", "title", "assetId", "assetName", "severity", "ownerId", "ownerName",
			"workflowState", "disposition", "acceptedRiskExpiresAt", "riskAcceptanceExpired",
			"sourceState", "sourceFreshnessAt")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result findingMetricEnvelope
	ok(t, "strictly decode finding metric response", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("finding metric response contains trailing JSON")
	}
	if result.Drilldown.Items == nil {
		t.Fatal("finding metric items must be an array, not null")
	}
	return result
}

func findingMetricTable(h *harness, name string) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_" + name}.Sanitize()
}

func newFindingMetricHarness(t *testing.T, tracer pgx.QueryTracer, storage bool, now time.Time) (*harness, *historicalStorageTripwire, *findingMetricClock) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	clock := newFindingMetricClock(now)
	h.clock.Store(now.UTC().UnixNano())
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
			ok(t, "close finding metric application", h.app.Close())
		}
	})
	h.enroll()
	return h, tripwire, clock
}

func requestFindingMetric(t *testing.T, h *harness, clock *findingMetricClock, who actor,
	path string, asOf *time.Time, body []byte) ([]byte, findingMetricEnvelope) {
	t.Helper()
	headers := []string{}
	if asOf != nil {
		headers = append(headers, findingMetricAsOf, asOf.UTC().Format(time.RFC3339Nano))
	}
	clock.start()
	response := h.request(who, http.MethodGet, path, body, 0, headers...)
	calls := clock.stopCalls()
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200; response withheld", path, response.Code)
	}
	if calls != 2 {
		t.Fatalf("finding metric request must capture application Now once after authentication: calls=%d", calls)
	}
	raw := append([]byte(nil), response.Body.Bytes()...)
	return raw, decodeFindingMetric(t, raw)
}

func denyFindingMetric(t *testing.T, h *harness, who actor, method, path string,
	body any, status int, code string, headers ...string) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw = encode(t, body)
	}
	response := h.request(who, method, path, raw, status, headers...)
	result := h.decode(response)
	if result.Error == nil || result.Error.Code != code {
		t.Fatalf("%s %s denial got %#v, want %s", method, path, result.Error, code)
	}
}

func findingMetricID(value int) string {
	return fmt.Sprintf("%032x", value)
}

func findingMetricTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func findingMetricApprovalID(seedID string, revision int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", seedID, revision)))
	return fmt.Sprintf("%x", sum[:16])
}

func seedFindingMetric(t *testing.T, h *harness, seed findingMetricSeed) findingMetricSeed {
	t.Helper()
	if seed.WorkspaceID == "" {
		seed.WorkspaceID = h.admin.workspace
	}
	if seed.Title == "" {
		seed.Title = "Synthetic current finding metric " + seed.ID
	}
	if seed.WorkflowState == "" {
		seed.WorkflowState = "open"
	}
	if seed.Disposition == "" {
		seed.Disposition = "none"
	}
	if seed.SourceState == "" {
		seed.SourceState = "observed"
	}
	var ownerID any
	if seed.Owner != nil {
		ownerID = seed.Owner.user.ID
	}
	decisionRevision := int64(1)
	switch seed.Disposition {
	case "accepted-risk", "false-positive":
		decisionRevision = 2
	case "suppressed":
		if len(seed.SuppressionApprovals) == 0 {
			t.Fatal("suppressed finding metric seed requires immutable approval expiry")
		}
		decisionRevision = int64(len(seed.SuppressionApprovals) + 1)
	}
	firstObserved := findingMetricNow.Add(-30 * 24 * time.Hour)
	collected := findingMetricNow.Add(-2 * time.Hour)
	imported := findingMetricNow.Add(-time.Hour)
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+findingMetricTable(h, "findings")+`
		(id,workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key,
		 title,description,remediation,severity,evidence_text,source_label,source_scan_at,
		 collected_at,imported_at,first_observed_at,source_freshness_at,source_state,owner_id,
		 workflow_state,disposition,accepted_risk_expires_at,decision_revision)
		VALUES($1,$2,$3,$4,$5,'1','refs/heads/main',$6,$7,
		 'Synthetic finding metric description.','Synthetic finding metric remediation.',$8,
		 'Synthetic finding metric evidence.','synthetic-finding-metric',$9,$10,$11,$12,$9,$13,
		 $14,$15,$16,$17,$18)`,
		seed.ID, seed.WorkspaceID, seed.Asset.ID, "finding-metric-source-"+seed.ID,
		"finding-metric-scope-"+seed.ID, "finding-metric-identity-"+seed.ID, seed.Title,
		seed.Severity, seed.SourceFreshnessAt, collected, imported, firstObserved,
		seed.SourceState, ownerID, seed.WorkflowState, seed.Disposition,
		seed.AcceptedRiskExpiresAt, decisionRevision)
	ok(t, "seed current finding metric row", err)

	insertApproval := func(revision int64, disposition string, expiresAt *time.Time) {
		_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+
			findingMetricTable(h, "finding_disposition_approvals")+`
			(id,workspace_id,finding_id,decision_revision,actor_id,disposition,scope_kind,
			 scope_value,rationale,expires_at,created_at)
			VALUES($1,$2,$3,$4,$5,$6,'finding',$3,$7,$8,$9)`,
			findingMetricApprovalID(seed.ID, revision), seed.WorkspaceID, seed.ID, revision,
			h.admin.user.ID, disposition, "Synthetic immutable finding metric approval.",
			expiresAt, findingMetricNow.Add(-time.Duration(decisionRevision-revision+1)*time.Hour))
		ok(t, "seed finding metric disposition approval", err)
	}
	switch seed.Disposition {
	case "accepted-risk":
		insertApproval(2, seed.Disposition, seed.AcceptedRiskExpiresAt)
	case "false-positive":
		insertApproval(2, seed.Disposition, nil)
	case "suppressed":
		for index := range seed.SuppressionApprovals {
			expires := seed.SuppressionApprovals[index].UTC()
			insertApproval(int64(index+2), seed.Disposition, &expires)
		}
	}
	return seed
}

func expectedFindingMetricItem(seed findingMetricSeed, asOf time.Time) findingMetricItem {
	var ownerID, ownerName *string
	if seed.Owner != nil {
		id, name := seed.Owner.user.ID, seed.Owner.user.Name
		ownerID, ownerName = &id, &name
	}
	expired := seed.Disposition == "accepted-risk" && seed.AcceptedRiskExpiresAt != nil &&
		!seed.AcceptedRiskExpiresAt.After(asOf)
	return findingMetricItem{
		FindingID: seed.ID, Title: seed.Title, AssetID: seed.Asset.ID, AssetName: seed.Asset.Name,
		Severity: seed.Severity, OwnerID: ownerID, OwnerName: ownerName,
		WorkflowState: seed.WorkflowState, Disposition: seed.Disposition,
		AcceptedRiskExpiresAt: seed.AcceptedRiskExpiresAt, RiskAcceptanceExpired: expired,
		SourceState: seed.SourceState, SourceFreshnessAt: seed.SourceFreshnessAt,
	}
}

func findingMetricSeedMember(seed findingMetricSeed, metric string, asOf time.Time) bool {
	switch metric {
	case "findings":
		return true
	case "open-findings":
		return seed.WorkflowState != "resolved"
	case "accepted-risk":
		return seed.Disposition == "accepted-risk"
	case "expired-accepted-risk":
		return seed.Disposition == "accepted-risk" && seed.AcceptedRiskExpiresAt != nil &&
			!seed.AcceptedRiskExpiresAt.After(asOf)
	case "suppressed":
		return seed.Disposition == "suppressed"
	case "expired-suppression":
		return seed.Disposition == "suppressed" && len(seed.SuppressionApprovals) > 0 &&
			!seed.SuppressionApprovals[len(seed.SuppressionApprovals)-1].After(asOf)
	case "false-positive":
		return seed.Disposition == "false-positive"
	case "inferred-resolved":
		return seed.SourceState == "inferred-resolved"
	case "critical", "high", "medium", "low", "info":
		return seed.Severity == metric
	default:
		return false
	}
}

func expectedFindingMetricPage(seeds []findingMetricSeed, hidden map[string]bool, metric string,
	asOf time.Time, limit int, cursor string) ([]findingMetricItem, int, *string) {
	ordered := append([]findingMetricSeed(nil), seeds...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	all := make([]findingMetricItem, 0, len(ordered))
	for _, seed := range ordered {
		if hidden[seed.ID] || !findingMetricSeedMember(seed, metric, asOf) {
			continue
		}
		all = append(all, expectedFindingMetricItem(seed, asOf))
	}
	total := len(all)
	remaining := make([]findingMetricItem, 0, len(all))
	for _, item := range all {
		if item.FindingID > cursor {
			remaining = append(remaining, item)
		}
	}
	if len(remaining) <= limit {
		return remaining, total, nil
	}
	page := remaining[:limit]
	next := page[len(page)-1].FindingID
	return page, total, &next
}

func findingMetricOverviewTotal(report postureReport, metric string) int {
	switch metric {
	case "findings":
		return report.Totals["findings"]
	case "open-findings":
		return report.Totals["openFindings"]
	case "accepted-risk":
		return report.Totals["acceptedRisk"]
	case "expired-accepted-risk":
		return report.Totals["expiredAcceptedRisk"]
	case "suppressed":
		return report.Totals["suppressed"]
	case "expired-suppression":
		return report.Totals["expiredSuppression"]
	case "false-positive":
		return report.Totals["falsePositive"]
	case "inferred-resolved":
		return report.Totals["inferredResolved"]
	case "critical", "high", "medium", "low", "info":
		return report.BySeverity[metric]
	default:
		return -1
	}
}

func findingMetricValidID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func findingMetricItemMember(item findingMetricItem, metric string, asOf time.Time) bool {
	switch metric {
	case "findings":
		return true
	case "open-findings":
		return item.WorkflowState != "resolved"
	case "accepted-risk":
		return item.Disposition == "accepted-risk"
	case "expired-accepted-risk":
		return item.Disposition == "accepted-risk" && item.AcceptedRiskExpiresAt != nil &&
			!item.AcceptedRiskExpiresAt.After(asOf)
	case "suppressed", "expired-suppression":
		return item.Disposition == "suppressed"
	case "false-positive":
		return item.Disposition == "false-positive"
	case "inferred-resolved":
		return item.SourceState == "inferred-resolved"
	case "critical", "high", "medium", "low", "info":
		return item.Severity == metric
	default:
		return false
	}
}

func assertFindingMetricPage(t *testing.T, got findingMetricEnvelope, workspace, metric string,
	asOf time.Time, want []findingMetricItem, total int, cursor *string, requestedCursor string, limit int) {
	t.Helper()
	if got.APIVersion != apiVersion || got.DataOrigin != "live" ||
		got.Drilldown.WorkspaceID != workspace || got.Drilldown.Metric != metric ||
		!got.Drilldown.AsOf.Equal(asOf) {
		t.Fatalf("finding metric envelope authority or as-of mismatch: %#v", got)
	}
	if got.Drilldown.Verification != (findingMetricVerification{
		State: "not-run", Reason: findingMetricVerificationReason,
	}) {
		t.Fatal("finding metric changed the fixed not-run verification statement")
	}
	if got.Drilldown.Total != total || !reflect.DeepEqual(got.Drilldown.NextCursor, cursor) ||
		!reflect.DeepEqual(got.Drilldown.Items, want) {
		t.Fatalf("finding metric page got %#v, want items=%#v total=%d cursor=%v",
			got.Drilldown, want, total, cursor)
	}
	severities := map[string]bool{"critical": true, "high": true, "medium": true, "low": true, "info": true}
	workflows := map[string]bool{"open": true, "in-progress": true, "pending-retest": true, "resolved": true}
	dispositions := map[string]bool{"none": true, "accepted-risk": true, "suppressed": true, "false-positive": true}
	sourceStates := map[string]bool{"observed": true, "inferred-resolved": true, "stale": true, "unknown": true}
	seen := make(map[string]bool, len(got.Drilldown.Items))
	last := requestedCursor
	for _, item := range got.Drilldown.Items {
		expired := item.Disposition == "accepted-risk" && item.AcceptedRiskExpiresAt != nil &&
			!item.AcceptedRiskExpiresAt.After(asOf)
		if !findingMetricValidID(item.FindingID) || !findingMetricValidID(item.AssetID) ||
			item.FindingID <= last || seen[item.FindingID] ||
			item.Title == "" || item.AssetName == "" ||
			!severities[item.Severity] || !workflows[item.WorkflowState] ||
			!dispositions[item.Disposition] || !sourceStates[item.SourceState] ||
			(item.OwnerID == nil) != (item.OwnerName == nil) ||
			item.OwnerID != nil && !findingMetricValidID(*item.OwnerID) ||
			item.Disposition != "accepted-risk" && item.AcceptedRiskExpiresAt != nil ||
			item.RiskAcceptanceExpired != expired ||
			!findingMetricItemMember(item, metric, asOf) {
			t.Fatalf("finding metric item authority, enum, order, membership, or expiry arithmetic is invalid: %#v", item)
		}
		seen[item.FindingID], last = true, item.FindingID
	}
	if len(got.Drilldown.Items) > limit || total < len(got.Drilldown.Items) {
		t.Fatal("finding metric page exceeded its limit or whole-filter total")
	}
	if cursor != nil && (len(got.Drilldown.Items) != limit ||
		*cursor != got.Drilldown.Items[len(got.Drilldown.Items)-1].FindingID ||
		*cursor <= requestedCursor) {
		t.Fatal("finding metric nextCursor is not its exclusive last returned finding ID")
	}
	if len(got.Drilldown.Items) < limit && got.Drilldown.NextCursor != nil {
		t.Fatal("finding metric returned a cursor after a short final page")
	}
	if requestedCursor == "" && (total > len(got.Drilldown.Items)) != (got.Drilldown.NextCursor != nil) {
		t.Fatal("finding metric first-page total and cursor relationship is invalid")
	}
}

func assertFindingMetricOverview(t *testing.T, report postureReport, seeds []findingMetricSeed,
	hidden map[string]bool, asOf time.Time) {
	t.Helper()
	for _, metric := range findingMetrics {
		_, want, _ := expectedFindingMetricPage(seeds, hidden, metric, asOf, 100, "")
		if got := findingMetricOverviewTotal(report, metric); got != want {
			t.Fatalf("Live overview %s got %d, want exact visible-canonical total %d", metric, got, want)
		}
	}
}

func seedExactFindingMetricMembership(t *testing.T, h *harness, analyst actor,
	foreignAdmin actor, foreignOwner actor) ([]findingMetricSeed, findingMetricSeed, string, string) {
	t.Helper()
	assetOne := h.asset(h.admin, "Current finding metric repository one", &h.admin.user.ID)
	assetTwo := h.asset(h.admin, "Current finding metric repository two", &analyst.user.ID)
	correlationAsset := h.asset(h.admin, "Current finding metric correlated repository", nil)
	fresh := findingMetricTime(findingMetricNow.Add(-time.Hour))
	expired := findingMetricTime(findingMetricNow.Add(-time.Hour))
	boundary := findingMetricTime(findingMetricNow)
	future := findingMetricTime(findingMetricNow.Add(time.Hour))
	seeds := []findingMetricSeed{
		{ID: findingMetricID(1), Asset: assetOne, Severity: "critical", Owner: &h.admin,
			WorkflowState: "open", Disposition: "accepted-risk", AcceptedRiskExpiresAt: boundary,
			SourceState: "inferred-resolved", SourceFreshnessAt: fresh},
		{ID: findingMetricID(2), Asset: assetOne, Severity: "high",
			WorkflowState: "resolved", SourceState: "observed", SourceFreshnessAt: fresh},
		{ID: findingMetricID(3), Asset: assetOne, Severity: "medium", Owner: &analyst,
			WorkflowState: "in-progress", Disposition: "accepted-risk", AcceptedRiskExpiresAt: future,
			SourceState: "stale", SourceFreshnessAt: fresh},
		{ID: findingMetricID(4), Asset: assetOne, Severity: "low",
			WorkflowState: "pending-retest", Disposition: "suppressed", SourceState: "observed",
			SourceFreshnessAt: fresh, SuppressionApprovals: []time.Time{findingMetricNow}},
		{ID: findingMetricID(5), Asset: assetTwo, Severity: "info",
			WorkflowState: "open", Disposition: "suppressed", SourceState: "unknown",
			SuppressionApprovals: []time.Time{findingMetricNow.Add(-24 * time.Hour), findingMetricNow.Add(24 * time.Hour)}},
		{ID: findingMetricID(6), Asset: assetTwo, Severity: "critical", Owner: &analyst,
			WorkflowState: "open", Disposition: "false-positive", SourceState: "observed",
			SourceFreshnessAt: fresh},
		{ID: findingMetricID(7), Asset: assetTwo, Severity: "high",
			WorkflowState: "open", SourceState: "inferred-resolved", SourceFreshnessAt: fresh},
		{ID: findingMetricID(8), Asset: assetTwo, Severity: "medium",
			WorkflowState: "resolved", Disposition: "accepted-risk", AcceptedRiskExpiresAt: expired,
			SourceState: "observed", SourceFreshnessAt: fresh},
		{ID: findingMetricID(9), Asset: assetTwo, Severity: "low",
			WorkflowState: "resolved", Disposition: "suppressed", SourceState: "observed",
			SourceFreshnessAt: fresh, SuppressionApprovals: []time.Time{findingMetricNow.Add(time.Hour), findingMetricNow.Add(-time.Hour)}},
		{ID: findingMetricID(10), Asset: correlationAsset, Severity: "info",
			WorkflowState: "open", SourceState: "observed", SourceFreshnessAt: fresh},
		{ID: findingMetricID(11), Asset: correlationAsset, Severity: "critical",
			WorkflowState: "open", Disposition: "false-positive", SourceState: "inferred-resolved",
			SourceFreshnessAt: fresh},
	}
	for index := range seeds {
		seeds[index].Title = fmt.Sprintf("Synthetic current finding metric %02d", index+1)
		seeds[index] = seedFindingMetric(t, h, seeds[index])
	}
	primaryID, secondaryID := seeds[9].ID, seeds[10].ID
	v25Merge(t, h, primaryID, secondaryID)
	seeds[9].Owner = &h.admin

	foreignAsset := h.asset(foreignAdmin, "Foreign current finding metric repository", &foreignOwner.user.ID)
	foreignSeed := seedFindingMetric(t, h, findingMetricSeed{
		ID: findingMetricID(900), WorkspaceID: foreignAdmin.workspace, Asset: foreignAsset,
		Title: "Foreign selected-workspace finding metric", Severity: "critical", Owner: &foreignOwner,
		WorkflowState: "open", SourceState: "observed",
		SourceFreshnessAt: findingMetricTime(findingMetricNow.Add(-time.Hour)),
	})
	return seeds, foreignSeed, primaryID, secondaryID
}

func splitFindingMetricCorrelation(t *testing.T, h *harness, primaryID, secondaryID string) {
	t.Helper()
	preview := h.json(h.admin, http.MethodPost, "/api/v1/findings/"+primaryID+"/split-previews",
		object{"memberFindingId": secondaryID}, http.StatusOK).SplitPreview
	h.json(h.admin, http.MethodPost, "/api/v1/findings/"+primaryID+"/splits", object{
		"memberFindingId":         secondaryID,
		"correlationRevision":     preview.Correlation.Revision,
		"primaryDecisionRevision": preview.Primary.DecisionRevision,
		"primaryEvidenceRevision": preview.Primary.EvidenceRevision,
		"memberDecisionRevision":  preview.Member.DecisionRevision,
		"memberEvidenceRevision":  preview.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(&h.admin.user.ID, "open", "none", nil),
		"memberDecision":          correlationDecision(nil, "open", "false-positive", nil),
		"rationale":               "Release the hidden canonical finding metric member after reviewed separation.",
		"idempotencyKey":          "finding-metric-visibility-split",
	}, http.StatusCreated)
}

func TestM10_FindingMetricOverviewAndNewSnapshotsUseVisibleCanonicalTotals(t *testing.T) {
	h, storage, _ := newFindingMetricHarness(t, nil, true, findingMetricNow)
	analyst := h.addUser(h.admin, "analyst")
	foreignAdmin := h.addWorkspace()
	foreign := h.addUser(foreignAdmin, "viewer")
	seeds, _, _, secondaryID := seedExactFindingMetricMembership(t, h, analyst, foreignAdmin, foreign)
	hidden := map[string]bool{secondaryID: true}

	completedAt := findingMetricNow.Add(-48 * time.Hour)
	oldReport := historicalReport(h.admin.workspace, completedAt, 0)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: findingMetricID(950), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Immutable pre-correction finding total", State: "succeeded",
		CompletedAt: &completedAt, Report: &oldReport,
	})
	oldDetail := append([]byte(nil), h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+findingMetricID(950), nil, http.StatusOK).Body.Bytes()...)
	var oldJSON string
	ok(t, "read existing saved report JSON", h.services.db.QueryRow(h.services.ctx,
		`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, findingMetricID(950)).Scan(&oldJSON))
	wantVersions := make([]int, 27)
	for index := range wantVersions {
		wantVersions[index] = index + 1
	}
	equal(t, "finding metric schema remains exactly V27", historicalSchemaVersions(t, h), wantVersions)
	storage.arm()

	current := overview(t, h, h.admin)
	assertFindingMetricOverview(t, current, seeds, hidden, current.AsOf)
	if current.Totals["findings"] != 10 || current.BySeverity["critical"] != 2 ||
		current.Totals["falsePositive"] != 1 || current.Totals["inferredResolved"] != 2 {
		t.Fatal("Live overview counted the hidden active-correlation secondary as a separate finding")
	}

	response := h.request(h.admin, http.MethodPost, "/api/v1/reports/snapshots",
		encode(t, object{"name": "Corrected visible canonical finding totals", "freshnessDays": 7}),
		http.StatusAccepted)
	var created struct {
		APIVersion string         `json:"apiVersion"`
		Snapshot   reportSnapshot `json:"snapshot"`
	}
	ok(t, "decode queued corrected snapshot", json.Unmarshal(response.Body.Bytes(), &created))
	if created.Snapshot.ID == "" || created.Snapshot.State != "queued" || created.Snapshot.Report != nil {
		t.Fatal("corrected snapshot request did not remain ordinary queued report work")
	}
	ok(t, "process corrected visible-canonical snapshot", h.app.ProcessReports(h.services.ctx))
	detail := h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+created.Snapshot.ID, nil, http.StatusOK)
	ok(t, "decode corrected saved snapshot", json.Unmarshal(detail.Body.Bytes(), &created))
	if created.Snapshot.State != "succeeded" || created.Snapshot.Report == nil {
		t.Fatal("report worker did not persist the corrected saved snapshot")
	}
	assertFindingMetricOverview(t, *created.Snapshot.Report, seeds, hidden, created.Snapshot.Report.AsOf)

	if !bytes.Equal(oldDetail, h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+findingMetricID(950), nil, http.StatusOK).Body.Bytes()) {
		t.Fatal("correcting new overview and snapshot totals rewrote an existing saved snapshot response")
	}
	var afterJSON string
	ok(t, "reread existing saved report JSON", h.services.db.QueryRow(h.services.ctx,
		`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, findingMetricID(950)).Scan(&afterJSON))
	equal(t, "existing saved report JSON remains immutable", afterJSON, oldJSON)
	if storage.calls.Load() != 0 {
		t.Fatal("overview or database-only snapshot reporting performed object-store I/O")
	}
}

func TestM10_FindingMetricDrilldownMatchesCorrectedOverviewAndCorrelationVisibility(t *testing.T) {
	h, storage, clock := newFindingMetricHarness(t, nil, true, findingMetricNow)
	analyst := h.addUser(h.admin, "analyst")
	viewer := h.addUser(h.admin, "viewer")
	foreignAdmin := h.addWorkspace()
	foreignViewer := h.addUser(foreignAdmin, "viewer")
	seeds, foreignSeed, primaryID, secondaryID := seedExactFindingMetricMembership(
		t, h, analyst, foreignAdmin, foreignViewer)
	hidden := map[string]bool{secondaryID: true}

	completedAt := findingMetricNow.Add(-24 * time.Hour)
	oldReport := historicalReport(h.admin.workspace, completedAt, 0)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: findingMetricID(951), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Finding metric immutable snapshot control", State: "succeeded",
		CompletedAt: &completedAt, Report: &oldReport,
	})
	oldDetail := append([]byte(nil), h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+findingMetricID(951), nil, http.StatusOK).Body.Bytes()...)
	current := overview(t, h, h.admin)
	assertFindingMetricOverview(t, current, seeds, hidden, current.AsOf)
	stateBefore := historicalState(t, h)
	sideEffectsBefore := slaSideEffectCounts(t, h)
	storage.arm()

	results := make(map[string]findingMetricEnvelope, len(findingMetrics))
	for index, metric := range findingMetrics {
		who := h.admin
		if index%2 == 1 {
			who = viewer
		}
		body := encode(t, object{"workspaceId": foreignAdmin.workspace})
		_, result := requestFindingMetric(t, h, clock, who,
			findingMetricPath+"?metric="+metric+"&limit=100", &current.AsOf, body)
		want, total, cursor := expectedFindingMetricPage(seeds, hidden, metric, current.AsOf, 100, "")
		assertFindingMetricPage(t, result, h.admin.workspace, metric, current.AsOf,
			want, total, cursor, "", 100)
		if result.Drilldown.Total != findingMetricOverviewTotal(current, metric) {
			t.Fatalf("%s drill-down total diverged from corrected Live overview", metric)
		}
		results[metric] = result
	}
	if len(results["expired-accepted-risk"].Drilldown.Items) != 2 ||
		len(results["expired-suppression"].Drilldown.Items) != 2 {
		t.Fatal("finding metric expiry boundaries or latest immutable suppression approval were not applied")
	}
	overlap := results["expired-accepted-risk"].Drilldown.Items[0]
	if overlap.FindingID != seeds[0].ID || overlap.SourceState != "inferred-resolved" ||
		overlap.Severity != "critical" || overlap.WorkflowState == "resolved" {
		t.Fatal("finding metric drill-down lost overlapping memberships")
	}
	for _, item := range results["open-findings"].Drilldown.Items {
		if item.FindingID == seeds[1].ID {
			t.Fatal("resolved severity member was incorrectly exposed as open")
		}
	}
	if !reflect.DeepEqual(results["high"].Drilldown.Items,
		[]findingMetricItem{expectedFindingMetricItem(seeds[1], current.AsOf), expectedFindingMetricItem(seeds[6], current.AsOf)}) {
		t.Fatal("severity membership did not remain independent of resolved workflow")
	}

	adminBytes, _ := requestFindingMetric(t, h, clock, h.admin,
		findingMetricPath+"?metric=findings&limit=100", &current.AsOf, nil)
	viewerBytes, _ := requestFindingMetric(t, h, clock, viewer,
		findingMetricPath+"?metric=findings&limit=100", &current.AsOf, nil)
	if !bytes.Equal(adminBytes, viewerBytes) {
		t.Fatal("admin and viewer finding metric bytes differ for the same selected workspace")
	}

	foreignOverview := overview(t, h, foreignViewer)
	_, foreignResult := requestFindingMetric(t, h, clock, foreignViewer,
		findingMetricPath+"?metric=findings&limit=100", &foreignOverview.AsOf,
		encode(t, object{"workspaceId": h.admin.workspace}))
	foreignWant, foreignTotal, foreignCursor := expectedFindingMetricPage(
		[]findingMetricSeed{foreignSeed}, map[string]bool{}, "findings", foreignOverview.AsOf, 100, "")
	assertFindingMetricPage(t, foreignResult, foreignAdmin.workspace, "findings", foreignOverview.AsOf,
		foreignWant, foreignTotal, foreignCursor, "", 100)

	if after := historicalState(t, h); !reflect.DeepEqual(after, stateBefore) ||
		!reflect.DeepEqual(slaSideEffectCounts(t, h), sideEffectsBefore) {
		t.Fatal("finding metric reads mutated findings, approvals, snapshots, provider, retention, archive, or schema state")
	}
	if !bytes.Equal(oldDetail, h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+findingMetricID(951), nil, http.StatusOK).Body.Bytes()) {
		t.Fatal("finding metric reads changed existing snapshot bytes")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("finding metric reads performed raw or archive object-store I/O")
	}

	splitFindingMetricCorrelation(t, h, primaryID, secondaryID)
	afterSplit := overview(t, h, h.admin)
	assertFindingMetricOverview(t, afterSplit, seeds, map[string]bool{}, afterSplit.AsOf)
	if afterSplit.Totals["findings"] != current.Totals["findings"]+1 ||
		afterSplit.Totals["openFindings"] != current.Totals["openFindings"]+1 ||
		afterSplit.Totals["falsePositive"] != current.Totals["falsePositive"]+1 ||
		afterSplit.Totals["inferredResolved"] != current.Totals["inferredResolved"]+1 ||
		afterSplit.BySeverity["critical"] != current.BySeverity["critical"]+1 {
		t.Fatal("split did not release the former hidden secondary into every current applicable metric")
	}
	stateAfterSplit := historicalState(t, h)
	sideEffectsAfterSplit := slaSideEffectCounts(t, h)
	for _, metric := range []string{"findings", "open-findings", "false-positive", "inferred-resolved", "critical"} {
		_, result := requestFindingMetric(t, h, clock, viewer,
			findingMetricPath+"?metric="+metric+"&limit=100", &afterSplit.AsOf, nil)
		want, total, cursor := expectedFindingMetricPage(seeds, map[string]bool{}, metric, afterSplit.AsOf, 100, "")
		assertFindingMetricPage(t, result, h.admin.workspace, metric, afterSplit.AsOf,
			want, total, cursor, "", 100)
	}
	if after := historicalState(t, h); !reflect.DeepEqual(after, stateAfterSplit) ||
		!reflect.DeepEqual(slaSideEffectCounts(t, h), sideEffectsAfterSplit) {
		t.Fatal("post-split finding metric reads performed a durable side effect")
	}
	if !bytes.Equal(oldDetail, h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+findingMetricID(951), nil, http.StatusOK).Body.Bytes()) {
		t.Fatal("correlation release or later metric reads rewrote existing snapshot bytes")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("correlation split or finding metric reads performed object-store I/O")
	}
}

func TestM10_FindingMetricDrilldownBindsIssuedAsOfAndValidatesQueriesAuthorityAndPaging(t *testing.T) {
	t.Run("issued as-of binding and expiry boundary", func(t *testing.T) {
		issued := findingMetricNow.Add(-10 * time.Minute)
		h, _, clock := newFindingMetricHarness(t, nil, false, issued)
		asset := h.asset(h.admin, "Finding metric issued as-of repository", nil)
		expiresBetween := issued.Add(5 * time.Minute)
		seeds := []findingMetricSeed{
			seedFindingMetric(t, h, findingMetricSeed{
				ID: findingMetricID(1), Asset: asset, Severity: "critical",
				WorkflowState: "open", Disposition: "accepted-risk",
				AcceptedRiskExpiresAt: &expiresBetween, SourceState: "observed",
			}),
			seedFindingMetric(t, h, findingMetricSeed{
				ID: findingMetricID(2), Asset: asset, Severity: "high",
				WorkflowState: "open", Disposition: "suppressed", SourceState: "observed",
				SuppressionApprovals: []time.Time{expiresBetween},
			}),
		}
		issuedOverview := overview(t, h, h.admin)
		if issuedOverview.Totals["expiredAcceptedRisk"] != 0 ||
			issuedOverview.Totals["expiredSuppression"] != 0 {
			t.Fatal("issued Live overview did not precede the controlled disposition expiries")
		}
		clock.set(findingMetricNow)
		h.clock.Store(findingMetricNow.UnixNano())
		for _, metric := range []string{"expired-accepted-risk", "expired-suppression"} {
			_, bound := requestFindingMetric(t, h, clock, h.admin,
				findingMetricPath+"?metric="+metric+"&limit=100", &issuedOverview.AsOf, nil)
			want, total, cursor := expectedFindingMetricPage(
				seeds, map[string]bool{}, metric, issuedOverview.AsOf, 100, "")
			assertFindingMetricPage(t, bound, h.admin.workspace, metric, issuedOverview.AsOf,
				want, total, cursor, "", 100)
			if total != 0 {
				t.Fatal("issued as-of fixture did not distinguish later current expiry")
			}
			_, current := requestFindingMetric(t, h, clock, h.admin,
				findingMetricPath+"?metric="+metric+"&limit=100", nil, nil)
			want, total, cursor = expectedFindingMetricPage(
				seeds, map[string]bool{}, metric, findingMetricNow, 100, "")
			assertFindingMetricPage(t, current, h.admin.workspace, metric, findingMetricNow,
				want, total, cursor, "", 100)
			if total != 1 {
				t.Fatal("unbound current finding metric did not observe the later expired disposition")
			}
		}
		path := findingMetricPath + "?metric=findings&limit=100"
		for _, value := range []string{"", "not-a-time", findingMetricNow.Add(time.Nanosecond).Format(time.RFC3339Nano)} {
			denyFindingMetric(t, h, h.admin, http.MethodGet, path, nil,
				http.StatusBadRequest, "invalid-input", findingMetricAsOf, value)
		}
	})

	t.Run("native paging and exact query", func(t *testing.T) {
		h, _, clock := newFindingMetricHarness(t, nil, false, findingMetricNow)
		asset := h.asset(h.admin, "Finding metric native paging repository", nil)
		seeds := make([]findingMetricSeed, 0, 101)
		for index := 1; index <= 101; index++ {
			seeds = append(seeds, seedFindingMetric(t, h, findingMetricSeed{
				ID: findingMetricID(index), Asset: asset,
				Title:    fmt.Sprintf("Synthetic paged finding metric %03d", index),
				Severity: "low", WorkflowState: "open", SourceState: "observed",
			}))
		}
		report := overview(t, h, h.admin)
		_, first := requestFindingMetric(t, h, clock, h.admin,
			findingMetricPath+"?metric=findings", &report.AsOf, nil)
		want, total, cursor := expectedFindingMetricPage(seeds, map[string]bool{},
			"findings", report.AsOf, 100, "")
		assertFindingMetricPage(t, first, h.admin.workspace, "findings", report.AsOf,
			want, total, cursor, "", 100)
		if cursor == nil || *cursor != findingMetricID(100) {
			t.Fatal("finding metric default limit did not return the exact native cursor")
		}
		_, second := requestFindingMetric(t, h, clock, h.admin,
			findingMetricPath+"?metric=findings&cursor="+*cursor, &report.AsOf, nil)
		secondWant, secondTotal, secondCursor := expectedFindingMetricPage(seeds, map[string]bool{},
			"findings", report.AsOf, 100, *cursor)
		assertFindingMetricPage(t, second, h.admin.workspace, "findings", report.AsOf,
			secondWant, secondTotal, secondCursor, *cursor, 100)
		_, one := requestFindingMetric(t, h, clock, h.admin,
			findingMetricPath+"?metric=findings&limit=1", &report.AsOf, nil)
		oneWant, oneTotal, oneCursor := expectedFindingMetricPage(seeds, map[string]bool{},
			"findings", report.AsOf, 1, "")
		assertFindingMetricPage(t, one, h.admin.workspace, "findings", report.AsOf,
			oneWant, oneTotal, oneCursor, "", 1)
		pastCursor := strings.Repeat("f", 32)
		_, past := requestFindingMetric(t, h, clock, h.admin,
			findingMetricPath+"?metric=findings&cursor="+pastCursor, &report.AsOf, nil)
		pastWant, pastTotal, pastNext := expectedFindingMetricPage(seeds, map[string]bool{},
			"findings", report.AsOf, 100, pastCursor)
		assertFindingMetricPage(t, past, h.admin.workspace, "findings", report.AsOf,
			pastWant, pastTotal, pastNext, pastCursor, 100)
		_, empty := requestFindingMetric(t, h, clock, h.admin,
			findingMetricPath+"?metric=critical&limit=100", &report.AsOf, nil)
		assertFindingMetricPage(t, empty, h.admin.workspace, "critical", report.AsOf,
			[]findingMetricItem{}, 0, nil, "", 100)

		for _, path := range []string{
			findingMetricPath,
			findingMetricPath + "?metric=",
			findingMetricPath + "?metric=Findings",
			findingMetricPath + "?metric=verified-resolved",
			findingMetricPath + "?metric=unknown",
			findingMetricPath + "?metric=findings&metric=open-findings",
			findingMetricPath + "?metric=findings&limit=",
			findingMetricPath + "?metric=findings&limit=0",
			findingMetricPath + "?metric=findings&limit=101",
			findingMetricPath + "?metric=findings&limit=-1",
			findingMetricPath + "?metric=findings&limit=1.0",
			findingMetricPath + "?metric=findings&limit=1e2",
			findingMetricPath + "?metric=findings&limit=%2B1",
			findingMetricPath + "?metric=findings&limit=1&limit=2",
			findingMetricPath + "?metric=findings&cursor=",
			findingMetricPath + "?metric=findings&cursor=abc",
			findingMetricPath + "?metric=findings&cursor=" + strings.Repeat("A", 32),
			findingMetricPath + "?metric=findings&cursor=" + findingMetricID(1) + "&cursor=" + findingMetricID(2),
			findingMetricPath + "?metric=findings&workspaceId=" + h.admin.workspace,
			findingMetricPath + "?metric=findings&asOf=" + report.AsOf.Format(time.RFC3339Nano),
			findingMetricPath + "?Metric=findings",
			findingMetricPath + "?metric=findings&page=1",
		} {
			denyFindingMetric(t, h, h.admin, http.MethodGet, path, nil,
				http.StatusBadRequest, "invalid-input")
		}
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			denyFindingMetric(t, h, h.admin, method,
				findingMetricPath+"?metric=findings", object{"workspaceId": h.admin.workspace},
				http.StatusMethodNotAllowed, "method-not-allowed")
		}
	})

	t.Run("authentication and selected membership", func(t *testing.T) {
		h, _, _ := newFindingMetricHarness(t, nil, false, findingMetricNow)
		path := findingMetricPath + "?metric=findings&limit=100"
		denyFindingMetric(t, h, actor{}, http.MethodGet, path, nil,
			http.StatusUnauthorized, "unauthorized")

		expired := h.addUser(h.admin, "viewer")
		expiredHash := sha256.Sum256([]byte(expired.cookie.Value))
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+findingMetricTable(h, "sessions")+`
			SET expires_at=$2 WHERE token_hash=$1`, expiredHash[:], findingMetricNow)
		ok(t, "expire finding metric session", err)
		denyFindingMetric(t, h, expired, http.MethodGet, path, nil,
			http.StatusUnauthorized, "unauthorized")

		revoked := h.addUser(h.admin, "viewer")
		revokedHash := sha256.Sum256([]byte(revoked.cookie.Value))
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+findingMetricTable(h, "sessions")+`
			SET revoked_at=$2 WHERE token_hash=$1`, revokedHash[:], findingMetricNow)
		ok(t, "revoke finding metric session", err)
		denyFindingMetric(t, h, revoked, http.MethodGet, path, nil,
			http.StatusUnauthorized, "unauthorized")

		unavailable := h.addUser(h.admin, "viewer")
		_, err = h.services.db.Exec(h.services.ctx, `DELETE FROM `+findingMetricTable(h, "memberships")+`
			WHERE workspace_id=$1 AND user_id=$2`, unavailable.workspace, unavailable.user.ID)
		ok(t, "remove finding metric membership", err)
		denyFindingMetric(t, h, unavailable, http.MethodGet, path, nil,
			http.StatusForbidden, "forbidden")

		foreign := h.addUser(h.addWorkspace(), "viewer")
		forged := foreign
		forged.workspace = h.admin.workspace
		denyFindingMetric(t, h, forged, http.MethodGet, path,
			object{"workspaceId": foreign.workspace}, http.StatusForbidden, "forbidden")
	})
}

type findingMetricTraceMarker struct{}

type findingMetricTracer struct {
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

func newFindingMetricTracer() *findingMetricTracer {
	return &findingMetricTracer{gate: newSecurityGate()}
}

func (t *findingMetricTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.armed.Load() {
		return ctx
	}
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != "finding-metric-read" {
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
	if strings.Contains(sql, "app_findings") {
		t.selects.Add(1)
		return context.WithValue(ctx, findingMetricTraceMarker{}, true)
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

func (t *findingMetricTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(findingMetricTraceMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func TestM10_FindingMetricDrilldownUsesOneStableReadOnlyRepeatableReadSnapshot(t *testing.T) {
	tracer := newFindingMetricTracer()
	h, _, clock := newFindingMetricHarness(t, tracer, false, findingMetricNow)
	asset := h.asset(h.admin, "Stable finding metric repository", nil)
	first := seedFindingMetric(t, h, findingMetricSeed{
		ID: findingMetricID(1), Asset: asset, Severity: "critical",
		WorkflowState: "open", Disposition: "suppressed", SourceState: "observed",
		SuppressionApprovals: []time.Time{findingMetricNow.Add(-time.Hour)},
	})
	second := seedFindingMetric(t, h, findingMetricSeed{
		ID: findingMetricID(2), Asset: asset, Severity: "high",
		WorkflowState: "open", Disposition: "suppressed", SourceState: "observed",
		SuppressionApprovals: []time.Time{findingMetricNow.Add(-time.Minute)},
	})
	seeds := []findingMetricSeed{first, second}

	clock.start()
	tracer.armed.Store(true)
	future := securityRequest(h, h.admin, http.MethodGet,
		findingMetricPath+"?metric=expired-suppression&limit=1",
		nil, 15*time.Second, "finding-metric-read")
	select {
	case <-tracer.gate.entered:
	case <-future.done:
		t.Fatalf("finding metric endpoint returned status %d before a real finding query", future.result.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("finding metric endpoint did not reach its bounded finding query")
	}

	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin concurrent finding metric changes", err)
	_, err = tx.Exec(h.services.ctx, `UPDATE `+findingMetricTable(h, "findings")+`
		SET disposition='none',decision_revision=decision_revision+1
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, first.ID)
	ok(t, "commit concurrent finding metric removal", err)
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+findingMetricTable(h, "finding_disposition_approvals")+`
		(id,workspace_id,finding_id,decision_revision,actor_id,disposition,scope_kind,
		 scope_value,rationale,expires_at,created_at)
		VALUES($1,$2,$3,3,$4,'suppressed','finding',$3,$5,$6,$7)`,
		findingMetricApprovalID(second.ID, 3), h.admin.workspace, second.ID, h.admin.user.ID,
		"Concurrent latest suppression approval.", findingMetricNow.Add(time.Hour), findingMetricNow)
	ok(t, "append concurrent latest suppression approval", err)
	_, err = tx.Exec(h.services.ctx, `UPDATE `+findingMetricTable(h, "findings")+`
		SET decision_revision=3 WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, second.ID)
	ok(t, "activate concurrent latest suppression approval", err)
	third := findingMetricSeed{
		ID: findingMetricID(3), Asset: asset, Title: "Concurrent added expired suppression",
		Severity: "medium", WorkflowState: "open", Disposition: "suppressed",
		SourceState: "observed", SuppressionApprovals: []time.Time{findingMetricNow},
	}
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+findingMetricTable(h, "findings")+`
		(id,workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key,
		 title,description,remediation,severity,evidence_text,source_label,collected_at,
		 imported_at,first_observed_at,source_state,workflow_state,disposition,decision_revision)
		VALUES($1,$2,$3,$4,$5,'1','refs/heads/main',$6,$7,'Concurrent description.',
		 'Concurrent remediation.',$8,'Concurrent evidence.','synthetic-finding-metric',$9,$9,$9,
		 'observed','open','suppressed',2)`,
		third.ID, h.admin.workspace, asset.ID, "finding-metric-source-"+third.ID,
		"finding-metric-scope-"+third.ID, "finding-metric-identity-"+third.ID,
		third.Title, third.Severity, findingMetricNow.Add(-time.Hour))
	ok(t, "insert concurrent finding metric row", err)
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+findingMetricTable(h, "finding_disposition_approvals")+`
		(id,workspace_id,finding_id,decision_revision,actor_id,disposition,scope_kind,
		 scope_value,rationale,expires_at,created_at)
		VALUES($1,$2,$3,2,$4,'suppressed','finding',$3,$5,$6,$7)`,
		findingMetricApprovalID(third.ID, 2), h.admin.workspace, third.ID, h.admin.user.ID,
		"Concurrent added expired suppression approval.", findingMetricNow, findingMetricNow)
	ok(t, "insert concurrent finding metric approval", err)
	ok(t, "commit concurrent finding and approval changes", tx.Commit(h.services.ctx))

	tracer.gate.open()
	response := future.wait(t)
	clock.stop(t, "stable finding metric request")
	if response.Code != http.StatusOK {
		t.Fatalf("stable finding metric read returned status %d", response.Code)
	}
	result := decodeFindingMetric(t, response.Body.Bytes())
	want, total, cursor := expectedFindingMetricPage(seeds, map[string]bool{},
		"expired-suppression", findingMetricNow, 1, "")
	assertFindingMetricPage(t, result, h.admin.workspace, "expired-suppression",
		findingMetricNow, want, total, cursor, "", 1)
	if total != 2 || len(want) != 1 || want[0].FindingID != first.ID {
		t.Fatal("stable finding metric fixture did not exercise whole total and first native page")
	}
	if tracer.begins.Load() != 1 || !tracer.readOnly.Load() ||
		tracer.commits.Load() != 1 || tracer.rollbacks.Load() != 0 ||
		tracer.selects.Load() < 1 || tracer.writes.Load() != 0 || tracer.locks.Load() != 0 {
		t.Fatalf("finding metric transaction was not one read-only repeatable-read snapshot: begins=%d commits=%d rollbacks=%d selects=%d writes=%d locks=%d readOnly=%t",
			tracer.begins.Load(), tracer.commits.Load(), tracer.rollbacks.Load(),
			tracer.selects.Load(), tracer.writes.Load(), tracer.locks.Load(), tracer.readOnly.Load())
	}

	tracer.armed.Store(false)
	second.SuppressionApprovals = append(second.SuppressionApprovals, findingMetricNow.Add(time.Hour))
	laterSeeds := []findingMetricSeed{first, second, third}
	laterSeeds[0].Disposition = "none"
	_, later := requestFindingMetric(t, h, clock, h.admin,
		findingMetricPath+"?metric=expired-suppression&limit=100", nil, nil)
	laterWant, laterTotal, laterCursor := expectedFindingMetricPage(laterSeeds, map[string]bool{},
		"expired-suppression", findingMetricNow, 100, "")
	assertFindingMetricPage(t, later, h.admin.workspace, "expired-suppression",
		findingMetricNow, laterWant, laterTotal, laterCursor, "", 100)
	if laterTotal != 1 || len(laterWant) != 1 || laterWant[0].FindingID != third.ID {
		t.Fatal("later finding metric transaction did not observe committed finding and latest-approval changes")
	}
}
