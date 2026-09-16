package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultFreshnessDays = 7
	reportQueryTime      = 15 * time.Second
)

type PostureTotals struct {
	Assets              int64 `json:"assets"`
	Findings            int64 `json:"findings"`
	OpenFindings        int64 `json:"openFindings"`
	AcceptedRisk        int64 `json:"acceptedRisk"`
	ExpiredAcceptedRisk int64 `json:"expiredAcceptedRisk"`
	InferredResolved    int64 `json:"inferredResolved"`
	VerifiedResolved    int64 `json:"verifiedResolved"`
}

type SeverityCounts struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
	Info     int64 `json:"info"`
}

type PostureCoverage struct {
	ScannedAssets          int64 `json:"scannedAssets"`
	UnscannedAssets        int64 `json:"unscannedAssets"`
	StaleAssets            int64 `json:"staleAssets"`
	UnknownFreshnessAssets int64 `json:"unknownFreshnessAssets"`
	FreshnessWindowDays    int   `json:"freshnessWindowDays"`
}

type FreshnessWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Days int       `json:"days"`
}

type PostureReport struct {
	WorkspaceID     string          `json:"workspaceId"`
	AsOf            time.Time       `json:"asOf"`
	Totals          PostureTotals   `json:"totals"`
	BySeverity      SeverityCounts  `json:"bySeverity"`
	Coverage        PostureCoverage `json:"coverage"`
	FreshnessWindow FreshnessWindow `json:"freshnessWindow"`
	Verification    struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"verification"`
}

func validFreshnessDays(days int) bool { return days >= 1 && days <= 365 }

func overviewFreshnessDays(rawQuery string) (int, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return 0, errInvalid
	}
	for key := range query {
		if key != "freshnessDays" {
			return 0, errInvalid
		}
	}
	values, provided := query["freshnessDays"]
	if !provided {
		return defaultFreshnessDays, nil
	}
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 3 {
		return 0, errInvalid
	}
	for _, char := range values[0] {
		if char < '0' || char > '9' {
			return 0, errInvalid
		}
	}
	days, err := strconv.Atoi(values[0])
	if err != nil || !validFreshnessDays(days) {
		return 0, errInvalid
	}
	return days, nil
}

func (a *Application) reportOverview(w http.ResponseWriter, r *http.Request, workspace string) error {
	days, err := overviewFreshnessDays(r.URL.RawQuery)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportQueryTime)
	defer cancel()
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	report, err := a.readPosture(ctx, tx, workspace, days, a.config.Now())
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "report": report})
	return nil
}

func (a *database) readPosture(ctx context.Context, db queryRower, workspace string, days int, asOf time.Time) (PostureReport, error) {
	if !validFreshnessDays(days) {
		return PostureReport{}, errInvalid
	}
	asOf = asOf.UTC()
	report := PostureReport{
		WorkspaceID: workspace, AsOf: asOf,
		Coverage:        PostureCoverage{FreshnessWindowDays: days},
		FreshnessWindow: FreshnessWindow{From: asOf.Add(-time.Duration(days) * 24 * time.Hour), To: asOf, Days: days},
	}
	// Canonical findings currently expose verifiedResolution=false: independent
	// verification results are not integrated into this domain. Keep its count
	// zero and expose that limitation, never count human or source resolution.
	report.Verification.State = "not-run"
	report.Verification.Reason = "Independent verified-resolution results are not integrated with canonical findings."
	err := db.QueryRow(ctx, `WITH source_scopes AS (
		SELECT asset_id,source_id,scope_id,scope_revision,scope_branch,
			max(source_scan_at) FILTER (WHERE source_scan_at<=$2) AS scanned_at
		FROM `+a.table("imports")+`
		WHERE workspace_id=$1 AND state='succeeded' AND source_status='succeeded'
			AND scan_kind='full' AND completeness='complete'
		GROUP BY asset_id,source_id,scope_id,scope_revision,scope_branch
	), assessed_assets AS (
		SELECT asset_id,bool_or(scanned_at<$3) AS stale,
			bool_or(scanned_at IS NULL) AS unknown_freshness
		FROM source_scopes GROUP BY asset_id
	), asset_totals AS (
		SELECT count(*) AS assets,
			count(*) FILTER (WHERE assessed.asset_id IS NOT NULL) AS scanned,
			count(*) FILTER (WHERE assessed.asset_id IS NULL) AS unscanned,
			count(*) FILTER (WHERE assessed.stale) AS stale,
			count(*) FILTER (WHERE assessed.unknown_freshness) AS unknown_freshness
		FROM `+a.table("assets")+` asset LEFT JOIN assessed_assets assessed ON assessed.asset_id=asset.id
		WHERE asset.workspace_id=$1
	), finding_totals AS (
		SELECT count(*) AS findings,
			count(*) FILTER (WHERE workflow_state<>'resolved') AS open_findings,
			count(*) FILTER (WHERE disposition='accepted-risk') AS accepted_risk,
			count(*) FILTER (WHERE disposition='accepted-risk' AND accepted_risk_expires_at<=$2) AS expired_accepted_risk,
			count(*) FILTER (WHERE source_state='inferred-resolved') AS inferred_resolved,
			count(*) FILTER (WHERE severity='critical') AS critical,
			count(*) FILTER (WHERE severity='high') AS high,
			count(*) FILTER (WHERE severity='medium') AS medium,
			count(*) FILTER (WHERE severity='low') AS low,
			count(*) FILTER (WHERE severity='info') AS info
		FROM `+a.table("findings")+` WHERE workspace_id=$1
	)
	SELECT a.assets,f.findings,f.open_findings,f.accepted_risk,f.expired_accepted_risk,f.inferred_resolved,
		f.critical,f.high,f.medium,f.low,f.info,a.scanned,a.unscanned,a.stale,a.unknown_freshness
	FROM asset_totals a CROSS JOIN finding_totals f`, workspace, asOf, report.FreshnessWindow.From).Scan(
		&report.Totals.Assets, &report.Totals.Findings, &report.Totals.OpenFindings,
		&report.Totals.AcceptedRisk, &report.Totals.ExpiredAcceptedRisk, &report.Totals.InferredResolved,
		&report.BySeverity.Critical, &report.BySeverity.High, &report.BySeverity.Medium, &report.BySeverity.Low, &report.BySeverity.Info,
		&report.Coverage.ScannedAssets, &report.Coverage.UnscannedAssets,
		&report.Coverage.StaleAssets, &report.Coverage.UnknownFreshnessAssets,
	)
	return report, err
}
