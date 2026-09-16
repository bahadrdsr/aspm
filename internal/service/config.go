package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/jackc/pgx/v5"
)

type Config struct {
	Listen           string
	Assets           string
	WorkerID         string
	Workspace        string
	PublicOrigin     string
	TLSCertFile      string
	TLSKeyFile       string
	BootstrapToken   string `json:"-"`
	ReadinessKey     string
	PrepareReadiness bool
	NormalizedPrefix string
	Jobs             jobs.Config
	Evidence         evidence.Config
}

func Environment(role string) (Config, error) {
	if role != "core" && role != "ingestion" && role != "reports" {
		return Config{}, errors.New("unsupported service role")
	}
	prepare := false
	if value := os.Getenv("ASPM_S3_PREPARE_READINESS"); value != "" {
		var err error
		prepare, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, errors.New("ASPM_S3_PREPARE_READINESS must be an explicit boolean")
		}
	}
	if prepare && role != "core" {
		return Config{}, errors.New("readiness preparation is only supported by core")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Config{}, fmt.Errorf("resolve worker identity: %w", err)
	}
	connections := int64(5)
	minimum := int64(2)
	if role == "reports" {
		minimum = 1
	}
	if value := os.Getenv("ASPM_DB_MAX_CONNECTIONS"); value != "" {
		connections, err = strconv.ParseInt(value, 10, 32)
		if err != nil || connections < minimum || connections > 100 {
			return Config{}, errors.New("invalid ASPM_DB_MAX_CONNECTIONS for the selected role")
		}
	}
	config := Config{
		Listen:           env("ASPM_LISTEN", "127.0.0.1:18080"),
		PrepareReadiness: prepare,
		WorkerID:         role + "-" + hostname + "-" + jobs.NewID(), Workspace: os.Getenv("ASPM_WORKSPACE"),
		TLSCertFile: os.Getenv("ASPM_TLS_CERT_FILE"), TLSKeyFile: os.Getenv("ASPM_TLS_KEY_FILE"),
		Jobs: jobs.Config{
			DatabaseURL: os.Getenv("ASPM_DATABASE_URL"), Schema: env("ASPM_SCHEMA", "aspm"),
			ApplicationName: "aspm-" + role, MaxConnections: int32(connections),
			MaxLease: time.Minute, MaxAttempts: 3, RetryDelay: time.Second, MaxRetryDelay: 30 * time.Second,
		},
	}
	if role != "reports" {
		config.Evidence = evidence.Config{
			Endpoint: os.Getenv("ASPM_S3_ENDPOINT"), Bucket: env("ASPM_S3_BUCKET", "aspm-evidence"),
			AccessKey: os.Getenv("ASPM_S3_ACCESS_KEY"), SecretKey: os.Getenv("ASPM_S3_SECRET_KEY"),
			Prefix: env("ASPM_S3_PREFIX", "evidence/"), Region: env("ASPM_S3_REGION", "us-east-1"),
			Timeout: 30 * time.Second,
		}
		config.ReadinessKey = os.Getenv("ASPM_S3_READINESS_KEY")
	}
	if role == "core" {
		config.Assets = env("ASPM_ASSETS", "web/dist")
		config.PublicOrigin = os.Getenv("ASPM_PUBLIC_ORIGIN")
		config.BootstrapToken = os.Getenv("ASPM_BOOTSTRAP_TOKEN")
	}
	if role == "ingestion" {
		config.NormalizedPrefix = env("ASPM_S3_NORMALIZED_PREFIX", "normalized/")
	}
	if err := validateConfig(role, config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func storageConfig(config evidence.Config) app.StorageConfig {
	return app.StorageConfig{Endpoint: config.Endpoint, Bucket: config.Bucket, Prefix: config.Prefix, Region: config.Region,
		AccessKey: config.AccessKey, SecretKey: config.SecretKey, Timeout: config.Timeout}
}

func databaseConfig(config jobs.Config) app.DatabaseConfig {
	return app.DatabaseConfig{DatabaseURL: config.DatabaseURL, Schema: config.Schema,
		ApplicationName: config.ApplicationName, MaxConnections: config.MaxConnections, Now: time.Now, LogOutput: os.Stderr}
}

// Validate the complete caller configuration before EnsureSchema, pool opens,
// storage probes, listeners, or worker goroutines can perform I/O.
func validateConfig(role string, config Config) error {
	if role != "core" && role != "ingestion" && role != "reports" {
		return errors.New("unsupported service role")
	}
	if config.PrepareReadiness && (role != "core" || config.ReadinessKey == "") {
		return errors.New("readiness preparation requires core and an explicit scoped readiness key")
	}
	if role != "reports" {
		if err := app.ValidateStorageConfig(storageConfig(config.Evidence)); err != nil {
			return err
		}
		if config.ReadinessKey != "" {
			if err := app.ValidateReadinessKey(config.Evidence.Prefix, config.ReadinessKey); err != nil {
				return err
			}
		}
		if config.Jobs.MaxConnections < 2 {
			return errors.New("application and integrity pools require at least two total database connections")
		}
		if config.Jobs.MaxLease <= 0 || config.Jobs.MaxLease > 10*time.Minute ||
			config.Jobs.MaxAttempts < 1 || config.Jobs.MaxAttempts > 20 ||
			config.Jobs.RetryDelay <= 0 || config.Jobs.MaxRetryDelay < config.Jobs.RetryDelay {
			return errors.New("invalid durable job configuration")
		}
	}
	if role == "ingestion" {
		if err := app.ValidateNormalizedPrefix(config.Evidence.Prefix, config.NormalizedPrefix); err != nil {
			return err
		}
		if strings.TrimSpace(config.WorkerID) == "" || len(config.WorkerID) > 512 || strings.ContainsRune(config.WorkerID, 0) {
			return errors.New("an explicit bounded worker identity is required")
		}
	}
	if err := app.ValidateDatabaseConfig(databaseConfig(config.Jobs)); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(config.Listen)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 0 || number > 65535 {
		return errors.New("invalid service listen address")
	}
	if (config.TLSCertFile == "") != (config.TLSKeyFile == "") {
		return errors.New("both TLS certificate and key files are required for direct HTTPS")
	}
	if role == "core" {
		if config.Assets == "" || (config.BootstrapToken != "" && len(config.BootstrapToken) < 32) {
			return errors.New("invalid core assets or bootstrap configuration")
		}
		if config.PublicOrigin != "" {
			origin, err := url.Parse(config.PublicOrigin)
			if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
				origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
				return errors.New("public origin must be an HTTPS origin without a path")
			}
		}
	}
	return nil
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func EnsureSchema(ctx context.Context, config jobs.Config) (err error) {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(config.Schema) {
		return errors.New("invalid database schema")
	}
	pc, err := pgx.ParseConfig(config.DatabaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	pc.RuntimeParams["application_name"] = "aspm-schema-migration"
	connection, err := pgx.ConnectConfig(ctx, pc)
	if err != nil {
		return errors.New("could not connect to the database for schema initialization")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = errors.Join(err, connection.Close(closeCtx))
	}()
	_, err = connection.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{config.Schema}.Sanitize())
	if err != nil {
		return fmt.Errorf("initialize selected schema: %w", err)
	}
	return nil
}
