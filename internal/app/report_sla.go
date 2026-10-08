package app

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const remediationSLAVerificationReason = "Remediation SLA status is a time-to-workflow target; it does not verify safety, resolution, or risk acceptance."

type ReportSLAPolicy struct {
	WorkspaceID    string    `json:"workspaceId"`
	CriticalDays   int       `json:"criticalDays"`
	HighDays       int       `json:"highDays"`
	MediumDays     int       `json:"mediumDays"`
	LowDays        int       `json:"lowDays"`
	InfoDays       int       `json:"infoDays"`
	Revision       int64     `json:"revision"`
	ApprovedBy     *string   `json:"approvedBy"`
	ApprovedByName *string   `json:"approvedByName"`
	Rationale      string    `json:"rationale"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type SLACounts struct {
	Tracked  int64 `json:"tracked"`
	Breached int64 `json:"breached"`
}

type SLATotals struct {
	Tracked      int64 `json:"tracked"`
	WithinTarget int64 `json:"withinTarget"`
	Breached     int64 `json:"breached"`
}

type RemediationSLASummary struct {
	WorkspaceID string          `json:"workspaceId"`
	AsOf        time.Time       `json:"asOf"`
	Policy      ReportSLAPolicy `json:"policy"`
	Totals      SLATotals       `json:"totals"`
	BySeverity  struct {
		Critical SLACounts `json:"critical"`
		High     SLACounts `json:"high"`
		Medium   SLACounts `json:"medium"`
		Low      SLACounts `json:"low"`
		Info     SLACounts `json:"info"`
	} `json:"bySeverity"`
	Verification struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"verification"`
}

type RemediationSLAFinding struct {
	FindingID       string    `json:"findingId"`
	Title           string    `json:"title"`
	AssetID         string    `json:"assetId"`
	AssetName       string    `json:"assetName"`
	Severity        string    `json:"severity"`
	OwnerID         *string   `json:"ownerId"`
	OwnerName       *string   `json:"ownerName"`
	WorkflowState   string    `json:"workflowState"`
	Disposition     string    `json:"disposition"`
	SourceState     string    `json:"sourceState"`
	FirstObservedAt time.Time `json:"firstObservedAt"`
	DueAt           time.Time `json:"dueAt"`
	TargetDays      int       `json:"targetDays"`
	Status          string    `json:"status"`
	OverdueSeconds  int64     `json:"overdueSeconds"`
}

const reportSLAPolicyColumns = `workspace_id,critical_days,high_days,medium_days,low_days,info_days,
	revision,approved_by,approved_by_name,rationale,created_at,updated_at`

func scanReportSLAPolicy(row pgx.Row) (ReportSLAPolicy, error) {
	var policy ReportSLAPolicy
	err := row.Scan(&policy.WorkspaceID, &policy.CriticalDays, &policy.HighDays,
		&policy.MediumDays, &policy.LowDays, &policy.InfoDays, &policy.Revision,
		&policy.ApprovedBy, &policy.ApprovedByName, &policy.Rationale,
		&policy.CreatedAt, &policy.UpdatedAt)
	if err == nil {
		policy.CreatedAt = policy.CreatedAt.UTC()
		policy.UpdatedAt = policy.UpdatedAt.UTC()
	}
	return policy, err
}

func validSLATargets(critical, high, medium, low, info int) bool {
	return critical >= 1 && info <= 3650 &&
		critical <= high && high <= medium && medium <= low && low <= info
}

func (a *Application) insertInitialWorkspaceSLAPolicy(ctx context.Context, tx pgx.Tx,
	workspace string, actor User, now time.Time) error {
	const rationale = "Initial workspace remediation targets."
	if _, err := tx.Exec(ctx, `INSERT INTO `+a.table("report_sla_policies")+`
		(workspace_id,critical_days,high_days,medium_days,low_days,info_days,revision,
		 approved_by,approved_by_name,rationale,created_at,updated_at)
		VALUES($1,7,30,90,180,365,1,$2,$3,$4,$5,$5)`,
		workspace, actor.ID, actor.Name, rationale, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+a.table("report_sla_policy_revisions")+`
		(workspace_id,revision,critical_days,high_days,medium_days,low_days,info_days,
		 approved_by,approved_by_name,rationale,created_at)
		VALUES($1,1,7,30,90,180,365,$2,$3,$4,$5)`,
		workspace, actor.ID, actor.Name, rationale, now)
	return err
}

func (a *Application) readReportSLAPolicy(ctx context.Context, db queryRower,
	workspace string) (ReportSLAPolicy, error) {
	return scanReportSLAPolicy(db.QueryRow(ctx, `SELECT `+reportSLAPolicyColumns+`
		FROM `+a.table("report_sla_policies")+` WHERE workspace_id=$1`, workspace))
}

func (a *Application) getReportSLAPolicy(w http.ResponseWriter, r *http.Request,
	workspace string) error {
	if r.URL.RawQuery != "" {
		return errInvalid
	}
	policy, err := a.readReportSLAPolicy(r.Context(), a.pool, workspace)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "policy": policy})
	return nil
}

func (a *Application) updateReportSLAPolicy(w http.ResponseWriter, r *http.Request,
	workspace string, session authenticatedSession) error {
	if r.URL.RawQuery != "" {
		return errInvalid
	}
	var input struct {
		Revision     int64  `json:"revision"`
		CriticalDays int    `json:"criticalDays"`
		HighDays     int    `json:"highDays"`
		MediumDays   int    `json:"mediumDays"`
		LowDays      int    `json:"lowDays"`
		InfoDays     int    `json:"infoDays"`
		Rationale    string `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if input.Revision < 1 ||
		!validSLATargets(input.CriticalDays, input.HighDays, input.MediumDays, input.LowDays, input.InfoDays) ||
		!validText(input.Rationale, 8192) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	current, err := scanReportSLAPolicy(tx.QueryRow(r.Context(), `SELECT `+reportSLAPolicyColumns+`
		FROM `+a.table("report_sla_policies")+` WHERE workspace_id=$1 FOR UPDATE`, workspace))
	if err != nil {
		return err
	}
	if current.Revision != input.Revision {
		return errConflict
	}
	if current.CriticalDays == input.CriticalDays && current.HighDays == input.HighDays &&
		current.MediumDays == input.MediumDays && current.LowDays == input.LowDays &&
		current.InfoDays == input.InfoDays && current.Rationale == input.Rationale {
		return errConflict
	}
	now := a.config.Now().UTC()
	next := current
	next.CriticalDays, next.HighDays, next.MediumDays = input.CriticalDays, input.HighDays, input.MediumDays
	next.LowDays, next.InfoDays, next.Revision = input.LowDays, input.InfoDays, current.Revision+1
	next.ApprovedBy, next.ApprovedByName = &session.User.ID, &session.User.Name
	next.Rationale, next.UpdatedAt = input.Rationale, now
	result, err := tx.Exec(r.Context(), `UPDATE `+a.table("report_sla_policies")+`
		SET critical_days=$3,high_days=$4,medium_days=$5,low_days=$6,info_days=$7,
		 revision=$8,approved_by=$9,approved_by_name=$10,rationale=$11,updated_at=$12
		WHERE workspace_id=$1 AND revision=$2`,
		workspace, current.Revision, next.CriticalDays, next.HighDays, next.MediumDays,
		next.LowDays, next.InfoDays, next.Revision, next.ApprovedBy, next.ApprovedByName,
		next.Rationale, next.UpdatedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errConflict
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("report_sla_policy_revisions")+`
		(workspace_id,revision,critical_days,high_days,medium_days,low_days,info_days,
		 approved_by,approved_by_name,rationale,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		workspace, next.Revision, next.CriticalDays, next.HighDays, next.MediumDays,
		next.LowDays, next.InfoDays, next.ApprovedBy, next.ApprovedByName,
		next.Rationale, next.UpdatedAt); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "policy": next})
	return nil
}

func (a *database) remediationSLARowsCTE() string {
	return `WITH active_members AS (
		SELECT c.workspace_id,c.primary_finding_id,m.finding_id
		FROM ` + a.table("finding_correlations") + ` c
		JOIN ` + a.table("finding_correlation_members") + ` m
		ON m.workspace_id=c.workspace_id AND m.correlation_id=c.id
		WHERE c.workspace_id=$1 AND c.state='active' AND m.released_at IS NULL
	), effective_ages AS (
		SELECT am.workspace_id,am.primary_finding_id,
			min(member.first_observed_at) AS first_observed_at
		FROM active_members am JOIN ` + a.table("findings") + ` member
		ON member.workspace_id=am.workspace_id AND member.id=am.finding_id
		GROUP BY am.workspace_id,am.primary_finding_id
	), visible AS (
		SELECT f.*,COALESCE(age.first_observed_at,f.first_observed_at) AS effective_first_observed_at
		FROM ` + a.table("findings") + ` f
		LEFT JOIN effective_ages age ON age.workspace_id=f.workspace_id
		AND age.primary_finding_id=f.id
		WHERE f.workspace_id=$1 AND f.workflow_state<>'resolved'
		AND NOT EXISTS(SELECT 1 FROM active_members hidden
		 WHERE hidden.workspace_id=f.workspace_id AND hidden.finding_id=f.id
		 AND hidden.primary_finding_id<>f.id)
	), targeted AS (
		SELECT f.*,asset.name AS asset_name,owner.name AS owner_name,
			CASE f.severity
			 WHEN 'critical' THEN policy.critical_days
			 WHEN 'high' THEN policy.high_days
			 WHEN 'medium' THEN policy.medium_days
			 WHEN 'low' THEN policy.low_days
			 ELSE policy.info_days END AS target_days
		FROM visible f
		JOIN ` + a.table("assets") + ` asset ON asset.workspace_id=f.workspace_id AND asset.id=f.asset_id
		LEFT JOIN ` + a.table("users") + ` owner ON owner.id=f.owner_id
		JOIN ` + a.table("report_sla_policies") + ` policy ON policy.workspace_id=f.workspace_id
	), deadlines AS (
		SELECT targeted.*,effective_first_observed_at+(target_days*interval '1 day') AS due_at
		FROM targeted
	), sla_rows AS (
		SELECT deadlines.*,
			CASE WHEN due_at<$2 THEN 'breached' ELSE 'within-target' END AS sla_status,
			floor(greatest(0,extract(epoch FROM ($2-due_at))))::bigint AS overdue_seconds
		FROM deadlines
	)`
}

func (a *Application) reportSLA(w http.ResponseWriter, r *http.Request, workspace string) error {
	if r.URL.RawQuery != "" {
		return errInvalid
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportQueryTime)
	defer cancel()
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := a.config.Now().UTC()
	policy, err := a.readReportSLAPolicy(ctx, tx, workspace)
	if err != nil {
		return err
	}
	summary := RemediationSLASummary{WorkspaceID: workspace, AsOf: now, Policy: policy}
	err = tx.QueryRow(ctx, a.remediationSLARowsCTE()+`
		SELECT count(*),count(*) FILTER(WHERE sla_status='within-target'),
			count(*) FILTER(WHERE sla_status='breached'),
			count(*) FILTER(WHERE severity='critical'),count(*) FILTER(WHERE severity='critical' AND sla_status='breached'),
			count(*) FILTER(WHERE severity='high'),count(*) FILTER(WHERE severity='high' AND sla_status='breached'),
			count(*) FILTER(WHERE severity='medium'),count(*) FILTER(WHERE severity='medium' AND sla_status='breached'),
			count(*) FILTER(WHERE severity='low'),count(*) FILTER(WHERE severity='low' AND sla_status='breached'),
			count(*) FILTER(WHERE severity='info'),count(*) FILTER(WHERE severity='info' AND sla_status='breached')
		FROM sla_rows`, workspace, now).Scan(
		&summary.Totals.Tracked, &summary.Totals.WithinTarget, &summary.Totals.Breached,
		&summary.BySeverity.Critical.Tracked, &summary.BySeverity.Critical.Breached,
		&summary.BySeverity.High.Tracked, &summary.BySeverity.High.Breached,
		&summary.BySeverity.Medium.Tracked, &summary.BySeverity.Medium.Breached,
		&summary.BySeverity.Low.Tracked, &summary.BySeverity.Low.Breached,
		&summary.BySeverity.Info.Tracked, &summary.BySeverity.Info.Breached)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	summary.Verification.State = "not-run"
	summary.Verification.Reason = remediationSLAVerificationReason
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "sla": summary})
	return nil
}

func remediationSLAFindingParameters(rawQuery string) (string, int, string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", 0, "", errInvalid
	}
	for key, values := range query {
		if key != "status" && key != "limit" && key != "cursor" || len(values) != 1 {
			return "", 0, "", errInvalid
		}
	}
	values, present := query["status"]
	if !present || values[0] != "breached" && values[0] != "within-target" {
		return "", 0, "", errInvalid
	}
	status := values[0]
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
	return status, limit, cursor, nil
}

func scanRemediationSLAFinding(row pgx.Row) (RemediationSLAFinding, error) {
	var finding RemediationSLAFinding
	err := row.Scan(&finding.FindingID, &finding.Title, &finding.AssetID, &finding.AssetName,
		&finding.Severity, &finding.OwnerID, &finding.OwnerName, &finding.WorkflowState,
		&finding.Disposition, &finding.SourceState, &finding.FirstObservedAt, &finding.DueAt,
		&finding.TargetDays, &finding.Status, &finding.OverdueSeconds)
	if err == nil {
		finding.FirstObservedAt = finding.FirstObservedAt.UTC()
		finding.DueAt = finding.DueAt.UTC()
	}
	return finding, err
}

func (a *Application) remediationSLAEvaluationTime(ctx context.Context, tx pgx.Tx,
	r *http.Request, workspace string, now time.Time) (time.Time, error) {
	rawTime := r.Header.Get("X-ASPM-SLA-As-Of")
	rawRevision := r.Header.Get("X-ASPM-SLA-Policy-Revision")
	if rawTime == "" && rawRevision == "" {
		return now, nil
	}
	if rawTime == "" || rawRevision == "" {
		return time.Time{}, errInvalid
	}
	asOf, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil || asOf.After(now) {
		return time.Time{}, errInvalid
	}
	revision, err := strconv.ParseInt(rawRevision, 10, 64)
	if err != nil || revision < 1 {
		return time.Time{}, errInvalid
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM `+a.table("report_sla_policies")+
		` WHERE workspace_id=$1`, workspace).Scan(&current); err != nil {
		return time.Time{}, err
	}
	if current != revision {
		return time.Time{}, errConflict
	}
	return asOf.UTC(), nil
}

func (a *Application) reportSLAFindingPage(w http.ResponseWriter, r *http.Request,
	workspace string) error {
	status, limit, cursor, err := remediationSLAFindingParameters(r.URL.RawQuery)
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
	now, err = a.remediationSLAEvaluationTime(ctx, tx, r, workspace, now)
	if err != nil {
		return err
	}
	query := a.remediationSLARowsCTE()
	var total int64
	if err = tx.QueryRow(ctx, query+`
		SELECT count(*) FROM sla_rows WHERE sla_status=$3`, workspace, now, status).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, query+`
		SELECT id,title,asset_id,asset_name,severity,owner_id,owner_name,workflow_state,
			disposition,source_state,effective_first_observed_at,due_at,target_days,sla_status,overdue_seconds
		FROM sla_rows WHERE sla_status=$3 AND id>$4 ORDER BY id LIMIT $5`,
		workspace, now, status, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []RemediationSLAFinding{}
	for rows.Next() {
		item, scanErr := scanRemediationSLAFinding(rows)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"dataOrigin": "live", "items": items, "total": total, "nextCursor": next,
	})
	return nil
}
