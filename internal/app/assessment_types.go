package app

import (
	"time"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
)

const (
	assessmentPromptRevision = "finding-validity-reviewed-context/v1"
	assessmentContextOrigin  = "user-reviewed-derived"
	assessmentContextLimit   = 32768
)

type AssessmentBinding struct {
	WorkspaceID          string `json:"workspaceId"`
	FindingID            string `json:"findingId"`
	ObservationID        string `json:"observationId"`
	SourceEvidenceDigest string `json:"sourceEvidenceDigest"`
	RequestedBy          string `json:"requestedBy"`
	ProfileID            string `json:"profileId"`
	ProfileRevision      string `json:"profileRevision"`
	PolicyRevision       string `json:"policyRevision"`
	GrantID              string `json:"grantId"`
	Destination          string `json:"destination"`
	Family               string `json:"family"`
	Model                string `json:"model"`
	Deployment           string `json:"deployment"`
	Task                 string `json:"task"`
	DataClass            string `json:"dataClass"`
	PromptRevision       string `json:"promptRevision"`
	ContextRef           string `json:"contextRef"`
	ContextDigest        string `json:"contextDigest"`
	ContextOrigin        string `json:"contextOrigin"`
	Context              string `json:"context"`
}

type AssessmentPreview struct {
	AssessmentBinding
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AssessmentUsage struct {
	Known             bool  `json:"known"`
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`
	CacheWriteTokens  int64 `json:"cacheWriteTokens"`
}

type AssessmentJob struct {
	AssessmentBinding
	ID                string                `json:"id"`
	PreviewID         string                `json:"previewId"`
	IdempotencyKey    string                `json:"idempotencyKey"`
	Scope             string                `json:"scope"`
	ConsentExpiresAt  time.Time             `json:"consentExpiresAt"`
	State             string                `json:"state"`
	Attempts          int                   `json:"attempts"`
	DispatchState     string                `json:"dispatchState"`
	AdvisoryOnly      bool                  `json:"advisoryOnly"`
	CreatedAt         time.Time             `json:"createdAt"`
	DispatchStartedAt *time.Time            `json:"dispatchStartedAt"`
	CompletedAt       *time.Time            `json:"completedAt"`
	Result            *providers.Assessment `json:"result"`
	Failure           *Failure              `json:"failure"`
	Usage             AssessmentUsage       `json:"usage"`
	RequestID         string                `json:"requestId"`
	RequestedModel    string                `json:"requestedModel"`
	ReturnedModel     string                `json:"returnedModel"`
	StopReason        string                `json:"stopReason"`
	RetryAfterMillis  int64                 `json:"retryAfterMillis"`
}

type assessmentJobRecord struct {
	AssessmentJob
	workerID   *string
	fence      int64
	leaseUntil *time.Time
}

const assessmentBindingColumns = `p.workspace_id,p.finding_id,p.observation_id,p.source_evidence_digest,
	p.requested_by,p.profile_id,p.profile_revision,p.policy_revision,p.grant_id,p.destination,
	p.family,p.model,p.deployment,p.task,p.data_class,p.prompt_revision,p.context_ref,
	p.context_digest,p.context_origin,p.context`

func assessmentBindingDest(b *AssessmentBinding) []any {
	return []any{&b.WorkspaceID, &b.FindingID, &b.ObservationID, &b.SourceEvidenceDigest,
		&b.RequestedBy, &b.ProfileID, &b.ProfileRevision, &b.PolicyRevision, &b.GrantID,
		&b.Destination, &b.Family, &b.Model, &b.Deployment, &b.Task, &b.DataClass,
		&b.PromptRevision, &b.ContextRef, &b.ContextDigest, &b.ContextOrigin, &b.Context}
}

func scanAssessmentPreview(row pgx.Row) (AssessmentPreview, error) {
	var preview AssessmentPreview
	dest := append(assessmentBindingDest(&preview.AssessmentBinding),
		&preview.ID, &preview.CreatedAt, &preview.ExpiresAt)
	err := row.Scan(dest...)
	return preview, err
}

func (db *database) assessmentPreviewSelect() string {
	return `SELECT ` + assessmentBindingColumns + `,p.id,p.created_at,p.expires_at
		FROM ` + db.table("assessment_previews") + ` p`
}

func (db *database) assessmentJobSelect() string {
	return `SELECT ` + assessmentBindingColumns + `,j.id,j.preview_id,j.idempotency_key,j.scope,
		j.consent_expires_at,j.state,j.attempts,j.dispatch_state,j.created_at,j.dispatch_started_at,
		j.completed_at,j.result,j.failure,j.usage,j.request_id,j.returned_model,j.stop_reason,
		j.retry_after_millis,j.worker_id,j.fence,j.lease_until
		FROM ` + db.table("assessment_jobs") + ` j JOIN ` + db.table("assessment_previews") + ` p
		ON p.workspace_id=j.workspace_id AND p.id=j.preview_id`
}

func scanAssessmentJob(row pgx.Row) (assessmentJobRecord, error) {
	var job assessmentJobRecord
	dest := append(assessmentBindingDest(&job.AssessmentBinding),
		&job.ID, &job.PreviewID, &job.IdempotencyKey, &job.Scope, &job.ConsentExpiresAt,
		&job.State, &job.Attempts, &job.DispatchState, &job.CreatedAt, &job.DispatchStartedAt,
		&job.CompletedAt, &job.Result, &job.Failure, &job.Usage, &job.RequestID,
		&job.ReturnedModel, &job.StopReason, &job.RetryAfterMillis,
		&job.workerID, &job.fence, &job.leaseUntil)
	err := row.Scan(dest...)
	job.AdvisoryOnly, job.RequestedModel = true, job.Model
	return job, err
}
