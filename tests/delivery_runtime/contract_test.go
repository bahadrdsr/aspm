//go:build integration

package delivery_runtime

import (
	"context"
	"net/http"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

type databaseConfig struct {
	URL, Schema, ApplicationName string
	MaxConnections               int32
}

type roleConfig struct {
	Database         databaseConfig
	Listen, WorkerID string
	EncryptionKey    []byte
	LeaseDuration    time.Duration
	SlackEndpoint    string
	Client           *http.Client
	CAFile           string
	Evidence         evidence.Config
	BootstrapToken   string
}

var Production struct {
	Environment func(string) (roleConfig, error)
	Run         func(context.Context, roleConfig) error
}
