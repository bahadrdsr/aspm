package acceptance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const apiVersion = "aspm/v1alpha1"

type InstallationPlan struct {
	ID            string
	Configuration json.RawMessage
}

type StorageConfig struct {
	Endpoint, Bucket, Prefix, Region string
	AccessKey                        string `json:"-"`
	SecretKey                        string `json:"-"`
}

type OIDCConfig struct {
	Issuer, ClientID, WorkspaceID string
	ClientSecret                  string `json:"-"`
	Client                        *http.Client
}

type ApplicationConfig struct {
	DatabaseURL             string `json:"-"`
	Schema, ApplicationName string
	MaxConnections          int32
	Storage                 StorageConfig
	BootstrapToken          string           `json:"-"`
	Now                     func() time.Time `json:"-"`
	LogOutput               io.Writer        `json:"-"`
	SessionTTL              time.Duration
	MaxUploadBytes          int64
	ManualProcessing        bool
	OIDC                    *OIDCConfig
	QueryTracer             pgx.QueryTracer `json:"-"`
}

type Application struct {
	Handler        http.Handler
	Close          func() error
	ProcessImports func(context.Context) error
	ProcessReports func(context.Context) error
}

// The coder owns forwarding-only bindings, never test-side business logic or SQL.
var Production struct {
	Plan            func(context.Context, json.RawMessage) (InstallationPlan, error)
	OpenApplication func(context.Context, ApplicationConfig) (Application, error)
}

type object = map[string]any

func ok(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; sensitive details withheld)", operation, err)
	}
}

func equal[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s did not match the contract", label)
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	ok(t, "encode test input", err)
	return data
}

func nonce(t *testing.T) string {
	t.Helper()
	b := make([]byte, 12)
	_, err := rand.Read(b)
	ok(t, "generate owned namespace", err)
	return hex.EncodeToString(b)
}

func secret(t *testing.T) string {
	t.Helper()
	return "Aa9!" + nonce(t) + nonce(t)
}

func requireApplication(t *testing.T) {
	t.Helper()
	if Production.OpenApplication == nil {
		t.Fatal("production binding missing: OpenApplication; coder must forward to real application and workers")
	}
}
