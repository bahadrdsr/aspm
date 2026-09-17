package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
)

func collectionStorageEnvironment(required bool) (*app.StorageConfig, error) {
	storage := &app.StorageConfig{}
	settings := []struct {
		name  string
		value *string
	}{
		{"ASPM_COLLECTION_S3_ENDPOINT", &storage.Endpoint},
		{"ASPM_COLLECTION_S3_BUCKET", &storage.Bucket},
		{"ASPM_COLLECTION_S3_PREFIX", &storage.Prefix},
		{"ASPM_COLLECTION_S3_REGION", &storage.Region},
		{"ASPM_COLLECTION_S3_ACCESS_KEY", &storage.AccessKey},
		{"ASPM_COLLECTION_S3_SECRET_KEY", &storage.SecretKey},
	}
	provided := false
	for _, setting := range settings {
		value, present := os.LookupEnv(setting.name)
		*setting.value = value
		provided = provided || present
	}
	if !provided && !required {
		return nil, nil
	}
	for _, setting := range settings {
		if strings.TrimSpace(*setting.value) == "" {
			return nil, fmt.Errorf("collection storage requires explicit %s", setting.name)
		}
	}
	if err := app.ValidateStorageConfig(*storage); err != nil {
		return nil, err
	}
	return storage, nil
}

func collectionEnvironment(config *Config) error {
	storage, err := collectionStorageEnvironment(true)
	if err != nil {
		return err
	}
	config.CollectionStorage = storage
	config.IntegrationEncryptionKey, err = integrationEncryptionKey(os.Getenv("ASPM_INTEGRATION_ENCRYPTION_KEY"), true)
	if err != nil {
		return err
	}
	config.CollectionLeaseDuration = 15 * time.Second
	if value := os.Getenv("ASPM_COLLECTION_LEASE_DURATION"); value != "" {
		config.CollectionLeaseDuration, err = time.ParseDuration(value)
		if err != nil {
			return errors.New("collection lease duration is invalid")
		}
	}
	config.CollectionLimits, err = app.NormalizeCollectionLimits(connectors.Limits{})
	if err != nil {
		return err
	}
	config.GitHubEndpoint, err = app.ValidateCollectionGateway(os.Getenv("ASPM_GITHUB_ENDPOINT"))
	if err != nil {
		return err
	}
	config.CollectionCAFile = os.Getenv("ASPM_COLLECTION_CA_FILE")
	config.CollectionClient, err = newProviderClient(config.GitHubEndpoint, config.CollectionCAFile)
	return err
}

func collectionWorkerConfig(config Config) app.CollectionWorkerConfig {
	var storage app.StorageConfig
	if config.CollectionStorage != nil {
		storage = *config.CollectionStorage
	}
	return app.CollectionWorkerConfig{
		Database: databaseConfig(config.Jobs), Storage: storage,
		EncryptionKey: config.IntegrationEncryptionKey, WorkerID: config.WorkerID,
		LeaseDuration: config.CollectionLeaseDuration, GitHubEndpoint: config.GitHubEndpoint,
		Client: config.CollectionClient, Limits: config.CollectionLimits,
	}
}

func validateCollectionConfig(config Config) error {
	if config.CollectionStorage == nil {
		return errors.New("collection storage capability is required")
	}
	return app.ValidateCollectionWorkerConfig(collectionWorkerConfig(config))
}
