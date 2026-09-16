//go:build integration

package runtime_roles

import (
	"context"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/bahadrdsr/aspm/internal/service"
)

type DatabaseConfig struct {
	URL                     string `json:"-"`
	Schema, ApplicationName string
	MaxConnections          int32
}

type ScopedStorage struct {
	Endpoint, Region, Bucket, Prefix, ReadinessKey string
	AccessKey, SecretKey                           string `json:"-"`
}

type CoreConfig struct {
	Database                     DatabaseConfig
	Raw                          ScopedStorage
	Listen, Assets, PublicOrigin string
	BootstrapToken               string `json:"-"`
}

type IngestionConfig struct {
	Database                 DatabaseConfig
	Raw                      ScopedStorage
	NormalizedPrefix, Listen string
}

type ReportConfig struct {
	Database DatabaseConfig
	Listen   string
}

type CoreRole interface{ Run(context.Context) error }
type IngestionRole interface{ Run(context.Context) error }
type ReportWorker interface{ Run(context.Context) error }

var Production struct {
	Core      func(CoreConfig) CoreRole
	Ingestion func(IngestionConfig) IngestionRole
	Reports   func(ReportConfig) ReportWorker
}

type mainRole struct {
	role   string
	config service.Config
}

func (r mainRole) Run(ctx context.Context) error { return service.Run(ctx, r.role, r.config) }

func commonConfig(db DatabaseConfig, listen string) service.Config {
	return service.Config{Listen: listen, WorkerID: db.ApplicationName,
		Jobs: jobs.Config{DatabaseURL: db.URL, Schema: db.Schema, ApplicationName: db.ApplicationName,
			MaxConnections: db.MaxConnections, MaxLease: time.Second, MaxAttempts: 3,
			RetryDelay: time.Second, MaxRetryDelay: 2 * time.Second}}
}

func rawConfig(store ScopedStorage) evidence.Config {
	return evidence.Config{Endpoint: store.Endpoint, Bucket: store.Bucket, Prefix: store.Prefix,
		Region: store.Region, AccessKey: store.AccessKey, SecretKey: store.SecretKey, Timeout: 4 * time.Second}
}

func init() {
	Production.Core = func(c CoreConfig) CoreRole {
		config := commonConfig(c.Database, c.Listen)
		config.Evidence, config.Assets, config.PublicOrigin, config.BootstrapToken = rawConfig(c.Raw), c.Assets, c.PublicOrigin, c.BootstrapToken
		config.ReadinessKey = c.Raw.ReadinessKey
		return mainRole{"core", config}
	}
	Production.Ingestion = func(c IngestionConfig) IngestionRole {
		config := commonConfig(c.Database, c.Listen)
		config.Evidence = rawConfig(c.Raw)
		config.ReadinessKey, config.NormalizedPrefix = c.Raw.ReadinessKey, c.NormalizedPrefix
		return mainRole{"ingestion", config}
	}
	Production.Reports = func(c ReportConfig) ReportWorker {
		return mainRole{"reports", commonConfig(c.Database, c.Listen)}
	}
}
