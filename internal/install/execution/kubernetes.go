package execution

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

const (
	commonSecretName = "aspm-secrets"
	policySecretName = "aspm-storage-policy"
	valuesPath       = "var/lib/aspm/installer/values.json"
)

type objectMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type secretObject struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Type       string            `json:"type"`
	Metadata   objectMetadata    `json:"metadata"`
	StringData map[string]string `json:"stringData"`
}

func (i *installer) applySecret(ctx context.Context, p prepared, name string, values map[string]string, material credentialMaterial) error {
	object := secretObject{APIVersion: "v1", Kind: "Secret", Type: "Opaque",
		Metadata: objectMetadata{Name: name, Namespace: p.config.Target.Namespace,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "aspmctl", "app.kubernetes.io/instance": "aspm"}},
		StringData: values}
	data, err := json.Marshal(object)
	if err != nil {
		return ErrCredential
	}
	_, err = i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p), "apply", "-f", "-"), Stdin: data}, material.values())
	return err
}

func (i *installer) kubernetesApply(ctx context.Context, p prepared, material *credentialMaterial) []executionStep {
	return []executionStep{
		{name: "namespace", run: func() error {
			object := struct {
				APIVersion string         `json:"apiVersion"`
				Kind       string         `json:"kind"`
				Metadata   objectMetadata `json:"metadata"`
			}{APIVersion: "v1", Kind: "Namespace", Metadata: objectMetadata{Name: p.config.Target.Namespace}}
			data, err := json.Marshal(object)
			if err != nil {
				return ErrUnsupported
			}
			_, err = i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p), "apply", "-f", "-"), Stdin: data}, material.values())
			return err
		}},
		{name: "database-bootstrap-secret", run: func() error {
			return i.applySecret(ctx, p, commonSecretName, map[string]string{
				"postgres-password": material.PostgresPassword, "database-url": material.DatabaseURL, "bootstrap-token": material.BootstrapToken,
			}, *material)
		}},
		{name: "core-storage-secret", run: func() error {
			selected := p.input.RuntimeRoles.Core.S3Secret
			return i.applySecret(ctx, p, selected.Name, map[string]string{
				selected.AccessKeyKey: material.Core.AccessKey, selected.SecretKeyKey: material.Core.SecretKey,
			}, *material)
		}},
		{name: "ingestion-storage-secret", run: func() error {
			selected := p.input.RuntimeRoles.Ingestion.S3Secret
			return i.applySecret(ctx, p, selected.Name, map[string]string{
				selected.AccessKeyKey: material.Ingestion.AccessKey, selected.SecretKeyKey: material.Ingestion.SecretKey,
			}, *material)
		}},
		{name: "operator-storage-policy", run: func() error {
			policy, err := i.read(ctx, "etc/aspm/s3.json", maxInputBytes)
			if err != nil {
				return ErrCredential
			}
			return i.applySecret(ctx, p, policySecretName, map[string]string{"s3.json": string(policy)}, *material)
		}},
		{name: "helm-upgrade", run: func() error {
			values, err := helmValues(p)
			if err != nil {
				return err
			}
			for _, secret := range material.values() {
				if strings.Contains(string(values), secret) {
					return ErrCredential
				}
			}
			if err := i.write(ctx, valuesPath, values); err != nil {
				return err
			}
			chart, err := i.chartSnapshot(ctx, p)
			if err != nil {
				return err
			}
			args := append(i.helmArgs(p), "upgrade", "--install", "aspm",
				chart,
				"--wait", "--timeout", "5m", "--values", filepath.Join(i.root, filepath.FromSlash(valuesPath)))
			_, err = i.command(ctx, p, Command{Tool: "helm", Args: args}, material.values())
			return err
		}},
	}
}

func helmValues(p prepared) ([]byte, error) {
	image := p.plan.Images["application"]
	digestAt := strings.LastIndex(image, "@sha256:")
	if digestAt < 0 {
		return nil, ErrBundle
	}
	colon := strings.LastIndex(image[:digestAt], ":")
	if colon < strings.LastIndex(image[:digestAt], "/") {
		return nil, ErrBundle
	}
	type roleValues struct {
		Replicas         int             `json:"replicas"`
		PrepareReadiness bool            `json:"prepareReadiness,omitempty"`
		S3Secret         SecretSelection `json:"s3Secret"`
		RawPrefix        string          `json:"rawPrefix"`
		ReadinessKey     string          `json:"readinessKey"`
		NormalizedPrefix string          `json:"normalizedPrefix,omitempty"`
	}
	core, ingestion := p.input.RuntimeRoles.Core, p.input.RuntimeRoles.Ingestion
	values := struct {
		ExistingSecret string `json:"existingSecret"`
		PublicOrigin   string `json:"publicOrigin"`
		Image          struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"image"`
		Core      roleValues `json:"core"`
		Ingestion roleValues `json:"ingestion"`
		Reports   struct {
			Replicas int `json:"replicas"`
		} `json:"reports"`
		Database struct {
			Managed                  bool   `json:"managed"`
			Image                    string `json:"image"`
			Name                     string `json:"name"`
			User                     string `json:"user"`
			Schema                   string `json:"schema"`
			MaxConnectionsPerReplica int    `json:"maxConnectionsPerReplica"`
			StorageClass             string `json:"storageClass"`
		} `json:"database"`
		Storage struct {
			Managed      bool   `json:"managed"`
			Image        string `json:"image"`
			Bucket       string `json:"bucket"`
			Region       string `json:"region"`
			StorageClass string `json:"storageClass"`
			PolicySecret struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"policySecret"`
		} `json:"storage"`
	}{ExistingSecret: commonSecretName, PublicOrigin: p.config.Access.BaseURL,
		Core: roleValues{Replicas: p.config.Services["core-api"].Replicas, PrepareReadiness: true,
			S3Secret: core.S3Secret, RawPrefix: core.RawPrefix, ReadinessKey: core.ReadinessKey},
		Ingestion: roleValues{Replicas: p.config.Services["ingestion-parser"].Replicas, S3Secret: ingestion.S3Secret,
			RawPrefix: ingestion.RawPrefix, ReadinessKey: ingestion.ReadinessKey, NormalizedPrefix: ingestion.NormalizedPrefix}}
	values.Image.Repository, values.Image.Tag = image[:colon], image[colon+1:]
	values.Reports.Replicas = p.config.Services["background-worker"].Replicas
	values.Database.Managed, values.Database.Image = true, p.plan.Images["postgres"]
	values.Database.Name, values.Database.User, values.Database.Schema = "aspm", "aspm", "aspm"
	values.Database.MaxConnectionsPerReplica = p.config.Services["core-api"].DBConnectionsPerReplica
	values.Database.StorageClass = p.config.Target.StorageClass
	values.Storage.Managed, values.Storage.Image, values.Storage.Bucket, values.Storage.Region = true, p.plan.Images["storage"], p.config.ObjectStore.Bucket, "us-east-1"
	values.Storage.StorageClass = p.config.Target.StorageClass
	values.Storage.PolicySecret.Name, values.Storage.PolicySecret.Key = policySecretName, "s3.json"
	data, err := json.Marshal(values)
	if err != nil {
		return nil, ErrUnsupported
	}
	return data, nil
}

func (i *installer) kubernetesUninstall(ctx context.Context, p prepared, material *credentialMaterial, record *stateRecord) []executionStep {
	steps := []executionStep{{name: "helm-uninstall", run: func() error {
		_, err := i.command(ctx, p, Command{Tool: "helm", Args: append(i.helmArgs(p), "uninstall", "aspm", "--wait", "--timeout", "5m", "--ignore-not-found")}, material.values())
		return err
	}}}
	if !p.input.DeleteData {
		return steps
	}
	return append(steps, executionStep{name: "delete-owned-data", run: func() error {
		result, err := i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p), "get", "pvc", "-o", "json")}, material.values())
		if err != nil {
			return err
		}
		var list struct {
			Items []struct{ Metadata struct{ Name string } } `json:"items"`
		}
		if json.Unmarshal(result.Stdout, &list) != nil || len(list.Items) > 1000 {
			return ErrCommand
		}
		for _, owned := range []string{"data-aspm-aspm-postgres-0", "data-aspm-aspm-storage-0"} {
			present := slices.ContainsFunc(list.Items, func(item struct{ Metadata struct{ Name string } }) bool { return item.Metadata.Name == owned })
			if present {
				if _, err := i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p), "delete", "pvc", owned, "--ignore-not-found=true")}, material.values()); err != nil {
					return err
				}
			}
		}
		for _, name := range []string{commonSecretName, policySecretName, p.input.RuntimeRoles.Core.S3Secret.Name, p.input.RuntimeRoles.Ingestion.S3Secret.Name} {
			if _, err := i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p), "delete", "secret", name, "--ignore-not-found=true")}, material.values()); err != nil {
				return err
			}
		}
		// Cluster data is deleted only by this separately approved operation.
		// Keep private material for an auditable, nonrotating recovery path.
		record.State.SecretFiles = append([]string(nil), credentialPaths...)
		return nil
	}})
}
