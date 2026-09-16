package execution

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/storagepolicy"
)

const materialPath = "etc/aspm/installer-credentials.json"

var credentialPaths = []string{materialPath, "etc/aspm/core.env", "etc/aspm/ingestion.env",
	"etc/aspm/reports.env", "etc/aspm/postgres.env", "etc/aspm/s3.json"}

type privateCredential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

type credentialMaterial struct {
	Version           int               `json:"version"`
	TargetFingerprint string            `json:"targetFingerprint"`
	ReferencesDigest  string            `json:"referencesDigest"`
	Core              privateCredential `json:"core"`
	Ingestion         privateCredential `json:"ingestion"`
	Operator          privateCredential `json:"operator"`
	PostgresPassword  string            `json:"postgresPassword"`
	BootstrapToken    string            `json:"bootstrapToken"`
	DatabaseURL       string            `json:"databaseUrl"`
}

func generatedSecret() string {
	var value [32]byte
	_, _ = rand.Read(value[:])
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func usableSecret(value string) bool {
	if len(value) < 8 || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (i *installer) applicationSecret(ctx context.Context, ref secretReference) (string, error) {
	if ref.Kind == "generated" {
		return generatedSecret(), nil
	}
	data, err := i.read(ctx, ref.Name, maxInputBytes)
	if err != nil {
		return "", ErrCredential
	}
	value := strings.TrimRight(string(data), "\r\n")
	if !usableSecret(value) {
		return "", ErrCredential
	}
	return value, nil
}

func (i *installer) materialize(ctx context.Context, p prepared) (credentialMaterial, error) {
	var material credentialMaterial
	references, _ := json.Marshal([]secretReference{p.config.Bootstrap.SecretRef, p.config.Postgres.CredentialRef, p.config.ObjectStore.CredentialRef})
	referencesDigest := digest(references)
	info, statErr := i.files.Stat(filepath.FromSlash(materialPath))
	if statErr == nil {
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxInputBytes {
			return material, ErrCredential
		}
		raw, err := i.read(ctx, materialPath, maxInputBytes)
		if err != nil || decode(raw, &material) != nil || material.Version != stateVersion ||
			material.TargetFingerprint != p.plan.TargetFingerprint ||
			material.ReferencesDigest != referencesDigest ||
			material.Core != (privateCredential{i.options.RoleKeys.Core.AccessKey, i.options.RoleKeys.Core.SecretKey}) ||
			material.Ingestion != (privateCredential{i.options.RoleKeys.Ingestion.AccessKey, i.options.RoleKeys.Ingestion.SecretKey}) ||
			!usableSecret(material.PostgresPassword) || !usableSecret(material.BootstrapToken) ||
			!usableSecret(material.Operator.AccessKey) || !usableSecret(material.Operator.SecretKey) {
			return credentialMaterial{}, ErrCredential
		}
	} else {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return material, ErrCredential
		}
		if p.input.Operation != "apply" {
			return material, ErrApproval
		}
		var err error
		material = credentialMaterial{Version: stateVersion, TargetFingerprint: p.plan.TargetFingerprint, ReferencesDigest: referencesDigest,
			Core:      privateCredential{i.options.RoleKeys.Core.AccessKey, i.options.RoleKeys.Core.SecretKey},
			Ingestion: privateCredential{i.options.RoleKeys.Ingestion.AccessKey, i.options.RoleKeys.Ingestion.SecretKey}}
		material.PostgresPassword, err = i.applicationSecret(ctx, p.config.Postgres.CredentialRef)
		if err != nil {
			return credentialMaterial{}, err
		}
		material.BootstrapToken, err = i.applicationSecret(ctx, p.config.Bootstrap.SecretRef)
		if err != nil {
			return credentialMaterial{}, err
		}
		if p.config.ObjectStore.CredentialRef.Kind == "generated" {
			material.Operator = privateCredential{AccessKey: generatedSecret(), SecretKey: generatedSecret()}
		} else {
			raw, err := i.read(ctx, p.config.ObjectStore.CredentialRef.Name, maxInputBytes)
			if err != nil || decode(raw, &material.Operator) != nil ||
				!usableSecret(material.Operator.AccessKey) || !usableSecret(material.Operator.SecretKey) {
				return credentialMaterial{}, ErrCredential
			}
		}
	}
	host := "aspm-aspm-postgres:5432"
	if p.config.Target.Kind == "linux" {
		host = "aspm-postgres:5432"
	}
	databaseURL := (&url.URL{Scheme: "postgres", User: url.UserPassword("aspm", material.PostgresPassword), Host: host,
		Path: "/aspm", RawQuery: "sslmode=disable"}).String()
	if material.DatabaseURL != "" && material.DatabaseURL != databaseURL {
		return credentialMaterial{}, ErrCredential
	}
	material.DatabaseURL = databaseURL
	policy, err := storagepolicy.BuildRuntime(storagepolicy.RuntimeConfig{
		Bucket: p.config.ObjectStore.Bucket, CoreRawPrefix: p.input.RuntimeRoles.Core.RawPrefix,
		IngestionRawPrefix: p.input.RuntimeRoles.Ingestion.RawPrefix, NormalizedPrefix: p.input.RuntimeRoles.Ingestion.NormalizedPrefix,
		Operator: storagepolicy.Credential{AccessKey: material.Operator.AccessKey, SecretKey: material.Operator.SecretKey},
		Core:     credential(i.options.RoleKeys.Core), Ingestion: credential(i.options.RoleKeys.Ingestion),
	})
	if err != nil {
		return credentialMaterial{}, ErrCredential
	}
	environments, err := roleEnvironments(p, material)
	if err != nil {
		return credentialMaterial{}, err
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return credentialMaterial{}, ErrCredential
	}
	if err := i.write(ctx, materialPath, raw); err != nil {
		return credentialMaterial{}, err
	}
	if err := i.write(ctx, "etc/aspm/s3.json", policy); err != nil {
		return credentialMaterial{}, err
	}
	for role, data := range environments {
		if err := i.write(ctx, "etc/aspm/"+role+".env", data); err != nil {
			return credentialMaterial{}, err
		}
	}
	return material, nil
}

func (m credentialMaterial) values() []string {
	return []string{m.Core.AccessKey, m.Core.SecretKey, m.Ingestion.AccessKey, m.Ingestion.SecretKey,
		m.Operator.AccessKey, m.Operator.SecretKey, m.PostgresPassword, m.BootstrapToken, m.DatabaseURL}
}

func environment(values map[string]string) ([]byte, error) {
	names := make([]string, 0, len(values))
	for name, value := range values {
		if name == "" || strings.ContainsAny(name, "=\r\n\x00") || !utf8.ValidString(value) ||
			strings.ContainsAny(value, "\r\n\x00") {
			return nil, ErrCredential
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var data strings.Builder
	for _, name := range names {
		data.WriteString(name)
		data.WriteByte('=')
		data.WriteString(values[name])
		data.WriteByte('\n')
	}
	return []byte(data.String()), nil
}

func roleEnvironments(p prepared, m credentialMaterial) (map[string][]byte, error) {
	postgres, err := environment(map[string]string{"POSTGRES_DB": "aspm", "POSTGRES_USER": "aspm", "POSTGRES_PASSWORD": m.PostgresPassword})
	if err != nil {
		return nil, err
	}
	result := map[string][]byte{
		"postgres": postgres,
	}
	for _, role := range []string{"core", "ingestion", "reports"} {
		service := "core-api"
		listen := "0.0.0.0:8080"
		if role == "ingestion" {
			service, listen = "ingestion-parser", "0.0.0.0:8081"
		} else if role == "reports" {
			service, listen = "background-worker", "0.0.0.0:8082"
		}
		values := map[string]string{"ASPM_DATABASE_URL": m.DatabaseURL, "ASPM_SCHEMA": "aspm",
			"ASPM_DB_MAX_CONNECTIONS": integer(p.config.Services[service].DBConnectionsPerReplica), "ASPM_LISTEN": listen}
		if role != "reports" {
			key, selected := m.Core, p.input.RuntimeRoles.Core
			if role == "ingestion" {
				key, selected = m.Ingestion, p.input.RuntimeRoles.Ingestion
				values["ASPM_S3_NORMALIZED_PREFIX"] = selected.NormalizedPrefix
			}
			values["ASPM_S3_ENDPOINT"] = "http://aspm-storage:8333"
			values["ASPM_S3_BUCKET"], values["ASPM_S3_REGION"] = p.config.ObjectStore.Bucket, "us-east-1"
			values["ASPM_S3_ACCESS_KEY"], values["ASPM_S3_SECRET_KEY"] = key.AccessKey, key.SecretKey
			values["ASPM_S3_PREFIX"], values["ASPM_S3_READINESS_KEY"] = selected.RawPrefix, selected.ReadinessKey
		}
		if role == "core" {
			values["ASPM_PUBLIC_ORIGIN"], values["ASPM_BOOTSTRAP_TOKEN"] = p.config.Access.BaseURL, m.BootstrapToken
			values["ASPM_ASSETS"] = "/app/web"
			values["ASPM_S3_PREPARE_READINESS"] = "true"
		}
		data, err := environment(values)
		if err != nil {
			return nil, err
		}
		result[role] = data
	}
	return result, nil
}
