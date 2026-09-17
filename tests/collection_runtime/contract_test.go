//go:build integration

package collection_runtime

import (
	"context"
	"net/http"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/evidence"
)

type runtimeConfig struct {
	Database                       app.DatabaseConfig
	Raw                            evidence.Config
	CollectionStorage              *app.StorageConfig
	Key                            []byte
	Listen, Assets, PublicOrigin   string
	Bootstrap, WorkerID            string
	Lease                          time.Duration
	Endpoint, CAFile               string
	Client                         *http.Client
	Limits                         connectors.Limits
	ReadinessKey, NormalizedPrefix string
	PrepareReadiness               bool
}

var Production struct {
	Environment func(string) (runtimeConfig, error)
	Run         func(context.Context, string, runtimeConfig) error
}

type reply struct {
	APIVersion string
	User       struct{ ID string }
	Workspace  struct{ ID string }
	Source     struct{ ID string }
	Collection struct {
		ID, State   string
		Complete    bool
		RecordCount int
		AssetID     *string
		Failure     *struct{ Code string }
	}
	Items []struct {
		ID, Kind, ExternalID string
		Evidence             struct {
			SHA256    string
			SizeBytes int64
		}
	}
	Total int
}
type actor struct {
	ID, Workspace string
	Cookie        *http.Cookie
}
