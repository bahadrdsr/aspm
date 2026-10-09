package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	verificationMethod        = "deterministic-evidence"
	verificationFixtureSchema = "aspm.synthetic-fixture/v1"
	verificationFixtureLimit  = 64 << 10
)

type VerificationEvidence struct {
	ID                      string    `json:"id"`
	WorkspaceID             string    `json:"workspaceId"`
	FindingID               string    `json:"findingId"`
	SubmittedBy             string    `json:"submittedBy"`
	Method                  string    `json:"method"`
	Schema                  string    `json:"schema"`
	EnvironmentID           string    `json:"environmentId"`
	ScopeRevision           string    `json:"scopeRevision"`
	FindingEvidenceRevision int64     `json:"findingEvidenceRevision"`
	Digest                  string    `json:"digest"`
	SizeBytes               int64     `json:"sizeBytes"`
	CreatedAt               time.Time `json:"createdAt"`
}

type VerificationApproval struct {
	ID                      string     `json:"id"`
	WorkspaceID             string     `json:"workspaceId"`
	FindingID               string     `json:"findingId"`
	EvidenceID              string     `json:"evidenceId"`
	ApprovedBy              string     `json:"approvedBy"`
	Method                  string     `json:"method"`
	EnvironmentID           string     `json:"environmentId"`
	ScopeRevision           string     `json:"scopeRevision"`
	FindingEvidenceRevision int64      `json:"findingEvidenceRevision"`
	EvidenceDigest          string     `json:"evidenceDigest"`
	Rationale               string     `json:"rationale"`
	CreatedAt               time.Time  `json:"createdAt"`
	ExpiresAt               time.Time  `json:"expiresAt"`
	RevokedAt               *time.Time `json:"revokedAt"`
	RevokedBy               *string    `json:"revokedBy"`
	RevocationRationale     *string    `json:"revocationRationale"`
	Current                 bool       `json:"current"`
}

type VerificationResult struct {
	Method         string `json:"method"`
	EnvironmentID  string `json:"environmentId"`
	ScopeRevision  string `json:"scopeRevision"`
	EvidenceID     string `json:"evidenceId"`
	EvidenceDigest string `json:"evidenceDigest"`
	Outcome        string `json:"outcome"`
	CloseFinding   bool   `json:"closeFinding"`
	FalsePositive  bool   `json:"falsePositive"`
}

type VerificationJob struct {
	ID                      string              `json:"id"`
	WorkspaceID             string              `json:"workspaceId"`
	FindingID               string              `json:"findingId"`
	ApprovalID              string              `json:"approvalId"`
	EvidenceID              string              `json:"evidenceId"`
	RequestedBy             string              `json:"requestedBy"`
	Method                  string              `json:"method"`
	EnvironmentID           string              `json:"environmentId"`
	ScopeRevision           string              `json:"scopeRevision"`
	FindingEvidenceRevision int64               `json:"findingEvidenceRevision"`
	EvidenceDigest          string              `json:"evidenceDigest"`
	State                   string              `json:"state"`
	CreatedAt               time.Time           `json:"createdAt"`
	CompletedAt             *time.Time          `json:"completedAt"`
	Failure                 *Failure            `json:"failure"`
	Result                  *VerificationResult `json:"result"`
}

type verificationJobRecord struct {
	VerificationJob
	IdempotencyKey string
	WorkerID       string
	Fence          int64
	Attempts       int
	LeaseUntil     *time.Time
}

func verificationEvidenceColumns(alias string) string {
	fields := []string{"id", "workspace_id", "finding_id", "submitted_by", "method", "fixture_schema",
		"environment_id", "scope_revision", "finding_evidence_revision", "content_digest", "content_size", "created_at"}
	if alias != "" {
		for index := range fields {
			fields[index] = alias + "." + fields[index]
		}
	}
	return joinColumns(fields)
}

func scanVerificationEvidence(row pgx.Row) (VerificationEvidence, error) {
	var value VerificationEvidence
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.FindingID, &value.SubmittedBy, &value.Method,
		&value.Schema, &value.EnvironmentID, &value.ScopeRevision, &value.FindingEvidenceRevision,
		&value.Digest, &value.SizeBytes, &value.CreatedAt)
	return value, err
}

func verificationApprovalColumns(alias string, currentRevision string) string {
	fields := []string{"id", "workspace_id", "finding_id", "evidence_id", "approved_by", "method",
		"environment_id", "scope_revision", "finding_evidence_revision", "evidence_digest", "rationale",
		"created_at", "expires_at", "revoked_at", "revoked_by", "revocation_rationale"}
	if alias != "" {
		for index := range fields {
			fields[index] = alias + "." + fields[index]
		}
	}
	return joinColumns(fields) + "," + alias + ".revoked_at IS NULL AND " + alias +
		".expires_at>$1 AND " + alias + ".finding_evidence_revision=" + currentRevision
}

func scanVerificationApproval(row pgx.Row) (VerificationApproval, error) {
	var value VerificationApproval
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.FindingID, &value.EvidenceID, &value.ApprovedBy,
		&value.Method, &value.EnvironmentID, &value.ScopeRevision, &value.FindingEvidenceRevision,
		&value.EvidenceDigest, &value.Rationale, &value.CreatedAt, &value.ExpiresAt, &value.RevokedAt,
		&value.RevokedBy, &value.RevocationRationale, &value.Current)
	return value, err
}

func verificationJobColumns(alias string) string {
	fields := []string{"id", "workspace_id", "finding_id", "approval_id", "evidence_id", "requested_by",
		"method", "environment_id", "scope_revision", "finding_evidence_revision", "evidence_digest",
		"state", "created_at", "completed_at", "failure_code", "failure_message", "outcome",
		"close_finding", "false_positive", "idempotency_key", "worker_id", "fence", "attempts", "lease_until"}
	if alias != "" {
		for index := range fields {
			fields[index] = alias + "." + fields[index]
		}
	}
	return joinColumns(fields)
}

func scanVerificationJob(row pgx.Row) (verificationJobRecord, error) {
	var record verificationJobRecord
	var failureCode, failureMessage, outcome, worker *string
	var closeFinding, falsePositive bool
	err := row.Scan(
		&record.ID, &record.WorkspaceID, &record.FindingID, &record.ApprovalID, &record.EvidenceID,
		&record.RequestedBy, &record.Method, &record.EnvironmentID, &record.ScopeRevision,
		&record.FindingEvidenceRevision, &record.EvidenceDigest, &record.State, &record.CreatedAt,
		&record.CompletedAt, &failureCode, &failureMessage, &outcome, &closeFinding, &falsePositive,
		&record.IdempotencyKey, &worker, &record.Fence, &record.Attempts, &record.LeaseUntil,
	)
	if err != nil {
		return record, err
	}
	if worker != nil {
		record.WorkerID = *worker
	}
	if failureCode != nil && failureMessage != nil {
		record.Failure = &Failure{Code: *failureCode, Message: *failureMessage, Retryable: false}
	}
	if outcome != nil {
		record.Result = &VerificationResult{
			Method: record.Method, EnvironmentID: record.EnvironmentID, ScopeRevision: record.ScopeRevision,
			EvidenceID: record.EvidenceID, EvidenceDigest: record.EvidenceDigest, Outcome: *outcome,
			CloseFinding: closeFinding, FalsePositive: falsePositive,
		}
	}
	return record, nil
}

func joinColumns(fields []string) string {
	result := ""
	for index, field := range fields {
		if index > 0 {
			result += ","
		}
		result += field
	}
	return result
}

func (a *Application) createVerificationEvidence(w http.ResponseWriter, r *http.Request,
	workspace, finding, userID string) error {
	var input struct {
		Method        string `json:"method"`
		EnvironmentID string `json:"environmentId"`
		ScopeRevision string `json:"scopeRevision"`
		Fixture       struct {
			Schema        string `json:"schema"`
			EnvironmentID string `json:"environmentId"`
			Condition     *bool  `json:"condition"`
		} `json:"fixture"`
	}
	if err := a.decode(w, r, &input, verificationFixtureLimit); err != nil {
		return err
	}
	if input.Method != verificationMethod || input.Fixture.Schema != verificationFixtureSchema ||
		input.Fixture.Condition == nil || input.EnvironmentID != input.Fixture.EnvironmentID ||
		!validText(input.EnvironmentID, 256) || !validText(input.ScopeRevision, 256) {
		return errInvalid
	}
	document := struct {
		Schema        string `json:"schema"`
		EnvironmentID string `json:"environmentId"`
		Condition     bool   `json:"condition"`
	}{input.Fixture.Schema, input.Fixture.EnvironmentID, *input.Fixture.Condition}
	content, err := json.Marshal(document)
	if err != nil || len(content) < 1 || len(content) > verificationFixtureLimit {
		return errInvalid
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var revision int64
	if err = tx.QueryRow(r.Context(), `SELECT evidence_revision FROM `+a.table("findings")+
		` WHERE workspace_id=$1 AND id=$2 FOR SHARE`, workspace, finding).Scan(&revision); err != nil {
		return err
	}
	value, err := scanVerificationEvidence(tx.QueryRow(r.Context(), `INSERT INTO `+
		a.table("verification_evidence")+`
		(id,workspace_id,finding_id,submitted_by,method,fixture_schema,environment_id,scope_revision,
		 finding_evidence_revision,content,content_digest,content_size,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING `+verificationEvidenceColumns(""), newID(), workspace, finding, userID,
		verificationMethod, verificationFixtureSchema, input.EnvironmentID, input.ScopeRevision, revision,
		content, digest, len(content), a.now().UTC()))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"dataOrigin": "live", "evidence": value})
	return nil
}

func (a *Application) createVerificationApproval(w http.ResponseWriter, r *http.Request,
	workspace, finding, userID string) error {
	var input struct {
		EvidenceID string `json:"evidenceId"`
		Rationale  string `json:"rationale"`
		ExpiresAt  string `json:"expiresAt"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
	now := a.now().UTC()
	if err != nil || !validID(input.EvidenceID) || !validText(input.Rationale, 8192) ||
		!expires.After(now) || expires.After(now.Add(24*time.Hour)) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var evidence VerificationEvidence
	var currentRevision int64
	evidence, err = scanVerificationEvidence(tx.QueryRow(r.Context(), `SELECT `+
		verificationEvidenceColumns("e")+` FROM `+a.table("verification_evidence")+` e
		WHERE e.workspace_id=$1 AND e.finding_id=$2 AND e.id=$3`,
		workspace, finding, input.EvidenceID))
	if err != nil {
		return err
	}
	if err = tx.QueryRow(r.Context(), `SELECT evidence_revision FROM `+a.table("findings")+
		` WHERE workspace_id=$1 AND id=$2 FOR SHARE`, workspace, finding).Scan(&currentRevision); err != nil {
		return err
	}
	if evidence.FindingEvidenceRevision != currentRevision {
		return errConflict
	}
	var value VerificationApproval
	err = tx.QueryRow(r.Context(), `INSERT INTO `+a.table("verification_approvals")+`
		(id,workspace_id,finding_id,evidence_id,approved_by,method,environment_id,scope_revision,
		 finding_evidence_revision,evidence_digest,rationale,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id,workspace_id,finding_id,evidence_id,approved_by,method,environment_id,scope_revision,
		 finding_evidence_revision,evidence_digest,rationale,created_at,expires_at,revoked_at,revoked_by,
		 revocation_rationale,true`,
		newID(), workspace, finding, evidence.ID, userID, evidence.Method, evidence.EnvironmentID,
		evidence.ScopeRevision, evidence.FindingEvidenceRevision, evidence.Digest, input.Rationale,
		now, expires.UTC()).Scan(
		&value.ID, &value.WorkspaceID, &value.FindingID, &value.EvidenceID, &value.ApprovedBy,
		&value.Method, &value.EnvironmentID, &value.ScopeRevision, &value.FindingEvidenceRevision,
		&value.EvidenceDigest, &value.Rationale, &value.CreatedAt, &value.ExpiresAt, &value.RevokedAt,
		&value.RevokedBy, &value.RevocationRationale, &value.Current)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"dataOrigin": "live", "approval": value})
	return nil
}

func (a *Application) revokeVerificationApproval(w http.ResponseWriter, r *http.Request,
	workspace, finding, approvalID, userID string) error {
	var input struct {
		Rationale string `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.Rationale, 8192) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var revoked *time.Time
	if err = tx.QueryRow(r.Context(), `SELECT revoked_at FROM `+a.table("verification_approvals")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3 FOR UPDATE`,
		workspace, finding, approvalID).Scan(&revoked); err != nil {
		return err
	}
	if revoked != nil {
		return errConflict
	}
	now := a.now().UTC()
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("verification_approvals")+`
		SET revoked_at=$4,revoked_by=$5,revocation_rationale=$6
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3`,
		workspace, finding, approvalID, now, userID, input.Rationale); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("verification_jobs")+`
		SET state='cancelled',completed_at=$4,failure_code='approval-revoked',
		 failure_message='Verification approval was revoked before completion',
		 worker_id=NULL,lease_until=NULL,fence=fence+1
		WHERE workspace_id=$1 AND finding_id=$2 AND approval_id=$3 AND state IN ('queued','processing')`,
		workspace, finding, approvalID, now); err != nil {
		return err
	}
	var value VerificationApproval
	err = tx.QueryRow(r.Context(), `SELECT `+verificationApprovalColumns("a", "f.evidence_revision")+`
		FROM `+a.table("verification_approvals")+` a JOIN `+a.table("findings")+` f
		ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
		WHERE a.workspace_id=$2 AND a.finding_id=$3 AND a.id=$4 FOR SHARE OF a,f`,
		now, workspace, finding, approvalID).Scan(
		&value.ID, &value.WorkspaceID, &value.FindingID, &value.EvidenceID, &value.ApprovedBy,
		&value.Method, &value.EnvironmentID, &value.ScopeRevision, &value.FindingEvidenceRevision,
		&value.EvidenceDigest, &value.Rationale, &value.CreatedAt, &value.ExpiresAt, &value.RevokedAt,
		&value.RevokedBy, &value.RevocationRationale, &value.Current)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "approval": value})
	return nil
}

func (a *Application) enqueueVerification(w http.ResponseWriter, r *http.Request,
	workspace, finding, userID string) error {
	var input struct {
		ApprovalID     string `json:"approvalId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.ApprovalID) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	existing, existingErr := scanVerificationJob(tx.QueryRow(r.Context(), `SELECT `+
		verificationJobColumns("j")+` FROM `+a.table("verification_jobs")+` j
		WHERE j.workspace_id=$1 AND j.idempotency_key=$2`, workspace, input.IdempotencyKey))
	if existingErr == nil {
		if existing.FindingID != finding || existing.ApprovalID != input.ApprovalID {
			return errConflict
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"dataOrigin": "live", "verification": existing.VerificationJob,
		})
		return nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return existingErr
	}
	now := a.now().UTC()
	var approval VerificationApproval
	err = tx.QueryRow(r.Context(), `SELECT `+verificationApprovalColumns("a", "f.evidence_revision")+`
		FROM `+a.table("verification_approvals")+` a JOIN `+a.table("findings")+` f
		ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
		WHERE a.workspace_id=$2 AND a.finding_id=$3 AND a.id=$4`,
		now, workspace, finding, input.ApprovalID).Scan(
		&approval.ID, &approval.WorkspaceID, &approval.FindingID, &approval.EvidenceID, &approval.ApprovedBy,
		&approval.Method, &approval.EnvironmentID, &approval.ScopeRevision, &approval.FindingEvidenceRevision,
		&approval.EvidenceDigest, &approval.Rationale, &approval.CreatedAt, &approval.ExpiresAt,
		&approval.RevokedAt, &approval.RevokedBy, &approval.RevocationRationale, &approval.Current)
	if err != nil {
		return err
	}
	if !approval.Current {
		return errConflict
	}
	result, err := tx.Exec(r.Context(), `INSERT INTO `+a.table("verification_jobs")+`
		(id,workspace_id,finding_id,approval_id,evidence_id,requested_by,method,environment_id,
		 scope_revision,finding_evidence_revision,evidence_digest,idempotency_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING`,
		newID(), workspace, finding, approval.ID, approval.EvidenceID, userID, approval.Method,
		approval.EnvironmentID, approval.ScopeRevision, approval.FindingEvidenceRevision,
		approval.EvidenceDigest, input.IdempotencyKey, now)
	if err != nil {
		return err
	}
	inserted := result.RowsAffected() == 1
	job, err := scanVerificationJob(tx.QueryRow(r.Context(), `SELECT `+verificationJobColumns("j")+
		` FROM `+a.table("verification_jobs")+` j WHERE j.workspace_id=$1 AND j.idempotency_key=$2`,
		workspace, input.IdempotencyKey))
	if err != nil {
		return err
	}
	if job.FindingID != finding || job.ApprovalID != input.ApprovalID {
		return errConflict
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"dataOrigin": "live", "verification": job.VerificationJob})
	return nil
}

func (a *Application) listVerificationEvidence(w http.ResponseWriter, r *http.Request,
	workspace, finding string) error {
	return a.listVerificationPage(w, r, workspace, finding, "verification_evidence")
}

func (a *Application) listVerificationApprovals(w http.ResponseWriter, r *http.Request,
	workspace, finding string) error {
	return a.listVerificationPage(w, r, workspace, finding, "verification_approvals")
}

func (a *Application) listVerificationJobs(w http.ResponseWriter, r *http.Request,
	workspace, finding string) error {
	return a.listVerificationPage(w, r, workspace, finding, "verification_jobs")
}

func (a *Application) listVerificationPage(w http.ResponseWriter, r *http.Request,
	workspace, finding, kind string) error {
	limit, cursor, err := reportExportPageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return err
	}
	defer rollback(tx)
	table := a.table(kind)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+table+
		` WHERE workspace_id=$1 AND finding_id=$2`, workspace, finding).Scan(&total); err != nil {
		return err
	}
	var items any
	var next *string
	switch kind {
	case "verification_evidence":
		rows, queryErr := tx.Query(r.Context(), `SELECT `+verificationEvidenceColumns("e")+` FROM `+table+` e
			WHERE e.workspace_id=$1 AND e.finding_id=$2 AND e.id>$3 ORDER BY e.id LIMIT $4`,
			workspace, finding, cursor, limit+1)
		if queryErr != nil {
			return queryErr
		}
		values := []VerificationEvidence{}
		for rows.Next() {
			value, scanErr := scanVerificationEvidence(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(values) > limit {
			values = values[:limit]
			next = &values[len(values)-1].ID
		}
		items = values
	case "verification_approvals":
		now := a.now().UTC()
		rows, queryErr := tx.Query(r.Context(), `SELECT `+verificationApprovalColumns("a", "f.evidence_revision")+`
			FROM `+table+` a JOIN `+a.table("findings")+` f
			ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
			WHERE a.workspace_id=$2 AND a.finding_id=$3 AND a.id>$4 ORDER BY a.id LIMIT $5`,
			now, workspace, finding, cursor, limit+1)
		if queryErr != nil {
			return queryErr
		}
		values := []VerificationApproval{}
		for rows.Next() {
			value, scanErr := scanVerificationApproval(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(values) > limit {
			values = values[:limit]
			next = &values[len(values)-1].ID
		}
		items = values
	default:
		rows, queryErr := tx.Query(r.Context(), `SELECT `+verificationJobColumns("j")+` FROM `+table+` j
			WHERE j.workspace_id=$1 AND j.finding_id=$2 AND j.id>$3 ORDER BY j.id LIMIT $4`,
			workspace, finding, cursor, limit+1)
		if queryErr != nil {
			return queryErr
		}
		values := []VerificationJob{}
		for rows.Next() {
			value, scanErr := scanVerificationJob(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			values = append(values, value.VerificationJob)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(values) > limit {
			values = values[:limit]
			next = &values[len(values)-1].ID
		}
		items = values
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dataOrigin": "live", "items": items, "total": total, "nextCursor": next,
	})
	return nil
}

func (a *Application) getVerificationJob(w http.ResponseWriter, r *http.Request,
	workspace, finding, id string) error {
	job, err := scanVerificationJob(a.pool.QueryRow(r.Context(), `SELECT `+verificationJobColumns("j")+
		` FROM `+a.table("verification_jobs")+` j
		WHERE j.workspace_id=$1 AND j.finding_id=$2 AND j.id=$3`, workspace, finding, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "verification": job.VerificationJob})
	return nil
}
