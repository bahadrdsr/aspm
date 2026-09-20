package app

import (
	"context"
	"crypto/cipher"
	"errors"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
)

type AIConfigurationResolverConfig struct {
	Database      DatabaseConfig
	EncryptionKey []byte `json:"-"`
}

// ActorID is a trusted server/worker identity, never a public request override.
type AIConfigurationRequest struct {
	WorkspaceID, ActorID, ProfileID, GrantID, Task, DataClass string
}

// AIConfiguration is private configuration, not an HTTP response DTO.
// Profile.APIKey retains providers.Profile's json:"-" restriction.
type AIConfiguration struct {
	Profile providers.Profile
	Policy  providers.Policy
}

// AIConfigurationResolver owns an independent database pool and optional
// credential cipher. It constructs no storage client or HTTP component.
type AIConfigurationResolver struct {
	*database
	credentials cipher.AEAD
}

func OpenAIConfigurationResolver(ctx context.Context, config AIConfigurationResolverConfig) (*AIConfigurationResolver, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	credentials, err := newIntegrationCipher(config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &AIConfigurationResolver{database: db, credentials: credentials}, nil
}

func (r *AIConfigurationResolver) Close() error { return r.database.close() }

func aiLookupError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return providers.ErrPolicy
	}
	// Do not expose database or credential-bearing diagnostics to callers.
	return errUnavailable
}

// Resolve authorizes a point-in-time read, not an execution reservation. Any
// future worker action must re-resolve its then-current persisted authority.
func (r *AIConfigurationResolver) Resolve(ctx context.Context, request AIConfigurationRequest) (AIConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return AIConfiguration{}, err
	}
	if !validID(request.WorkspaceID) || !validID(request.ActorID) || !validID(request.ProfileID) ||
		request.GrantID != "" && !validID(request.GrantID) ||
		request.Task != aiTask || request.DataClass != aiDataClass {
		return AIConfiguration{}, providers.ErrPolicy
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	defer rollback(tx)
	var workspace, role string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+r.table("workspaces")+` WHERE id=$1`,
		request.WorkspaceID).Scan(&workspace); err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	if err = tx.QueryRow(ctx, `SELECT role FROM `+r.table("memberships")+`
		WHERE workspace_id=$1 AND user_id=$2`, workspace, request.ActorID).Scan(&role); err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	if role != "admin" && role != "analyst" {
		return AIConfiguration{}, providers.ErrPolicy
	}
	profile, err := scanAIProfile(tx.QueryRow(ctx, `SELECT `+aiProfileColumns+
		` FROM `+r.table("ai_profiles")+` WHERE workspace_id=$1 AND id=$2`, workspace, request.ProfileID))
	if err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	if !profile.Enabled || !validAIProfile(profile.AIProfile) || profile.Revision == "" {
		return AIConfiguration{}, providers.ErrPolicy
	}
	if !profile.StructuredOutput {
		return AIConfiguration{}, providers.ErrCapability
	}
	policy, err := scanAIPolicy(tx.QueryRow(ctx, `SELECT `+aiPolicyColumns+
		` FROM `+r.table("ai_policies")+` WHERE workspace_id=$1`, workspace), workspace)
	if err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	if policy.Revision == "" || policy.Revision == "0" || policy.UpdatedAt == nil || policy.UpdatedBy == nil {
		return AIConfiguration{}, providers.ErrPolicy
	}
	var grant AIEgressGrant
	switch policy.Mode {
	case "local-only":
		if profile.Family != "local" || request.GrantID != "" {
			return AIConfiguration{}, providers.ErrPolicy
		}
	case "approved-hosted":
		if request.GrantID == "" {
			return AIConfiguration{}, providers.ErrPolicy
		}
		grant, err = scanAIGrant(tx.QueryRow(ctx, `SELECT `+aiGrantColumns+
			` FROM `+r.table("ai_egress_grants")+` WHERE workspace_id=$1 AND id=$2`, workspace, request.GrantID))
		if err != nil {
			return AIConfiguration{}, aiLookupError(ctx, err)
		}
		if grant.ProfileID != profile.ID || grant.ProfileRevision != profile.Revision ||
			grant.PolicyRevision != policy.Revision || grant.Destination != profile.Endpoint ||
			grant.Task != request.Task || grant.DataClass != request.DataClass ||
			grant.RevokedAt != nil || grant.RevokedBy != nil || !grant.ExpiresAt.After(r.now().UTC()) {
			return AIConfiguration{}, providers.ErrPolicy
		}
	default:
		return AIConfiguration{}, providers.ErrPolicy
	}
	var plain []byte
	if profile.CredentialConfigured {
		plain, err = openCredential(r.credentials, aiCredentialAAD(workspace, profile.ID), profile.ciphertext)
		if err != nil {
			return AIConfiguration{}, providers.ErrPolicy
		}
		defer clear(plain)
	}
	if err = tx.Commit(ctx); err != nil {
		return AIConfiguration{}, aiLookupError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return AIConfiguration{}, err
	}
	if grant.ID != "" && !grant.ExpiresAt.After(r.now().UTC()) {
		return AIConfiguration{}, providers.ErrPolicy
	}
	return AIConfiguration{
		Profile: providers.Profile{
			ID: profile.ID, Revision: profile.Revision, Family: profile.Family, Endpoint: profile.Endpoint,
			Model: profile.Model, Deployment: profile.Deployment, APIKey: string(plain),
			StructuredOutput: profile.StructuredOutput,
		},
		Policy: providers.Policy{
			Mode: policy.Mode, Revision: policy.Revision, ApprovalRef: grant.ID,
			Workspaces: []string{workspace}, Tasks: []string{request.Task},
			DataClasses: []string{request.DataClass}, AllowedDestinations: []string{profile.Endpoint},
		},
	}, nil
}
