package installer

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
	Stdin []byte
}

type CommandResult struct {
	ExitCode       int
	Stdout, Stderr []byte
}

// Files is rooted disk I/O, not a replacement for production state or rendering.
type Files interface {
	Stat(string) (fs.FileInfo, error)
	ReadFile(string) ([]byte, error)
	WriteFile(string, []byte, fs.FileMode) error
	Rename(string, string) error
	Remove(string) error
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

type RoleCredential struct {
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
}

type RoleCredentials struct {
	Core, Ingestion RoleCredential
}

type Options struct {
	Root       string
	Kubeconfig string
	Files      Files
	Run        func(context.Context, Command) (CommandResult, error)
	TrustedKey ed25519.PublicKey
	RoleKeys   RoleCredentials `json:"-"`
	Output     io.Writer
	LocalHost  struct {
		OS   string
		EUID int
	}
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
	ID, ConfigID, BundleDigest, TargetFingerprint, Trust string
	Images                                               map[string]string
	Operation                                            string
	DeleteData                                           bool
	RuntimeRoles                                         RuntimeRoles
}

type State struct {
	Phase, PlanID, Trust, FailedStep, FailureCode string
	Completed, SecretFiles                        []string
}

type Installer interface {
	Plan(context.Context, Intent) (Plan, error)
	Execute(context.Context, Intent, Approval) (State, error)
	Status(context.Context) (State, error)
	Close() error
}

// A later coder-owned binding must only construct/forward real implementation.
var Production struct {
	OpenInstaller func(context.Context, Options) (Installer, error)
}
