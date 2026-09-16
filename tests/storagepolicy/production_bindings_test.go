package storagepolicy

import (
	"encoding/json"

	"github.com/bahadrdsr/aspm/internal/storagepolicy"
)

func init() {
	Production.Build = func(config Config) (json.RawMessage, error) {
		return storagepolicy.Build(storagepolicy.Config{
			Bucket: config.Bucket, RawPrefix: config.RawPrefix,
			NormalizedPrefix: config.NormalizedPrefix, ApprovedPrefix: config.ApprovedPrefix,
			Operator: storagepolicy.Credential{
				AccessKey: config.Operator.AccessKey, SecretKey: config.Operator.SecretKey,
			},
			Core: storagepolicy.Credential{
				AccessKey: config.Core.AccessKey, SecretKey: config.Core.SecretKey,
			},
			Ingestion: storagepolicy.Credential{
				AccessKey: config.Ingestion.AccessKey, SecretKey: config.Ingestion.SecretKey,
			},
			AI: storagepolicy.Credential{
				AccessKey: config.AI.AccessKey, SecretKey: config.AI.SecretKey,
			},
		})
	}
}
