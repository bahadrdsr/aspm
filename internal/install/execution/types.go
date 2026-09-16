package execution

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
)

var (
	ErrApproval    = errors.New("deployment approval mismatch")
	ErrBundle      = errors.New("untrusted or inconsistent bundle")
	ErrCredential  = errors.New("required caller credential missing or unusable")
	ErrCommand     = errors.New("external command failed")
	ErrUnsupported = errors.New("unsupported execution profile")
)

type Command struct {
	Tool  string
	Args  []string
	Stdin []byte `json:"-"`
}

func (Command) String() string   { return "installer command [private input redacted]" }
func (Command) GoString() string { return "installer command [private input redacted]" }

type CommandResult struct {
	ExitCode int
	Stdout   []byte `json:"-"`
	Stderr   []byte `json:"-"`
}

type Files interface {
	Stat(string) (fs.FileInfo, error)
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode) error
	Rename(string, string) error
	Remove(string) error
}

type Host struct {
	OS   string
	EUID int
}

type RoleCredential struct {
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
}

func (RoleCredential) String() string   { return "role credential [redacted]" }
func (RoleCredential) GoString() string { return "role credential [redacted]" }

type RoleCredentials struct {
	Core      RoleCredential `json:"-"`
	Ingestion RoleCredential `json:"-"`
}

type Options struct {
	Root       string
	Kubeconfig string
	Files      Files
	Run        func(context.Context, Command) (CommandResult, error)
	TrustedKey ed25519.PublicKey
	Output     io.Writer
	LocalHost  Host
	RoleKeys   RoleCredentials `json:"-"`
}

type SecretSelection struct {
	Name         string `json:"name"`
	AccessKeyKey string `json:"accessKeyKey"`
	SecretKeyKey string `json:"secretKeyKey"`
}

type RoleSelection struct {
	S3Secret         SecretSelection `json:"s3Secret"`
	RawPrefix        string          `json:"rawPrefix"`
	ReadinessKey     string          `json:"readinessKey"`
	NormalizedPrefix string          `json:"normalizedPrefix,omitempty"`
}

type RuntimeRoles struct {
	Core      RoleSelection `json:"core"`
	Ingestion RoleSelection `json:"ingestion"`
}

type Intent struct {
	Configuration     json.RawMessage
	BundleDir         string
	Operation         string
	DeleteData        bool
	DevelopmentPolicy string
	RuntimeRoles      RuntimeRoles
}

type Approval struct {
	PlanID string
	DryRun bool
}

type Plan struct {
	ID                string            `json:"id"`
	ConfigID          string            `json:"configId"`
	BundleDigest      string            `json:"bundleDigest"`
	TargetFingerprint string            `json:"targetFingerprint"`
	Trust             string            `json:"trust"`
	Images            map[string]string `json:"images"`
	Operation         string            `json:"operation"`
	DeleteData        bool              `json:"deleteData"`
	RuntimeRoles      RuntimeRoles      `json:"runtimeRoles"`
}

type State struct {
	Phase       string   `json:"phase"`
	PlanID      string   `json:"planId"`
	Trust       string   `json:"trust"`
	FailedStep  string   `json:"failedStep,omitempty"`
	FailureCode string   `json:"failureCode,omitempty"`
	Completed   []string `json:"completed"`
	SecretFiles []string `json:"secretFiles"`
}

type Installer interface {
	Plan(context.Context, Intent) (Plan, error)
	Execute(context.Context, Intent, Approval) (State, error)
	Status(context.Context) (State, error)
	Close() error
}
