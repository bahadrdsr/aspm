//go:build integration

package remediation

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const apiVersion = "aspm/v1alpha1"
const slackProfile = "slack-workspace-bot"
const publicOrigin = "https://remediation.synthetic.invalid"
const connectionsPath = "/api/v1/integrations/connections"

type databaseConfig struct {
	URL, Schema, ApplicationName string
	MaxConnections               int32
}

type storageConfig struct {
	Endpoint, Bucket, Prefix, Region string
	AccessKey, SecretKey             string
}

type coreConfig struct {
	Database       databaseConfig
	Storage        storageConfig
	EncryptionKey  []byte
	BootstrapToken string
	PublicOrigin   string
	LogOutput      io.Writer
	QueryTracer    pgx.QueryTracer
}

type core struct {
	Handler        http.Handler
	Close          func() error
	ProcessImports func(context.Context) error
}

type workerConfig struct {
	Database      databaseConfig
	EncryptionKey []byte
	WorkerID      string
	LeaseDuration time.Duration
	SlackEndpoint string
	Client        *http.Client
	LogOutput     io.Writer
}

type deliveryWorker interface {
	ProcessNext(context.Context) (bool, error)
	Ping(context.Context) error
	Close() error
}

var Production struct {
	OpenCore       func(context.Context, coreConfig) (core, error)
	OpenWorker     func(context.Context, workerConfig) (deliveryWorker, error)
	EnvironmentKey func() ([]byte, error)
	RunCore        func(context.Context, coreConfig, string, string) error
}

type object = map[string]any

type connection struct {
	ID, WorkspaceID, Profile, Name, Channel string
	Enabled, CredentialConfigured           bool
	Revision                                int64
	CreatedAt, UpdatedAt                    time.Time
}

type notification struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	DeepLink string `json:"deepLink"`
}

type failure struct {
	Code, NativeCode  string
	HTTPStatus        int
	RetryAfterSeconds int64
	Retryable         bool
}

type receipt struct{ RemoteID, RemoteURL string }

type delivery struct {
	ID, WorkspaceID, FindingID, ConnectionID string
	Profile, Channel, RequestedBy, State     string
	ConnectionRevision                       int64
	Payload                                  notification
	CreatedAt                                time.Time
	DispatchStartedAt, CompletedAt           *time.Time
	Receipt                                  *receipt
	Failure                                  *failure
}

type user struct{ ID, Name, Email string }
type workspace struct{ ID, Name, Role string }
type actor struct {
	User      user
	Workspace string
	Cookie    *http.Cookie
}

type finding struct {
	ID, WorkspaceID, AssetID, AssetName, Title, Severity string
	WorkflowState, SourceState, Disposition              string
	VerifiedResolution                                   bool
	Evidence                                             struct{ Text, VerificationState string }
	Notes                                                []object
}

type apiReply struct {
	APIVersion string
	User       user
	Workspace  workspace
	Workspaces []workspace
	Asset      struct{ ID string }
	Import     struct{ ID, State string }
	Finding    finding
	Connection connection
	Delivery   delivery
	Items      []jsonObject
	Total      int
	NextCursor *string
	Error      *struct {
		Code, Message, RequestID string
		Retryable                bool
	}
}

type jsonObject = map[string]any
