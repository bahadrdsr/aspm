package storagepolicy

import (
	"encoding/json"
	"strings"
)

// RuntimeConfig is the explicitly selected AI-disabled installer profile.
// Successful JSON is private operator material, never an application credential.
type RuntimeConfig struct {
	Bucket, CoreRawPrefix, IngestionRawPrefix, NormalizedPrefix string
	Operator, Core, Ingestion                                   Credential `json:"-"`
}

func ValidateRuntimeCredentials(core, ingestion Credential) error {
	seen := make(map[string]bool, 4)
	for _, value := range []string{core.AccessKey, core.SecretKey, ingestion.AccessKey, ingestion.SecretKey} {
		if !validCredentialValue(value) || seen[value] {
			return ErrInvalid
		}
		seen[value] = true
	}
	return nil
}

func ValidateRuntimeScopes(coreRaw, ingestionRaw, normalized string) error {
	if !validPrefix(coreRaw) || !validPrefix(ingestionRaw) || !validPrefix(normalized) {
		return ErrInvalid
	}

	for _, raw := range []string{coreRaw, ingestionRaw} {
		if strings.HasPrefix(raw, normalized) || strings.HasPrefix(normalized, raw) {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateRuntimeSelection(bucket, coreRaw, ingestionRaw, normalized string) error {
	if !validBucket(bucket) {
		return ErrInvalid
	}
	return ValidateRuntimeScopes(coreRaw, ingestionRaw, normalized)
}

func BuildRuntime(config RuntimeConfig) (json.RawMessage, error) {
	if !validBucket(config.Bucket) ||
		ValidateRuntimeCredentials(config.Core, config.Ingestion) != nil ||
		ValidateRuntimeScopes(config.CoreRawPrefix, config.IngestionRawPrefix, config.NormalizedPrefix) != nil {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, 6)
	for _, credential := range []Credential{config.Operator, config.Core, config.Ingestion} {
		for _, value := range []string{credential.AccessKey, credential.SecretKey} {
			if !validCredentialValue(value) || seen[value] {
				return nil, ErrInvalid
			}
			seen[value] = true
		}
	}
	policy := seaweedPolicy{Identities: []identity{
		role("operator", config.Operator, "Admin"),
		role("core", config.Core, "Read:"+config.Bucket+"/"+config.CoreRawPrefix+"*", "Write:"+config.Bucket+"/"+config.CoreRawPrefix+"*"),
		role("ingestion", config.Ingestion, "Read:"+config.Bucket+"/"+config.IngestionRawPrefix+"*", "Write:"+config.Bucket+"/"+config.NormalizedPrefix+"*"),
	}}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, ErrInvalid
	}
	return json.RawMessage(encoded), nil
}
