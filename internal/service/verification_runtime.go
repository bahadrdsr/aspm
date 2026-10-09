package service

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func verificationEnvironment(config *Config) error {
	allowed := map[string]bool{
		"ASPM_DATABASE_URL": true, "ASPM_SCHEMA": true, "ASPM_DB_MAX_CONNECTIONS": true,
		"ASPM_LISTEN": true, "ASPM_TLS_CERT_FILE": true, "ASPM_TLS_KEY_FILE": true,
		"ASPM_VERIFICATION_LEASE_DURATION":         true,
		"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL": true,
		"ASPM_VERIFICATION_MAX_FIXTURE_BYTES":      true,
	}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "ASPM_") && value != "" && !allowed[strings.ToUpper(name)] {
			return errors.New("verification role rejects unrelated authority")
		}
	}
	var err error
	config.VerificationLeaseDuration = 90 * time.Second
	if value := os.Getenv("ASPM_VERIFICATION_LEASE_DURATION"); value != "" {
		config.VerificationLeaseDuration, err = time.ParseDuration(value)
		if err != nil || config.VerificationLeaseDuration < time.Second ||
			config.VerificationLeaseDuration > 10*time.Minute {
			return errors.New("ASPM_VERIFICATION_LEASE_DURATION must be an explicit bounded duration")
		}
	}
	config.VerificationAuthorizationInterval = 100 * time.Millisecond
	if value := os.Getenv("ASPM_VERIFICATION_AUTHORIZATION_INTERVAL"); value != "" {
		config.VerificationAuthorizationInterval, err = time.ParseDuration(value)
		if err != nil || config.VerificationAuthorizationInterval < 10*time.Millisecond ||
			config.VerificationAuthorizationInterval > time.Second {
			return errors.New("ASPM_VERIFICATION_AUTHORIZATION_INTERVAL must be an explicit bounded duration")
		}
	}
	config.VerificationMaxFixtureBytes = 65536
	if value := os.Getenv("ASPM_VERIFICATION_MAX_FIXTURE_BYTES"); value != "" {
		config.VerificationMaxFixtureBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || config.VerificationMaxFixtureBytes < 1 ||
			config.VerificationMaxFixtureBytes > 65536 {
			return errors.New("ASPM_VERIFICATION_MAX_FIXTURE_BYTES must be an explicit bounded integer")
		}
	}
	return nil
}

func verificationWorkerConfig(config Config) app.VerificationWorkerConfig {
	return app.VerificationWorkerConfig{
		Database: databaseConfig(config.Jobs), WorkerID: config.WorkerID,
		LeaseDuration:         config.VerificationLeaseDuration,
		AuthorizationInterval: config.VerificationAuthorizationInterval,
		MaxFixtureBytes:       config.VerificationMaxFixtureBytes,
	}
}

func validateVerificationConfig(config Config) error {
	if config.Workspace != "" || config.Assets != "" || config.PublicOrigin != "" ||
		config.BootstrapToken != "" || len(config.IntegrationEncryptionKey) != 0 ||
		config.CollectionStorage != nil || config.ReadinessKey != "" || config.PrepareReadiness ||
		config.NormalizedPrefix != "" || config.ArchivePrefix != "" ||
		config.DeliveryClient != nil || config.CollectionClient != nil || config.AssessmentClient != nil ||
		config.SlackEndpoint != "" || len(config.JiraAPIOrigins) != 0 ||
		len(config.TeamsWorkflowOrigins) != 0 || len(config.WebhookOrigins) != 0 ||
		config.GitHubEndpoint != "" || config.AzureDevOpsEndpoint != "" ||
		config.AssessmentScope != "" || config.AssessmentCAFile != "" ||
		config.CollectionCAFile != "" || config.DeliveryCAFile != "" ||
		config.Evidence.Endpoint != "" || config.Evidence.Bucket != "" ||
		config.Evidence.AccessKey != "" || config.Evidence.SecretKey != "" ||
		config.Evidence.Prefix != "" || config.Evidence.Region != "" ||
		config.Evidence.Timeout != 0 {
		return errors.New("verification role rejects storage, application, provider, target or tool authority")
	}
	if config.Jobs.MaxConnections < 1 || config.Jobs.MaxConnections > 100 {
		return errors.New("invalid verification database connection budget")
	}
	return app.ValidateVerificationWorkerConfig(verificationWorkerConfig(config))
}
