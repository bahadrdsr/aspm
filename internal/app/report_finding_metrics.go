package app

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

const findingMetricVerificationReason = "Finding metric drill-down reflects current canonical finding state; it does not verify safety or historical membership."

var findingMetricNames = map[string]bool{
	"findings": true, "open-findings": true, "accepted-risk": true,
	"expired-accepted-risk": true, "suppressed": true, "expired-suppression": true,
	"false-positive": true, "inferred-resolved": true, "critical": true,
	"high": true, "medium": true, "low": true, "info": true,
}

type FindingMetricItem struct {
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

type FindingMetricDrilldown struct {
	WorkspaceID  string              `json:"workspaceId"`
	Metric       string              `json:"metric"`
	AsOf         time.Time           `json:"asOf"`
	Items        []FindingMetricItem `json:"items"`
	Total        int64               `json:"total"`
	NextCursor   *string             `json:"nextCursor"`
	Verification struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"verification"`
}

func findingMetricParameters(rawQuery string) (string, int, string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", 0, "", errInvalid
	}
	for key, values := range query {
		if key != "metric" && key != "limit" && key != "cursor" || len(values) != 1 {
			return "", 0, "", errInvalid
		}
	}
	values, present := query["metric"]
	if !present || !findingMetricNames[values[0]] {
		return "", 0, "", errInvalid
	}
	metric := values[0]
	limit := 100
	if values, present = query["limit"]; present {
		limit, err = strictReportInteger(values[0], 1, 100)
		if err != nil {
			return "", 0, "", err
		}
	}
	cursor := ""
	if values, present = query["cursor"]; present {
		cursor = values[0]
		if !validID(cursor) {
			return "", 0, "", errInvalid
		}
	}
	return metric, limit, cursor, nil
}

func (a *database) findingMetricFrom() string {
	return ` FROM ` + a.table("findings") + ` f
		JOIN ` + a.table("assets") + ` asset ON asset.workspace_id=f.workspace_id AND asset.id=f.asset_id
		LEFT JOIN ` + a.table("users") + ` owner ON owner.id=f.owner_id
		LEFT JOIN LATERAL (
			SELECT expires_at FROM ` + a.table("finding_disposition_approvals") + ` approval
			WHERE approval.workspace_id=f.workspace_id AND approval.finding_id=f.id
			AND approval.disposition=f.disposition AND approval.decision_revision<=f.decision_revision
			ORDER BY approval.decision_revision DESC LIMIT 1
		) approval ON true`
}

func findingMetricPredicate(metric string) (string, error) {
	switch metric {
	case "findings":
		return "true", nil
	case "open-findings":
		return "f.workflow_state<>'resolved'", nil
	case "accepted-risk":
		return "f.disposition='accepted-risk'", nil
	case "expired-accepted-risk":
		return "f.disposition='accepted-risk' AND f.accepted_risk_expires_at IS NOT NULL AND f.accepted_risk_expires_at<=$2", nil
	case "suppressed":
		return "f.disposition='suppressed'", nil
	case "expired-suppression":
		return "f.disposition='suppressed' AND approval.expires_at IS NOT NULL AND approval.expires_at<=$2", nil
	case "false-positive":
		return "f.disposition='false-positive'", nil
	case "inferred-resolved":
		return "f.source_state='inferred-resolved'", nil
	case "critical", "high", "medium", "low", "info":
		return "f.severity='" + metric + "'", nil
	default:
		return "", errInvalid
	}
}

func findingMetricAsOf(r *http.Request, now time.Time) (time.Time, error) {
	values, present := r.Header[http.CanonicalHeaderKey("X-ASPM-Report-As-Of")]
	if !present {
		return now, nil
	}
	if len(values) != 1 || values[0] == "" {
		return time.Time{}, errInvalid
	}
	raw := values[0]
	asOf, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || asOf.After(now) {
		return time.Time{}, errInvalid
	}
	return asOf.UTC(), nil
}

func scanFindingMetricItem(row pgx.Row) (FindingMetricItem, error) {
	var item FindingMetricItem
	err := row.Scan(&item.FindingID, &item.Title, &item.AssetID, &item.AssetName,
		&item.Severity, &item.OwnerID, &item.OwnerName, &item.WorkflowState,
		&item.Disposition, &item.AcceptedRiskExpiresAt, &item.RiskAcceptanceExpired,
		&item.SourceState, &item.SourceFreshnessAt)
	if err == nil {
		if item.AcceptedRiskExpiresAt != nil {
			value := item.AcceptedRiskExpiresAt.UTC()
			item.AcceptedRiskExpiresAt = &value
		}
		if item.SourceFreshnessAt != nil {
			value := item.SourceFreshnessAt.UTC()
			item.SourceFreshnessAt = &value
		}
	}
	return item, err
}

func (a *Application) reportFindingMetrics(w http.ResponseWriter, r *http.Request, workspace string) error {
	metric, limit, cursor, err := findingMetricParameters(r.URL.RawQuery)
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
	now := a.config.Now().UTC()
	asOf, err := findingMetricAsOf(r, now)
	if err != nil {
		return err
	}
	predicate, err := findingMetricPredicate(metric)
	if err != nil {
		return err
	}
	from := a.findingMetricFrom()
	where := ` WHERE f.workspace_id=$1 AND $2::timestamptz IS NOT NULL AND ` +
		a.workVisible() + ` AND ` + predicate
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*)`+from+where, workspace, asOf).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT f.id,f.title,f.asset_id,asset.name,f.severity,
		f.owner_id,owner.name,f.workflow_state,f.disposition,f.accepted_risk_expires_at,
		f.disposition='accepted-risk' AND f.accepted_risk_expires_at IS NOT NULL AND f.accepted_risk_expires_at<=$2,
		f.source_state,f.source_freshness_at`+from+where+` AND f.id>$3 ORDER BY f.id LIMIT $4`,
		workspace, asOf, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []FindingMetricItem{}
	for rows.Next() {
		item, scanErr := scanFindingMetricItem(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].FindingID
	}
	drilldown := FindingMetricDrilldown{
		WorkspaceID: workspace, Metric: metric, AsOf: asOf,
		Items: items, Total: total, NextCursor: next,
	}
	drilldown.Verification.State = "not-run"
	drilldown.Verification.Reason = findingMetricVerificationReason
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "drilldown": drilldown})
	return nil
}
