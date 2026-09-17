//go:build integration

package source_collection

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

const apiVersion = "aspm/v1alpha1"
const sourceProfile = "github-cloud-app"
const sourcesPath = "/api/v1/sources"

type coreConfig struct {
	Database          app.DatabaseConfig
	Storage           app.StorageConfig
	CollectionStorage *app.StorageConfig
	Key               []byte
	Bootstrap         string
	Log               io.Writer
}
type core struct {
	Handler http.Handler
	Close   func() error
}
type workerConfig struct {
	Database      app.DatabaseConfig
	Storage       app.StorageConfig
	Key           []byte
	WorkerID      string
	LeaseDuration time.Duration
	Endpoint      string
	Client        *http.Client
	Limits        connectors.Limits
	Tracer        pgx.QueryTracer
}
type collectionWorker interface {
	ProcessNext(context.Context) (bool, error)
	Ping(context.Context) error
	Close() error
}

var Production struct {
	OpenCore   func(context.Context, coreConfig) (core, error)
	OpenWorker func(context.Context, workerConfig) (collectionWorker, error)
}

type sourceConnection struct {
	ID, WorkspaceID, Profile, Name, Repository string
	Enabled, CredentialConfigured              bool
	Revision                                   int64
	CreatedAt, UpdatedAt                       time.Time
}
type nativeFailure struct {
	Code, NativeCode  string
	HTTPStatus        int
	RetryAfterSeconds int64
	Retryable         bool
}
type collection struct {
	ID, WorkspaceID, SourceID, Profile, Repository, RequestedBy, State string
	ConnectionRevision                                                 int64
	Complete                                                           bool
	AssetID, RepositoryID                                              *string
	RecordCount                                                        int
	Gaps                                                               []string
	CreatedAt                                                          time.Time
	CollectedAt, CompletedAt                                           *time.Time
	Failure                                                            *nativeFailure
}
type record struct {
	ID, CollectionID, Kind, ExternalID, ParentID   string
	NativeRunID, State, Severity, Location, RawURL string
	Ordinal                                        int
	SourceScanAt, SourceUpdatedAt                  *time.Time
	Evidence                                       struct {
		SHA256    string
		SizeBytes int64
	}
}
type asset struct {
	ID, WorkspaceID, Name, Kind, Environment, Criticality string
	OwnerID                                               *string
	Tags                                                  []string
}
type actor struct {
	ID, Workspace string
	Cookie        *http.Cookie
}
type apiReply struct {
	APIVersion string
	User       struct{ ID string }
	Workspace  struct{ ID string }
	Workspaces []struct{ ID, Role string }
	Source     sourceConnection
	Collection collection
	Asset      asset
	Connection struct{ ID string }
	Items      []map[string]any
	Total      int
	NextCursor *string
	Error      *struct {
		Code, Message, RequestID string
		Retryable                bool
	}
}
