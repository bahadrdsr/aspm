package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	retentionPreviewLimit = 200
	retentionPreviewTTL   = 15 * time.Minute
)

type retentionDB interface {
	queryRower
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type retentionSnapshotItem struct {
	RetentionPreviewItem
	binding string
}

type retentionSnapshot struct {
	Policy    RetentionPolicy
	Summaries []RetentionClassSummary
	Items     []retentionSnapshotItem
	Digest    string
}

func (a *Application) readRetentionPolicy(ctx context.Context, db queryRower, workspace string) (RetentionPolicy, error) {
	var policy RetentionPolicy
	err := db.QueryRow(ctx, `SELECT workspace_id,revision,hot_history_days,raw_report_days,
		archived_evidence_days,audit_days,updated_by,updated_at
		FROM `+a.table("retention_policies")+` WHERE workspace_id=$1`, workspace).Scan(
		&policy.WorkspaceID, &policy.Revision, &policy.HotHistoryDays, &policy.RawReportDays,
		&policy.ArchivedEvidenceDays, &policy.AuditDays, &policy.UpdatedBy, &policy.UpdatedAt)
	if policy.UpdatedAt != nil {
		value := policy.UpdatedAt.UTC()
		policy.UpdatedAt = &value
	}
	return policy, err
}

func validRetentionPolicy(policy RetentionPolicy) bool {
	return policy.Revision > 0 && policy.HotHistoryDays >= 1 && policy.AuditDays <= 3650 &&
		policy.HotHistoryDays < policy.RawReportDays &&
		policy.RawReportDays < policy.ArchivedEvidenceDays &&
		policy.ArchivedEvidenceDays < policy.AuditDays
}

func (a *Application) getRetentionPolicy(w http.ResponseWriter, r *http.Request, workspace string) error {
	policy, err := a.readRetentionPolicy(r.Context(), a.pool, workspace)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionPolicy": policy})
	return nil
}

func (a *Application) updateRetentionPolicy(w http.ResponseWriter, r *http.Request, workspace, actor string) error {
	var input struct {
		Revision             int64 `json:"revision"`
		HotHistoryDays       int   `json:"hotHistoryDays"`
		RawReportDays        int   `json:"rawReportDays"`
		ArchivedEvidenceDays int   `json:"archivedEvidenceDays"`
		AuditDays            int   `json:"auditDays"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	next := RetentionPolicy{
		Revision: input.Revision, HotHistoryDays: input.HotHistoryDays,
		RawReportDays: input.RawReportDays, ArchivedEvidenceDays: input.ArchivedEvidenceDays,
		AuditDays: input.AuditDays,
	}
	if !validRetentionPolicy(next) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	result, err := tx.Exec(r.Context(), `UPDATE `+a.table("retention_policies")+`
		SET revision=revision+1,hot_history_days=$3,raw_report_days=$4,
			archived_evidence_days=$5,audit_days=$6,updated_by=$7,updated_at=$8
		WHERE workspace_id=$1 AND revision=$2`,
		workspace, input.Revision, input.HotHistoryDays, input.RawReportDays,
		input.ArchivedEvidenceDays, input.AuditDays, actor, a.config.Now().UTC())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errConflict
	}
	policy, err := a.readRetentionPolicy(r.Context(), tx, workspace)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionPolicy": policy})
	return nil
}

func scanRetentionHold(row pgx.Row) (RetentionHold, error) {
	var hold RetentionHold
	err := row.Scan(&hold.ID, &hold.WorkspaceID, &hold.ResourceKind, &hold.ResourceID, &hold.Reason,
		&hold.Revision, &hold.CreatedBy, &hold.CreatedAt, &hold.ReleasedBy, &hold.ReleasedAt,
		&hold.ReleaseRationale)
	hold.CreatedAt = hold.CreatedAt.UTC()
	if hold.ReleasedAt != nil {
		value := hold.ReleasedAt.UTC()
		hold.ReleasedAt = &value
	}
	return hold, err
}

const retentionHoldColumns = `id,workspace_id,resource_kind,resource_id,reason,revision,created_by,
	created_at,released_by,released_at,release_rationale`

func (a *Application) retentionResourceExists(ctx context.Context, db queryRower,
	workspace, kind, id string) (bool, error) {
	table := ""
	switch kind {
	case "import":
		table = "imports"
	case "observation":
		table = "observations"
	case "correlation-event":
		table = "finding_correlation_events"
	case "finding-decision-event":
		table = "finding_decision_events"
	case "notification-policy-revision":
		table = "notification_policy_revisions"
	case "finding-change-event":
		table = "finding_change_events"
	case "notification-policy-event":
		table = "notification_policy_events"
	default:
		return false, nil
	}
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table(table)+`
		WHERE workspace_id=$1 AND id=$2)`, workspace, id).Scan(&exists)
	return exists, err
}

func (a *Application) listRetentionHolds(w http.ResponseWriter, r *http.Request, workspace string) error {
	rows, err := a.pool.Query(r.Context(), `SELECT `+retentionHoldColumns+`
		FROM `+a.table("retention_holds")+` WHERE workspace_id=$1
		ORDER BY created_at,id LIMIT 501`, workspace)
	if err != nil {
		return err
	}
	defer rows.Close()
	holds := []RetentionHold{}
	for rows.Next() {
		hold, err := scanRetentionHold(rows)
		if err != nil {
			return err
		}
		holds = append(holds, hold)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(holds) > 500 {
		return errTooLarge
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionHolds": holds})
	return nil
}

func (a *Application) createRetentionHold(w http.ResponseWriter, r *http.Request, workspace, actor string) error {
	var input struct {
		ResourceKind string `json:"resourceKind"`
		ResourceID   string `json:"resourceId"`
		Reason       string `json:"reason"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.ResourceID) || !validRetentionHoldKind(input.ResourceKind) ||
		!validText(input.Reason, 8192) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	exists, err := a.retentionResourceExists(r.Context(), tx, workspace, input.ResourceKind, input.ResourceID)
	if err != nil {
		return err
	}
	if !exists {
		return errNotFound
	}
	hold := RetentionHold{
		ID: newID(), WorkspaceID: workspace, ResourceKind: input.ResourceKind,
		ResourceID: input.ResourceID, Reason: input.Reason, Revision: 1,
		CreatedBy: actor, CreatedAt: a.config.Now().UTC(),
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_holds")+`
		(id,workspace_id,resource_kind,resource_id,reason,revision,created_by,created_at)
		VALUES($1,$2,$3,$4,$5,1,$6,$7)`,
		hold.ID, workspace, hold.ResourceKind, hold.ResourceID, hold.Reason, actor, hold.CreatedAt); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"retentionHold": hold})
	return nil
}

func (a *Application) releaseRetentionHold(w http.ResponseWriter, r *http.Request,
	workspace, actor, id string) error {
	var input struct {
		Revision  int64  `json:"revision"`
		Rationale string `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if input.Revision < 1 || !validText(input.Rationale, 8192) {
		return errInvalid
	}
	now := a.config.Now().UTC()
	result, err := a.pool.Exec(r.Context(), `UPDATE `+a.table("retention_holds")+`
		SET revision=revision+1,released_by=$4,released_at=$5,release_rationale=$6
		WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND released_at IS NULL`,
		workspace, id, input.Revision, actor, now, input.Rationale)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		var exists bool
		if err = a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+
			a.table("retention_holds")+` WHERE workspace_id=$1 AND id=$2)`, workspace, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errNotFound
		}
		return errConflict
	}
	hold, err := scanRetentionHold(a.pool.QueryRow(r.Context(), `SELECT `+retentionHoldColumns+`
		FROM `+a.table("retention_holds")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionHold": hold})
	return nil
}

func retentionReasons(hold bool, decision bool, shared bool, extra string) []string {
	reasons := []string{}
	if hold {
		reasons = append(reasons, "legal-hold")
	}
	if decision {
		reasons = append(reasons, "active-decision")
	}
	if shared {
		reasons = append(reasons, "shared-observation-references")
	}
	if extra != "" {
		reasons = append(reasons, extra)
	}
	return reasons
}

func newRetentionSummaries(policy RetentionPolicy) []RetentionClassSummary {
	return []RetentionClassSummary{
		{Class: "hot-history", Action: "archive-history", RetainDays: policy.HotHistoryDays},
		{Class: "archived-evidence", Action: "expire-archive", RetainDays: policy.ArchivedEvidenceDays},
		{Class: "raw-report", Action: "expire-raw-report", RetainDays: policy.RawReportDays},
		{Class: "audit", Action: "archive-audit", RetainDays: policy.AuditDays},
		{Class: "orphan-archive", Action: "delete-orphan", RetainDays: 1},
	}
}

func addRetentionItem(snapshot *retentionSnapshot, item retentionSnapshotItem) {
	snapshot.Items = append(snapshot.Items, item)
	for index := range snapshot.Summaries {
		summary := &snapshot.Summaries[index]
		if summary.Class != item.Class {
			continue
		}
		summary.TotalCount++
		if len(item.ProtectedReasons) == 0 {
			summary.EligibleCount++
			summary.SizeBytes += item.SizeBytes
		} else {
			summary.ProtectedCount++
		}
		return
	}
}

func (a *Application) appendHotHistory(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT o.id,i.id,i.imported_at,octet_length(o.data::text),
		f.id,f.decision_revision,f.evidence_revision,
		(f.owner_id IS NOT NULL OR f.workflow_state<>'open' OR f.disposition<>'none'
		 OR EXISTS(SELECT 1 FROM `+a.table("notes")+` n
		  WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		 OR EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` cm
		  JOIN `+a.table("finding_correlations")+` c
		  ON c.workspace_id=cm.workspace_id AND c.id=cm.correlation_id AND c.state='active'
		  WHERE cm.workspace_id=f.workspace_id AND cm.finding_id=f.id AND cm.released_at IS NULL)),
		(SELECT count(*) FROM `+a.table("observations")+` shared
		 WHERE shared.workspace_id=o.workspace_id AND shared.run_id=o.run_id),
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=o.workspace_id AND h.released_at IS NULL
		 AND ((h.resource_kind='observation' AND h.resource_id=o.id)
		  OR (h.resource_kind='import' AND h.resource_id=i.id))),''),
		EXISTS(SELECT 1 FROM `+a.table("assessment_previews")+` p
		 WHERE p.workspace_id=o.workspace_id AND p.observation_id=o.id)
		FROM `+a.table("observations")+` o
		JOIN `+a.table("imports")+` i ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
		JOIN `+a.table("findings")+` f ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		WHERE o.workspace_id=$1 AND i.imported_at<=$2 AND o.evidence_availability='available'
		ORDER BY o.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item retentionSnapshotItem
		var importID, findingID, holds string
		var decision, assessment bool
		var decisionRevision, evidenceRevision int64
		var references int
		if err = rows.Scan(&item.ResourceID, &importID, &item.ObservedAt, &item.SizeBytes,
			&findingID, &decisionRevision, &evidenceRevision, &decision, &references, &holds,
			&assessment); err != nil {
			return err
		}
		item.Class, item.ResourceKind, item.Action = "hot-history", "observation", "archive-history"
		item.ObservedAt = item.ObservedAt.UTC()
		item.ProtectedReasons = retentionReasons(holds != "", decision, false, "")
		if assessment {
			item.ProtectedReasons = append(item.ProtectedReasons, "assessment-reference")
		}
		item.binding = strings.Join([]string{item.ResourceID, importID, findingID,
			timeString(item.ObservedAt), holds, intString(decisionRevision), intString(evidenceRevision),
			intString(int64(references)), boolString(assessment)}, "|")
		addRetentionItem(snapshot, item)
		if len(snapshot.Items) > retentionPreviewLimit {
			return errTooLarge
		}
	}
	return rows.Err()
}

func (a *Application) appendRawReports(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT i.id,i.run_id,i.imported_at,i.report_size,i.report_digest,i.report_key,i.state,
		(SELECT count(*) FROM `+a.table("observations")+` o
		 WHERE o.workspace_id=i.workspace_id AND o.run_id=i.run_id),
		EXISTS(SELECT 1 FROM `+a.table("observations")+` o
		 JOIN `+a.table("findings")+` f
		 ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		 WHERE o.workspace_id=i.workspace_id AND o.run_id=i.run_id
		 AND (f.owner_id IS NOT NULL OR f.workflow_state<>'open' OR f.disposition<>'none'
		  OR EXISTS(SELECT 1 FROM `+a.table("notes")+` n
		   WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		  OR EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` cm
		   JOIN `+a.table("finding_correlations")+` c
		   ON c.workspace_id=cm.workspace_id AND c.id=cm.correlation_id AND c.state='active'
		   WHERE cm.workspace_id=f.workspace_id AND cm.finding_id=f.id AND cm.released_at IS NULL))),
		EXISTS(SELECT 1 FROM `+a.table("observations")+` o
		 JOIN `+a.table("assessment_previews")+` p
		 ON p.workspace_id=o.workspace_id AND p.observation_id=o.id
		 WHERE o.workspace_id=i.workspace_id AND o.run_id=i.run_id),
		COALESCE((SELECT string_agg(binding,',' ORDER BY binding) FROM (
		 SELECT DISTINCT f.id||':'||f.decision_revision::text||':'||f.evidence_revision::text binding
		 FROM `+a.table("observations")+` o JOIN `+a.table("findings")+` f
		 ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		 WHERE o.workspace_id=i.workspace_id AND o.run_id=i.run_id) decisions),''),
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=i.workspace_id AND h.released_at IS NULL
		 AND ((h.resource_kind='import' AND h.resource_id=i.id)
		  OR (h.resource_kind='observation' AND EXISTS(
		   SELECT 1 FROM `+a.table("observations")+` held
		   WHERE held.workspace_id=i.workspace_id AND held.run_id=i.run_id
		   AND held.id=h.resource_id)))),'')
		FROM `+a.table("imports")+` i
		WHERE i.workspace_id=$1 AND i.imported_at<=$2 AND i.state IN ('succeeded','failed')
		AND i.evidence_availability='available'
		ORDER BY i.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item retentionSnapshotItem
		var runID, digest, key, state, decisions, holds string
		var decision, assessment bool
		var references int
		if err = rows.Scan(&item.ResourceID, &runID, &item.ObservedAt, &item.SizeBytes,
			&digest, &key, &state, &references, &decision, &assessment, &decisions, &holds); err != nil {
			return err
		}
		item.Class, item.ResourceKind, item.Action = "raw-report", "import", "expire-raw-report"
		item.ObservedAt = item.ObservedAt.UTC()
		extra := ""
		if assessment {
			extra = "assessment-reference"
		}
		item.ProtectedReasons = retentionReasons(holds != "", decision, references > 1, extra)
		item.binding = strings.Join([]string{item.ResourceID, runID, timeString(item.ObservedAt),
			intString(item.SizeBytes), digest, key, state, intString(int64(references)),
			boolString(assessment), decisions, holds}, "|")
		addRetentionItem(snapshot, item)
		if len(snapshot.Items) > retentionPreviewLimit {
			return errTooLarge
		}
	}
	return rows.Err()
}

func (a *Application) appendAudit(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT e.id,e.created_at,
		octet_length(e.before_state::text)+octet_length(e.after_state::text)+octet_length(e.rationale),
		c.state,c.revision,
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=e.workspace_id AND h.resource_kind='correlation-event'
		 AND h.resource_id=e.id AND h.released_at IS NULL),'')
		FROM `+a.table("finding_correlation_events")+` e
		JOIN `+a.table("finding_correlations")+` c
		ON c.workspace_id=e.workspace_id AND c.id=e.correlation_id
		WHERE e.workspace_id=$1 AND e.created_at<=$2 AND e.detail_availability='available'
		ORDER BY e.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item retentionSnapshotItem
		var state, holds string
		var revision int64
		if err = rows.Scan(&item.ResourceID, &item.ObservedAt, &item.SizeBytes,
			&state, &revision, &holds); err != nil {
			return err
		}
		item.Class, item.ResourceKind, item.Action = "audit", "correlation-event", "archive-audit"
		item.ObservedAt = item.ObservedAt.UTC()
		item.ProtectedReasons = retentionReasons(holds != "", false, false, "")
		if state == "active" {
			item.ProtectedReasons = append(item.ProtectedReasons, "active-correlation")
		}
		item.binding = strings.Join([]string{item.ResourceID, timeString(item.ObservedAt),
			intString(item.SizeBytes), state, intString(revision), holds}, "|")
		addRetentionItem(snapshot, item)
		if len(snapshot.Items) > retentionPreviewLimit {
			return errTooLarge
		}
	}
	return rows.Err()
}

func (a *Application) appendArchivedEvidence(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT o.id,i.id,o.archived_at,o.archive_size,o.evidence_revision,
		f.id,f.decision_revision,f.evidence_revision,
		(f.owner_id IS NOT NULL OR f.workflow_state<>'open' OR f.disposition<>'none'
		 OR EXISTS(SELECT 1 FROM `+a.table("notes")+` n
		  WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		 OR EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` cm
		  JOIN `+a.table("finding_correlations")+` c
		  ON c.workspace_id=cm.workspace_id AND c.id=cm.correlation_id AND c.state='active'
		  WHERE cm.workspace_id=f.workspace_id AND cm.finding_id=f.id AND cm.released_at IS NULL)),
		COALESCE((SELECT string_agg(h.id||':'||h.revision::text,',' ORDER BY h.id)
		 FROM `+a.table("retention_holds")+` h
		 WHERE h.workspace_id=o.workspace_id AND h.released_at IS NULL
		 AND ((h.resource_kind='observation' AND h.resource_id=o.id)
		  OR (h.resource_kind='import' AND h.resource_id=i.id))),'')
		,EXISTS(SELECT 1 FROM `+a.table("assessment_previews")+` p
		 WHERE p.workspace_id=o.workspace_id AND p.observation_id=o.id)
		FROM `+a.table("observations")+` o
		JOIN `+a.table("imports")+` i ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
		JOIN `+a.table("findings")+` f ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		WHERE o.workspace_id=$1 AND o.archived_at<=$2 AND o.evidence_availability='archived'
		ORDER BY o.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item retentionSnapshotItem
		var importID, findingID, holds string
		var revision, decisionRevision, evidenceRevision int64
		var decision, assessment bool
		if err = rows.Scan(&item.ResourceID, &importID, &item.ObservedAt, &item.SizeBytes, &revision,
			&findingID, &decisionRevision, &evidenceRevision, &decision, &holds, &assessment); err != nil {
			return err
		}
		item.Class, item.ResourceKind, item.Action = "archived-evidence", "observation", "expire-archive"
		item.ObservedAt = item.ObservedAt.UTC()
		item.ProtectedReasons = retentionReasons(holds != "", decision, false, "")
		if assessment {
			item.ProtectedReasons = append(item.ProtectedReasons, "assessment-reference")
		}
		item.binding = strings.Join([]string{item.ResourceID, importID, findingID, timeString(item.ObservedAt),
			intString(item.SizeBytes), intString(revision), intString(decisionRevision),
			intString(evidenceRevision), holds, boolString(assessment)}, "|")
		addRetentionItem(snapshot, item)
		if len(snapshot.Items) > retentionPreviewLimit {
			return errTooLarge
		}
	}
	return rows.Err()
}

func (a *Application) appendOrphanArchives(ctx context.Context, db retentionDB, workspace string,
	cutoff time.Time, snapshot *retentionSnapshot) error {
	rows, err := db.Query(ctx, `SELECT p.id,p.object_key,p.object_digest,p.object_size,p.state,
		p.revision,p.updated_at,
		EXISTS(SELECT 1 FROM `+a.table("observations")+` o
		 WHERE o.workspace_id=p.workspace_id AND o.archive_key=p.object_key)
		OR EXISTS(SELECT 1 FROM `+a.table("finding_correlation_events")+` e
		 WHERE e.workspace_id=p.workspace_id AND e.archive_key=p.object_key)
		OR EXISTS(SELECT 1 FROM `+a.table("finding_decision_events")+` e
		 WHERE e.workspace_id=p.workspace_id AND e.archive_key=p.object_key)
		OR EXISTS(SELECT 1 FROM `+a.table("notification_policy_revisions")+` e
		 WHERE e.workspace_id=p.workspace_id AND e.archive_key=p.object_key)
		OR EXISTS(SELECT 1 FROM `+a.table("finding_change_events")+` e
		 WHERE e.workspace_id=p.workspace_id AND e.archive_key=p.object_key)
		OR EXISTS(SELECT 1 FROM `+a.table("notification_policy_events")+` e
		 WHERE e.workspace_id=p.workspace_id AND e.archive_key=p.object_key)
		FROM `+a.table("archive_publications")+` p
		WHERE p.workspace_id=$1 AND p.state IN ('publishing','orphan') AND p.updated_at<=$2
		ORDER BY p.id LIMIT $3`, workspace, cutoff, retentionPreviewLimit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item retentionSnapshotItem
		var key, digest, state string
		var revision int64
		var referenced bool
		if err = rows.Scan(&item.ResourceID, &key, &digest, &item.SizeBytes,
			&state, &revision, &item.ObservedAt, &referenced); err != nil {
			return err
		}
		item.Class, item.ResourceKind, item.Action = "orphan-archive", "archive-object", "delete-orphan"
		item.ObservedAt = item.ObservedAt.UTC()
		item.ObjectKey, item.ObjectDigest, item.ObjectRevision = &key, &digest, &revision
		if referenced {
			item.ProtectedReasons = []string{"archive-reference"}
		} else {
			item.ProtectedReasons = []string{}
		}
		item.binding = strings.Join([]string{item.ResourceID, key, digest, intString(item.SizeBytes),
			state, intString(revision), timeString(item.ObservedAt), boolString(referenced)}, "|")
		addRetentionItem(snapshot, item)
		if len(snapshot.Items) > retentionPreviewLimit {
			return errTooLarge
		}
	}
	return rows.Err()
}

func timeString(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func intString(value int64) string      { return strconv.FormatInt(value, 10) }
func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func retentionSnapshotDigest(snapshot retentionSnapshot) (string, error) {
	type bindingPolicy struct {
		Revision             int64
		HotHistoryDays       int
		RawReportDays        int
		ArchivedEvidenceDays int
		AuditDays            int
	}
	type bindingItem struct {
		RetentionPreviewItem
		Binding string
	}
	items := make([]bindingItem, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items = append(items, bindingItem{RetentionPreviewItem: item.RetentionPreviewItem, Binding: item.binding})
	}
	data, err := json.Marshal(struct {
		Policy bindingPolicy
		Items  []bindingItem
	}{
		Policy: bindingPolicy{
			Revision: snapshot.Policy.Revision, HotHistoryDays: snapshot.Policy.HotHistoryDays,
			RawReportDays:        snapshot.Policy.RawReportDays,
			ArchivedEvidenceDays: snapshot.Policy.ArchivedEvidenceDays, AuditDays: snapshot.Policy.AuditDays,
		},
		Items: items,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (a *Application) buildRetentionSnapshot(ctx context.Context, db retentionDB,
	workspace string, now time.Time) (retentionSnapshot, error) {
	policy, err := a.readRetentionPolicy(ctx, db, workspace)
	if err != nil {
		return retentionSnapshot{}, err
	}
	snapshot := retentionSnapshot{Policy: policy, Summaries: newRetentionSummaries(policy)}
	if err = a.appendHotHistory(ctx, db, workspace,
		now.AddDate(0, 0, -policy.HotHistoryDays), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	if err = a.appendRawReports(ctx, db, workspace,
		now.AddDate(0, 0, -policy.RawReportDays), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	if err = a.appendArchivedEvidence(ctx, db, workspace,
		now.AddDate(0, 0, -policy.ArchivedEvidenceDays), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	if err = a.appendAudit(ctx, db, workspace,
		now.AddDate(0, 0, -policy.AuditDays), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	if err = a.appendHistoryRetention(ctx, db, workspace,
		now.AddDate(0, 0, -policy.AuditDays), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	if err = a.appendOrphanArchives(ctx, db, workspace,
		now.Add(-orphanArchiveGrace), &snapshot); err != nil {
		return retentionSnapshot{}, err
	}
	slices.SortFunc(snapshot.Items, func(left, right retentionSnapshotItem) int {
		rank := map[string]int{
			"hot-history": 0, "archived-evidence": 1, "raw-report": 2, "audit": 3, "orphan-archive": 4,
		}
		if rank[left.Class] != rank[right.Class] {
			return rank[left.Class] - rank[right.Class]
		}
		if left.ResourceKind != right.ResourceKind {
			return strings.Compare(left.ResourceKind, right.ResourceKind)
		}
		if isHistoryRetentionKind(left.ResourceKind) && !left.ObservedAt.Equal(right.ObservedAt) {
			if left.ObservedAt.Before(right.ObservedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(left.ResourceID, right.ResourceID)
	})
	snapshot.Digest, err = retentionSnapshotDigest(snapshot)
	return snapshot, err
}

func publicRetentionItems(items []retentionSnapshotItem) []RetentionPreviewItem {
	result := make([]RetentionPreviewItem, 0, len(items))
	for _, item := range items {
		result = append(result, item.RetentionPreviewItem)
	}
	return result
}

func (a *Application) createRetentionPreview(w http.ResponseWriter, r *http.Request,
	workspace, actor string) error {
	var input struct{}
	if err := a.decode(w, r, &input, 1024); err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := a.config.Now().UTC()
	snapshot, err := a.buildRetentionSnapshot(r.Context(), tx, workspace, now)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(snapshot.Summaries)
	if err != nil {
		return err
	}
	preview := RetentionPreview{
		ID: newID(), WorkspaceID: workspace, Revision: 1, State: "ready",
		PolicyRevision: snapshot.Policy.Revision, SnapshotDigest: snapshot.Digest,
		CreatedBy: actor, CreatedAt: now, ExpiresAt: now.Add(retentionPreviewTTL),
		Summaries: snapshot.Summaries, Items: publicRetentionItems(snapshot.Items),
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_previews")+`
		(id,workspace_id,revision,state,policy_revision,snapshot_digest,summary,created_by,created_at,expires_at)
		VALUES($1,$2,1,'ready',$3,$4,$5,$6,$7,$8)`,
		preview.ID, workspace, preview.PolicyRevision, preview.SnapshotDigest, summary,
		actor, preview.CreatedAt, preview.ExpiresAt); err != nil {
		return err
	}
	for ordinal, item := range preview.Items {
		reasons, err := json.Marshal(item.ProtectedReasons)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_preview_items")+`
			(preview_id,workspace_id,ordinal,class,resource_kind,resource_id,action,observed_at,size_bytes,
			 protected_reasons,object_key,object_digest,object_revision)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			preview.ID, workspace, ordinal, item.Class, item.ResourceKind, item.ResourceID,
			item.Action, item.ObservedAt, item.SizeBytes, reasons,
			item.ObjectKey, item.ObjectDigest, item.ObjectRevision); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"retentionPreview": preview})
	return nil
}

func (a *Application) loadRetentionPreview(ctx context.Context, db retentionDB,
	workspace, id string, lock bool) (RetentionPreview, error) {
	var preview RetentionPreview
	var summary []byte
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := db.QueryRow(ctx, `SELECT id,workspace_id,revision,state,policy_revision,snapshot_digest,summary,
		created_by,created_at,expires_at,approved_by,approved_at,approval_rationale
		FROM `+a.table("retention_previews")+` WHERE workspace_id=$1 AND id=$2`+suffix,
		workspace, id).Scan(&preview.ID, &preview.WorkspaceID, &preview.Revision, &preview.State,
		&preview.PolicyRevision, &preview.SnapshotDigest, &summary, &preview.CreatedBy,
		&preview.CreatedAt, &preview.ExpiresAt, &preview.ApprovedBy, &preview.ApprovedAt,
		&preview.ApprovalRationale)
	if err != nil {
		return preview, err
	}
	preview.CreatedAt, preview.ExpiresAt = preview.CreatedAt.UTC(), preview.ExpiresAt.UTC()
	if preview.ApprovedAt != nil {
		value := preview.ApprovedAt.UTC()
		preview.ApprovedAt = &value
	}
	if err = json.Unmarshal(summary, &preview.Summaries); err != nil {
		return preview, err
	}
	rows, err := db.Query(ctx, `SELECT class,resource_kind,resource_id,action,observed_at,size_bytes,
		protected_reasons,object_key,object_digest,object_revision
		FROM `+a.table("retention_preview_items")+`
		WHERE workspace_id=$1 AND preview_id=$2 ORDER BY ordinal`, workspace, id)
	if err != nil {
		return preview, err
	}
	defer rows.Close()
	for rows.Next() {
		var item RetentionPreviewItem
		var reasons []byte
		if err = rows.Scan(&item.Class, &item.ResourceKind, &item.ResourceID, &item.Action,
			&item.ObservedAt, &item.SizeBytes, &reasons,
			&item.ObjectKey, &item.ObjectDigest, &item.ObjectRevision); err != nil {
			return preview, err
		}
		item.ObservedAt = item.ObservedAt.UTC()
		if err = json.Unmarshal(reasons, &item.ProtectedReasons); err != nil {
			return preview, err
		}
		preview.Items = append(preview.Items, item)
	}
	return preview, rows.Err()
}

func (a *Application) getRetentionPreview(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	preview, err := a.loadRetentionPreview(r.Context(), a.pool, workspace, id, false)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionPreview": preview})
	return nil
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && value == strings.ToLower(value)
}

func retentionApprovalDigest(workspace, preview, actor, rationale, key, snapshot string, revision int64) (string, error) {
	data, err := json.Marshal(struct {
		Workspace, Preview, Actor, Rationale, Key, Snapshot string
		Revision                                            int64
	}{workspace, preview, actor, rationale, key, snapshot, revision})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (a *Application) approveRetentionPreview(w http.ResponseWriter, r *http.Request,
	workspace, actor, id string) error {
	var input struct {
		Revision       int64  `json:"revision"`
		SnapshotDigest string `json:"snapshotDigest"`
		Rationale      string `json:"rationale"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if input.Revision < 1 || !validDigest(input.SnapshotDigest) ||
		!validText(input.Rationale, 8192) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	binding, err := retentionApprovalDigest(workspace, id, actor, input.Rationale,
		input.IdempotencyKey, input.SnapshotDigest, input.Revision)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/retention-approval/"+workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	var priorID, priorBinding string
	err = tx.QueryRow(r.Context(), `SELECT id,approval_binding_digest
		FROM `+a.table("retention_previews")+`
		WHERE workspace_id=$1 AND approval_idempotency_key=$2`,
		workspace, input.IdempotencyKey).Scan(&priorID, &priorBinding)
	if err == nil {
		if priorID != id || priorBinding != binding {
			return errConflict
		}
		preview, err := a.loadRetentionPreview(r.Context(), tx, workspace, id, false)
		if err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"retentionPreview": preview})
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	preview, err := a.loadRetentionPreview(r.Context(), tx, workspace, id, true)
	if err != nil {
		return err
	}
	now := a.config.Now().UTC()
	if preview.State != "ready" || preview.Revision != input.Revision ||
		preview.SnapshotDigest != input.SnapshotDigest {
		return errConflict
	}
	if !preview.ExpiresAt.After(now) {
		if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("retention_previews")+`
			SET state='stale',revision=revision+1
			WHERE workspace_id=$1 AND id=$2 AND state='ready'`, workspace, id); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return errConflict
	}
	current, err := a.buildRetentionSnapshot(r.Context(), tx, workspace, now)
	if err != nil {
		return err
	}
	if current.Policy.Revision != preview.PolicyRevision || current.Digest != preview.SnapshotDigest {
		if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("retention_previews")+`
			SET state='stale',revision=revision+1
			WHERE workspace_id=$1 AND id=$2 AND state='ready'`, workspace, id); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return errConflict
	}
	result, err := tx.Exec(r.Context(), `UPDATE `+a.table("retention_previews")+`
		SET state='approved',revision=revision+1,approved_by=$4,approved_at=$5,
			approval_rationale=$6,approval_idempotency_key=$7,approval_binding_digest=$8
		WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND state='ready'`,
		workspace, id, input.Revision, actor, now, input.Rationale, input.IdempotencyKey, binding)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errConflict
	}
	preview, err = a.loadRetentionPreview(r.Context(), tx, workspace, id, false)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"retentionPreview": preview})
	return nil
}
