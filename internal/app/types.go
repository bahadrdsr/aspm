package app

import (
	"encoding/json"
	"time"

	"github.com/bahadrdsr/aspm/internal/parsers"
)

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type Asset struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspaceId"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Environment string   `json:"environment"`
	Criticality string   `json:"criticality"`
	Tags        []string `json:"tags"`
	OwnerID     *string  `json:"ownerId"`
}

type Scope struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Branch   string `json:"branch"`
}

type WorkItem struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	AssetName     string     `json:"assetName"`
	Severity      string     `json:"severity"`
	OwnerName     *string    `json:"ownerName"`
	WorkflowState string     `json:"workflowState"`
	SourceScanAt  *time.Time `json:"sourceScanAt"`
	CollectedAt   time.Time  `json:"collectedAt"`
	ImportedAt    time.Time  `json:"importedAt"`
}

type Finding struct {
	WorkItem
	AssetID                string              `json:"assetId"`
	WorkspaceID            string              `json:"workspaceId"`
	ScopeLabel             string              `json:"scopeLabel"`
	Description            string              `json:"description"`
	Remediation            string              `json:"remediation"`
	Evidence               FindingEvidence     `json:"evidence"`
	OwnerID                *string             `json:"ownerId"`
	SourceState            string              `json:"sourceState"`
	SourceFreshnessAt      *time.Time          `json:"sourceFreshnessAt"`
	Disposition            string              `json:"disposition"`
	AcceptedRiskExpiresAt  *time.Time          `json:"acceptedRiskExpiresAt"`
	RiskAcceptanceExpired  bool                `json:"riskAcceptanceExpired"`
	VerifiedResolution     bool                `json:"verifiedResolution"`
	DecisionRevision       int64               `json:"decisionRevision"`
	EvidenceRevision       int64               `json:"evidenceRevision"`
	Notes                  []Note              `json:"notes"`
	Observations           []Observation       `json:"observations"`
	NotesNextCursor        *string             `json:"notesNextCursor"`
	ObservationsNextCursor *string             `json:"observationsNextCursor"`
	Correlation            *FindingCorrelation `json:"correlation,omitempty"`
}

type FindingEvidence struct {
	Text              string `json:"text"`
	SourceLabel       string `json:"sourceLabel"`
	VerificationState string `json:"verificationState"`
}

type Note struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type FindingDecision struct {
	OwnerID               *string    `json:"ownerId"`
	WorkflowState         string     `json:"workflowState"`
	Disposition           string     `json:"disposition"`
	AcceptedRiskExpiresAt *time.Time `json:"acceptedRiskExpiresAt"`
}

type FindingCorrelationMember struct {
	FindingID        string          `json:"findingId"`
	SourceID         string          `json:"sourceId"`
	Title            string          `json:"title"`
	Severity         string          `json:"severity"`
	DecisionRevision int64           `json:"decisionRevision"`
	EvidenceRevision int64           `json:"evidenceRevision"`
	ObservationCount int             `json:"observationCount"`
	NoteCount        int             `json:"noteCount"`
	Decision         FindingDecision `json:"decision"`
	OriginalDecision FindingDecision `json:"originalDecision"`
}

type FindingCorrelationEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	ActorID   string    `json:"actorId"`
	Rationale string    `json:"rationale"`
	CreatedAt time.Time `json:"createdAt"`
}

type FindingCorrelation struct {
	ID               string                     `json:"id"`
	WorkspaceID      string                     `json:"workspaceId"`
	PrimaryFindingID string                     `json:"primaryFindingId"`
	State            string                     `json:"state"`
	Revision         int64                      `json:"revision"`
	Members          []FindingCorrelationMember `json:"members"`
	Events           []FindingCorrelationEvent  `json:"events"`
}

type FindingMergePreview struct {
	Primary   FindingCorrelationMember `json:"primary"`
	Other     FindingCorrelationMember `json:"other"`
	Conflicts []string                 `json:"conflicts"`
}

type FindingSplitPreview struct {
	Correlation FindingCorrelation       `json:"correlation"`
	Primary     FindingCorrelationMember `json:"primary"`
	Member      FindingCorrelationMember `json:"member"`
}

type Observation struct {
	ID                 string           `json:"id"`
	RunID              string           `json:"runId"`
	SourceID           string           `json:"sourceId"`
	ScanID             string           `json:"scanId"`
	Scope              Scope            `json:"scope"`
	SourceScanAt       *time.Time       `json:"sourceScanAt"`
	SourceFindingID    string           `json:"sourceFindingId"`
	SourceSeverity     string           `json:"sourceSeverity"`
	NormalizedSeverity string           `json:"normalizedSeverity"`
	SourceLocation     parsers.Location `json:"sourceLocation"`
	Impact             string           `json:"impact"`
	Remediation        string           `json:"remediation"`
	Unmapped           map[string]any   `json:"unmapped"`
	EvidenceDigest     string           `json:"evidenceDigest"`
}

type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
	Retryable bool   `json:"retryable"`
}

type Import struct {
	ID               string     `json:"id"`
	RunID            string     `json:"runId"`
	State            string     `json:"state"`
	AssetID          string     `json:"assetId"`
	Format           string     `json:"format"`
	SourceID         string     `json:"sourceId"`
	ScanID           string     `json:"scanId"`
	Scope            Scope      `json:"scope"`
	SourceScanAt     *time.Time `json:"sourceScanAt"`
	CollectedAt      time.Time  `json:"collectedAt"`
	ImportedAt       time.Time  `json:"importedAt"`
	SourceStatus     string     `json:"sourceStatus"`
	ScanKind         string     `json:"scanKind"`
	Completeness     string     `json:"completeness"`
	ReportDigest     string     `json:"reportDigest"`
	ObservationCount int        `json:"observationCount"`
	Failure          *Failure   `json:"failure"`
}

type importRecord struct {
	Import
	WorkspaceID    string
	SubmittedBy    string
	ReportKey      string
	ReportSize     int64
	MetadataDigest string
	Mapping        parsers.Mapping
	Fence          int64
	WorkerID       string
}

type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	return json.Unmarshal(data, &o.Value)
}
