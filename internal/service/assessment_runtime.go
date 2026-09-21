package service

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
)

func assessmentEnvironment(config *Config) error {
	config.AssessmentScope = os.Getenv("ASPM_ASSESSMENT_SCOPE")
	encoded, supplied := os.LookupEnv("ASPM_INTEGRATION_ENCRYPTION_KEY")
	key, err := integrationEncryptionKey(encoded, supplied)
	if err != nil {
		return err
	}
	config.IntegrationEncryptionKey = key
	for _, setting := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{"LEASE_DURATION", &config.AssessmentLeaseDuration, 15 * time.Second},
		{"AUTHORIZATION_INTERVAL", &config.AssessmentAuthorizationInterval, 100 * time.Millisecond},
		{"REQUEST_TIMEOUT", &config.AssessmentRequestTimeout, 10 * time.Second},
		{"REQUEST_WINDOW", &config.AssessmentRequestWindow, time.Minute},
	} {
		*setting.value = setting.fallback
		if value, present := os.LookupEnv("ASPM_ASSESSMENT_" + setting.name); present {
			*setting.value, err = time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("ASPM_ASSESSMENT_%s must be an explicit bounded duration", setting.name)
			}
		}
	}
	for _, setting := range []struct {
		name     string
		value    *int
		fallback int
	}{
		{"MAX_CONCURRENT", &config.AssessmentMaxConcurrent, 1},
		{"REQUESTS_PER_WINDOW", &config.AssessmentRequestsPerWindow, 30},
		{"MAX_INPUT_BYTES", &config.AssessmentMaxInputBytes, 32768},
		{"MAX_OUTPUT_TOKENS", &config.AssessmentMaxOutputTokens, 1024},
	} {
		*setting.value = setting.fallback
		if value, present := os.LookupEnv("ASPM_ASSESSMENT_" + setting.name); present {
			number, err := strconv.ParseInt(value, 10, strconv.IntSize)
			if err != nil {
				return fmt.Errorf("ASPM_ASSESSMENT_%s must be an explicit bounded integer", setting.name)
			}
			*setting.value = int(number)
		}
	}
	config.AssessmentMaxResponseBytes = 65536
	if value, present := os.LookupEnv("ASPM_ASSESSMENT_MAX_RESPONSE_BYTES"); present {
		config.AssessmentMaxResponseBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("ASPM_ASSESSMENT_MAX_RESPONSE_BYTES must be an explicit bounded integer")
		}
	}
	config.AssessmentCAFile = os.Getenv("ASPM_ASSESSMENT_CA_FILE")
	config.AssessmentClient, err = newDirectProviderClient(config.AssessmentCAFile)
	if err != nil {
		return assessmentCAError()
	}
	config.AssessmentClient.Timeout = config.AssessmentRequestTimeout
	config.AssessmentClient.Transport.(*http.Transport).ResponseHeaderTimeout = config.AssessmentRequestTimeout
	return nil
}

func assessmentWorkerConfig(config Config) app.AssessmentWorkerConfig {
	return app.AssessmentWorkerConfig{
		Database: databaseConfig(config.Jobs), EncryptionKey: config.IntegrationEncryptionKey,
		WorkerID: config.WorkerID, Scope: config.AssessmentScope, Client: config.AssessmentClient,
		LeaseDuration: config.AssessmentLeaseDuration, AuthorizationInterval: config.AssessmentAuthorizationInterval,
		RequestTimeout: config.AssessmentRequestTimeout, RequestWindow: config.AssessmentRequestWindow,
		MaxConcurrent: config.AssessmentMaxConcurrent, RequestsPerWindow: config.AssessmentRequestsPerWindow,
		MaxInputBytes: config.AssessmentMaxInputBytes, MaxOutputTokens: config.AssessmentMaxOutputTokens,
		MaxResponseBytes: config.AssessmentMaxResponseBytes,
	}
}

func validateAssessmentConfig(config Config) error {
	if err := app.ValidateAssessmentWorkerConfig(assessmentWorkerConfig(config)); err != nil {
		return err
	}
	transport := config.AssessmentClient.Transport.(*http.Transport)
	if transport.Dial != nil && transport.DialContext == nil {
		return errors.New("assessment worker requires a context-aware dialer")
	}
	if policy := transport.TLSClientConfig; policy != nil && policy.MaxVersion > tls.VersionTLS13 {
		return errors.New("assessment worker requires a supported TLS version range")
	}
	return nil
}

func assessmentCAError() error {
	return errors.New("assessment CA file must be a nonempty certificate-only regular PEM file of at most 1 MiB")
}
