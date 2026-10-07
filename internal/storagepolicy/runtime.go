package storagepolicy

import (
	"encoding/json"
	"strings"
)

// RuntimeConfig is the explicitly selected AI-disabled installer profile.
// Successful JSON is private operator material, never an application credential.
type RuntimeConfig struct {
	Bucket, CoreRawPrefix, IngestionRawPrefix, NormalizedPrefix, ArchivePrefix string
	Operator, Core, Ingestion, Retention                                       Credential `json:"-"`
}

func ValidateRuntimeCredentials(credentials ...Credential) error {
	seen := make(map[string]bool, len(credentials)*2)
	for _, credential := range credentials {
		for _, value := range []string{credential.AccessKey, credential.SecretKey} {
			if !validCredentialValue(value) || seen[value] {
				return ErrInvalid
			}
			seen[value] = true
		}
	}
	return nil
}

func ValidateRuntimeScopes(coreRaw, ingestionRaw, normalized string, archive ...string) error {
	prefixes := []string{coreRaw, normalized}
	if len(archive) > 1 {
		return ErrInvalid
	}
	if !validPrefix(coreRaw) || !validPrefix(ingestionRaw) || !validPrefix(normalized) {
		return ErrInvalid
	}
	if len(archive) == 1 {
		if !validPrefix(archive[0]) {
			return ErrInvalid
		}
		prefixes = append(prefixes, archive[0])
	}
	if coreRaw != ingestionRaw {
		return ErrInvalid
	}
	for index, prefix := range prefixes {
		for _, prior := range prefixes[:index] {
			if strings.HasPrefix(prefix, prior) || strings.HasPrefix(prior, prefix) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func ValidateRuntimeSelection(bucket, coreRaw, ingestionRaw, normalized string, archive ...string) error {
	if !validBucket(bucket) {
		return ErrInvalid
	}
	return ValidateRuntimeScopes(coreRaw, ingestionRaw, normalized, archive...)
}

func BuildRuntime(config RuntimeConfig) (json.RawMessage, error) {
	if !validBucket(config.Bucket) ||
		ValidateRuntimeCredentials(config.Core, config.Ingestion, config.Retention) != nil ||
		ValidateRuntimeScopes(config.CoreRawPrefix, config.IngestionRawPrefix,
			config.NormalizedPrefix, config.ArchivePrefix) != nil {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, 8)
	for _, credential := range []Credential{config.Operator, config.Core, config.Ingestion, config.Retention} {
		for _, value := range []string{credential.AccessKey, credential.SecretKey} {
			if !validCredentialValue(value) || seen[value] {
				return nil, ErrInvalid
			}
			seen[value] = true
		}
	}
	policy := seaweedPolicy{Identities: []identity{
		role("operator", config.Operator, "Admin"),
		role("core", config.Core, "Read:"+config.Bucket+"/"+config.CoreRawPrefix+"*",
			"Write:"+config.Bucket+"/"+config.CoreRawPrefix+"*", "Read:"+config.Bucket+"/"+config.ArchivePrefix+"*"),
		role("ingestion", config.Ingestion, "Read:"+config.Bucket+"/"+config.IngestionRawPrefix+"*", "Write:"+config.Bucket+"/"+config.NormalizedPrefix+"*"),
		role("retention", config.Retention, "Read:"+config.Bucket+"/"+config.CoreRawPrefix+"*",
			"Write:"+config.Bucket+"/"+config.CoreRawPrefix+"*", "Read:"+config.Bucket+"/"+config.ArchivePrefix+"*",
			"Write:"+config.Bucket+"/"+config.ArchivePrefix+"*"),
	}}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, ErrInvalid
	}
	return json.RawMessage(encoded), nil
}
