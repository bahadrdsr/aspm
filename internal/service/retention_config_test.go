package service

import (
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

func validRetentionServiceConfig() Config {
	return Config{
		Listen:        "127.0.0.1:18080",
		ArchivePrefix: "archive/team-a/",
		Jobs: jobs.Config{
			DatabaseURL: "postgres://aspm:synthetic-password@127.0.0.1:5432/aspm?sslmode=disable",
			Schema:      "aspm", ApplicationName: "aspm-retention", MaxConnections: 1,
			MaxLease: time.Minute, MaxAttempts: 3, RetryDelay: time.Second, MaxRetryDelay: 30 * time.Second,
		},
		Evidence: evidence.Config{
			Endpoint: "http://127.0.0.1:8333", Bucket: "aspm-evidence",
			Prefix: "raw/team-a/", Region: "us-east-1",
			AccessKey: "retention-access", SecretKey: "retention-secret", Timeout: 30 * time.Second,
		},
	}
}

func TestRetentionConfigAllowsOneConnectionAndRequiresDisjointArchiveScope(t *testing.T) {
	config := validRetentionServiceConfig()
	if err := validateConfig("retention", config); err != nil {
		t.Fatalf("valid single-pool retention configuration was rejected: %v", err)
	}
	config.ArchivePrefix = config.Evidence.Prefix
	if err := validateConfig("retention", config); err == nil {
		t.Fatal("overlapping retention raw/archive scopes were accepted")
	}
}

func TestRetentionEnvironmentSelectsIndependentSinglePoolAndArchiveScope(t *testing.T) {
	for name, value := range map[string]string{
		"ASPM_DATABASE_URL": validRetentionServiceConfig().Jobs.DatabaseURL,
		"ASPM_SCHEMA":       "aspm", "ASPM_DB_MAX_CONNECTIONS": "1", "ASPM_LISTEN": "127.0.0.1:18080",
		"ASPM_S3_ENDPOINT": "http://127.0.0.1:8333", "ASPM_S3_BUCKET": "aspm-evidence",
		"ASPM_S3_ACCESS_KEY": "retention-access", "ASPM_S3_SECRET_ACCESS_KEY": "",
		"ASPM_S3_SECRET_KEY": "retention-secret", "ASPM_S3_REGION": "us-east-1",
		"ASPM_S3_PREFIX": "raw/team-a/", "ASPM_S3_ARCHIVE_PREFIX": "archive/team-a/",
		"ASPM_S3_READINESS_KEY": "", "ASPM_S3_PREPARE_READINESS": "",
		"ASPM_TLS_CERT_FILE": "", "ASPM_TLS_KEY_FILE": "",
	} {
		t.Setenv(name, value)
	}
	config, err := Environment("retention")
	if err != nil {
		t.Fatalf("valid retention environment was rejected: %v", err)
	}
	if config.Jobs.MaxConnections != 1 || config.ArchivePrefix != "archive/team-a/" ||
		config.Evidence.Prefix != "raw/team-a/" {
		t.Fatalf("retention environment lost selected pool or scopes: %+v", config)
	}
}

func TestRetentionConfigRejectsLeaseOutsideWorkerBounds(t *testing.T) {
	for _, lease := range []time.Duration{time.Second - 1, 5*time.Minute + 1} {
		config := validRetentionServiceConfig()
		config.Jobs.MaxLease = lease
		if err := validateConfig("retention", config); err == nil {
			t.Fatalf("retention lease %s outside worker bounds was accepted", lease)
		}
	}
}
