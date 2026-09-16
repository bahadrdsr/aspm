package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/bahadrdsr/aspm/internal/install"
	"github.com/bahadrdsr/aspm/internal/storagepolicy"
)

type secretReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type serviceSelection struct {
	Replicas                int `json:"replicas"`
	DBConnectionsPerReplica int `json:"dbConnectionsPerReplica"`
}

type configuration struct {
	Release struct {
		Version string `json:"version"`
	} `json:"release"`
	Target struct {
		Kind, Host, Context, Namespace, StorageClass, IngressClass string
	} `json:"target"`
	Access struct {
		BaseURL        string          `json:"baseURL"`
		Exposure       string          `json:"exposure"`
		TLSMode        string          `json:"tlsMode"`
		CertificateRef secretReference `json:"certificateRef"`
		PrivateKeyRef  secretReference `json:"privateKeyRef"`
	} `json:"access"`
	Bootstrap struct {
		SecretRef secretReference `json:"secretRef"`
	} `json:"bootstrap"`
	Postgres struct {
		Mode          string          `json:"mode"`
		VolumeName    string          `json:"volumeName"`
		CredentialRef secretReference `json:"credentialRef"`
	} `json:"postgres"`
	ObjectStore struct {
		Mode, Bucket, VolumeName string
		CredentialRef            secretReference `json:"credentialRef"`
		Image                    struct{ Version, Digest string }
	} `json:"objectStore"`
	AI struct {
		Mode string `json:"mode"`
	} `json:"ai"`
	Proof struct {
		Enabled bool `json:"enabled"`
	} `json:"proof"`
	Services  map[string]serviceSelection `json:"services"`
	Artifacts struct {
		Mode              string `json:"mode"`
		SignatureRequired bool   `json:"signatureRequired"`
	} `json:"artifacts"`
}

var (
	dnsLabel    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	dataKey     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	contextName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:/-]{0,255}$`)
)

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func privateRoleDigest(keys RoleCredentials) string {
	data, _ := json.Marshal([]string{keys.Core.AccessKey, keys.Core.SecretKey, keys.Ingestion.AccessKey, keys.Ingestion.SecretKey})
	return digest(append([]byte("aspm-installer-role-identities-v1\x00"), data...))
}

func credential(key RoleCredential) storagepolicy.Credential {
	return storagepolicy.Credential{AccessKey: key.AccessKey, SecretKey: key.SecretKey}
}

func validSecretName(name string) bool {
	if len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func validateSelection(role RoleSelection) error {
	if !validSecretName(role.S3Secret.Name) || !dataKey.MatchString(role.S3Secret.AccessKeyKey) ||
		!dataKey.MatchString(role.S3Secret.SecretKeyKey) || role.S3Secret.AccessKeyKey == role.S3Secret.SecretKeyKey {
		return ErrCredential
	}
	if role.S3Secret.Name == commonSecretName || role.S3Secret.Name == policySecretName {
		return ErrCredential
	}
	for _, key := range []string{role.S3Secret.AccessKeyKey, role.S3Secret.SecretKeyKey} {
		switch key {
		case "database-url", "postgres-password", "bootstrap-token", "s3.json", "s3-access-key", "s3-secret-key":
			return ErrCredential
		}
	}
	if !strings.HasPrefix(role.ReadinessKey, role.RawPrefix) || len(role.ReadinessKey) <= len(role.RawPrefix) {
		return ErrUnsupported
	}
	if _, err := relativePath(role.ReadinessKey); err != nil || strings.ContainsAny(role.ReadinessKey, "*?[]{}") {
		return ErrUnsupported
	}
	return nil
}

func (i *installer) resolve(ctx context.Context, input Intent) (install.Plan, configuration, error) {
	if input.Operation != "apply" && input.Operation != "uninstall" ||
		(input.DeleteData && input.Operation != "uninstall") || input.DevelopmentPolicy != "" {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	if storagepolicy.ValidateRuntimeCredentials(credential(i.options.RoleKeys.Core), credential(i.options.RoleKeys.Ingestion)) != nil {
		return install.Plan{}, configuration{}, ErrCredential
	}
	if validateSelection(input.RuntimeRoles.Core) != nil || validateSelection(input.RuntimeRoles.Ingestion) != nil {
		return install.Plan{}, configuration{}, ErrCredential
	}
	if input.RuntimeRoles.Core.S3Secret.Name == input.RuntimeRoles.Ingestion.S3Secret.Name {
		return install.Plan{}, configuration{}, ErrCredential
	}
	if input.RuntimeRoles.Core.NormalizedPrefix != "" ||
		input.RuntimeRoles.Core.RawPrefix != input.RuntimeRoles.Ingestion.RawPrefix {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	preview, err := install.Resolve(ctx, input.Configuration)
	if err != nil {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	var config configuration
	if json.Unmarshal(preview.Configuration, &config) != nil {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	if !config.Artifacts.SignatureRequired || config.Artifacts.Mode != "online" || config.Postgres.Mode != "managed" ||
		config.ObjectStore.Mode != "managed" || config.AI.Mode != "disabled" || config.Proof.Enabled {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	if storagepolicy.ValidateRuntimeSelection(config.ObjectStore.Bucket, input.RuntimeRoles.Core.RawPrefix,
		input.RuntimeRoles.Ingestion.RawPrefix, input.RuntimeRoles.Ingestion.NormalizedPrefix) != nil {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	core, ingestion, reconciliation, reports := config.Services["core-api"], config.Services["ingestion-parser"],
		config.Services["ingestion-reconciler"], config.Services["background-worker"]
	if core.Replicas < 1 || ingestion.Replicas < 1 || reports.Replicas < 1 ||
		ingestion.Replicas != reconciliation.Replicas || core.DBConnectionsPerReplica < 2 ||
		core.DBConnectionsPerReplica != ingestion.DBConnectionsPerReplica ||
		core.DBConnectionsPerReplica != reconciliation.DBConnectionsPerReplica ||
		core.DBConnectionsPerReplica != reports.DBConnectionsPerReplica {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	if config.Target.Kind == "linux" {
		if config.Target.Host != "localhost" || i.options.LocalHost.OS != "linux" ||
			i.options.LocalHost.EUID != 0 || input.DeleteData ||
			core.Replicas != 1 || ingestion.Replicas != 1 || reports.Replicas != 1 ||
			config.ObjectStore.Bucket != "aspm-evidence" || config.Postgres.VolumeName != "aspm-postgres" ||
			config.ObjectStore.VolumeName != "aspm-evidence" {
			return install.Plan{}, configuration{}, ErrUnsupported
		}
	} else if !contextName.MatchString(config.Target.Context) || !dnsLabel.MatchString(config.Target.Namespace) {
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	for _, ref := range []secretReference{config.Bootstrap.SecretRef, config.Postgres.CredentialRef, config.ObjectStore.CredentialRef} {
		if err := i.referenceAvailable(ctx, ref); err != nil {
			return install.Plan{}, configuration{}, err
		}
	}
	if config.Access.TLSMode == "provided" {
		if err := i.referenceAvailable(ctx, config.Access.CertificateRef); err != nil {
			return install.Plan{}, configuration{}, err
		}
		if err := i.referenceAvailable(ctx, config.Access.PrivateKeyRef); err != nil {
			return install.Plan{}, configuration{}, err
		}
		return install.Plan{}, configuration{}, ErrUnsupported
	}
	return preview, config, nil
}

func (i *installer) referenceAvailable(ctx context.Context, ref secretReference) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ref.Name == "" {
		return ErrCredential
	}
	switch ref.Kind {
	case "generated":
		return nil
	case "file":
		path, err := relativePath(ref.Name)
		if err != nil {
			return ErrCredential
		}
		info, err := i.files.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxInputBytes {
			return ErrCredential
		}
		if i.nativeFiles && i.options.LocalHost.OS != "windows" && info.Mode().Perm()&0077 != 0 {
			return ErrCredential
		}
		return nil
	default:
		return ErrCredential
	}
}
