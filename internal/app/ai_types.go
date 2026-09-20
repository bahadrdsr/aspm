package app

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	aiTask          = "finding-validity"
	aiDataClass     = "finding-evidence"
	aiEndpointLimit = 16384
	aiBodyLimit     = 32 << 10
)

type AIProfile struct {
	ID                   string    `json:"id"`
	WorkspaceID          string    `json:"workspaceId"`
	Name                 string    `json:"name"`
	Family               string    `json:"family"`
	Endpoint             string    `json:"endpoint"`
	Model                string    `json:"model"`
	Deployment           string    `json:"deployment"`
	Enabled              bool      `json:"enabled"`
	StructuredOutput     bool      `json:"structuredOutput"`
	CredentialConfigured bool      `json:"credentialConfigured"`
	Revision             string    `json:"revision"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type aiProfileRecord struct {
	AIProfile
	ciphertext []byte
}

const aiProfileColumns = `id,workspace_id,name,family,endpoint,model,deployment,
	enabled,structured_output,revision::text,created_at,updated_at,credential_ciphertext`

func scanAIProfile(row pgx.Row) (aiProfileRecord, error) {
	var record aiProfileRecord
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.Name, &record.Family,
		&record.Endpoint, &record.Model, &record.Deployment, &record.Enabled,
		&record.StructuredOutput, &record.Revision, &record.CreatedAt, &record.UpdatedAt,
		&record.ciphertext)
	record.CredentialConfigured = len(record.ciphertext) != 0
	return record, err
}

type AIPolicy struct {
	WorkspaceID string     `json:"workspaceId"`
	Mode        string     `json:"mode"`
	Revision    string     `json:"revision"`
	UpdatedAt   *time.Time `json:"updatedAt"`
	UpdatedBy   *string    `json:"updatedBy"`
}

const aiPolicyColumns = `workspace_id,mode,revision::text,updated_at,updated_by`

func scanAIPolicy(row pgx.Row, workspace string) (AIPolicy, error) {
	var policy AIPolicy
	err := row.Scan(&policy.WorkspaceID, &policy.Mode, &policy.Revision, &policy.UpdatedAt, &policy.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return AIPolicy{WorkspaceID: workspace, Mode: "disabled", Revision: "0"}, nil
	}
	return policy, err
}

type AIEgressGrant struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspaceId"`
	ProfileID       string     `json:"profileId"`
	ProfileRevision string     `json:"profileRevision"`
	PolicyRevision  string     `json:"policyRevision"`
	Destination     string     `json:"destination"`
	Task            string     `json:"task"`
	DataClass       string     `json:"dataClass"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	GrantedBy       string     `json:"grantedBy"`
	RevokedAt       *time.Time `json:"revokedAt"`
	RevokedBy       *string    `json:"revokedBy"`
}

const aiGrantColumns = `id,workspace_id,profile_id,profile_revision,policy_revision,destination,
	task,data_class,expires_at,created_at,granted_by,revoked_at,revoked_by`

func scanAIGrant(row pgx.Row) (AIEgressGrant, error) {
	var grant AIEgressGrant
	err := row.Scan(&grant.ID, &grant.WorkspaceID, &grant.ProfileID, &grant.ProfileRevision,
		&grant.PolicyRevision, &grant.Destination, &grant.Task, &grant.DataClass,
		&grant.ExpiresAt, &grant.CreatedAt, &grant.GrantedBy, &grant.RevokedAt, &grant.RevokedBy)
	return grant, err
}

func aiCredentialAAD(workspace, profile string) []byte {
	return []byte("aspm/ai-credential/v1\x00" + workspace + "\x00" + profile)
}
