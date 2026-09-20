//go:build integration

package ai_configuration

import (
	"context"
	"net/http"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/providers"
)

const apiVersion = "aspm/v1alpha1"
const profilesPath = "/api/v1/ai/profiles"
const policyPath = "/api/v1/ai/policy"
const grantsPath = "/api/v1/ai/grants"
const validityTask = "finding-validity"
const evidenceClass = "finding-evidence"

type actor struct {
	ID, Workspace string
	Cookie        *http.Cookie
}
type profile struct {
	ID, WorkspaceID, Name, Family, Endpoint, Model, Deployment, Revision string
	Enabled, StructuredOutput, CredentialConfigured                      bool
	CreatedAt, UpdatedAt                                                 time.Time
}
type policy struct {
	WorkspaceID, Mode, Revision string
	UpdatedAt                   *time.Time
	UpdatedBy                   *string
}
type grant struct {
	ID, WorkspaceID, ProfileID, ProfileRevision, PolicyRevision string
	Destination, Task, DataClass, GrantedBy                     string
	ExpiresAt, CreatedAt                                        time.Time
	RevokedAt                                                   *time.Time
	RevokedBy                                                   *string
}
type reply struct {
	APIVersion string
	User       struct{ ID string }
	Workspace  struct{ ID string }
	Profile    profile
	Policy     policy
	Grant      grant
	Connection struct{ ID string }
	Source     struct{ ID string }
	Items      []map[string]any
	Total      int
	NextCursor *string
	Error      *struct {
		Code, Message, RequestID string
		Retryable                bool
	}
}
type lookupRequest struct {
	WorkspaceID, ActorID, ProfileID, GrantID, Task, DataClass string
}
type resolved struct {
	Profile providers.Profile
	Policy  providers.Policy
}
type resolver interface {
	Resolve(context.Context, lookupRequest) (resolved, error)
	Close() error
}

var Production struct {
	OpenResolver func(context.Context, app.DatabaseConfig, []byte) (resolver, error)
}
