package collection_deployment

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func requireIntegration(t *testing.T, env map[string]envVar) {
	t.Helper()
	requireReference(t, env, "ASPM_INTEGRATION_ENCRYPTION_KEY", "synthetic-integration-key", "encryption-key")
}

func collectionIsolation(t *testing.T, output rendered, input object) {
	t.Helper()
	collection := output.Roles["collection"]
	settings := input["collection"].(object)
	publisher := settings["s3Secret"].(object)
	allowed := map[string]bool{
		"ASPM_DATABASE_URL": true, "ASPM_SCHEMA": true, "ASPM_DB_MAX_CONNECTIONS": true, "ASPM_LISTEN": true,
		"ASPM_INTEGRATION_ENCRYPTION_KEY": true, "ASPM_COLLECTION_LEASE_DURATION": true, "ASPM_GITHUB_ENDPOINT": true,
	}
	for _, name := range collectionVariables() {
		allowed[name] = true
	}
	for _, item := range append(collection.Spec.Template.Spec.InitContainers, collection.Spec.Template.Spec.Containers...) {
		for name, variable := range environment(t, item) {
			if !allowed[name] {
				t.Fatalf("collection container %s received unrelated raw/AWS/bootstrap/assets/readiness authority %s", item.Name, name)
			}
			if variable.ValueFrom != nil {
				ref := variable.ValueFrom.Secret
				valid := name == "ASPM_DATABASE_URL" && ref.Name == "synthetic-operator-db" && ref.Key == "database-url" ||
					name == "ASPM_INTEGRATION_ENCRYPTION_KEY" && ref.Name == "synthetic-integration-key" && ref.Key == "encryption-key" ||
					name == "ASPM_COLLECTION_S3_ACCESS_KEY" && ref.Name == publisher["name"] && ref.Key == publisher["accessKeyKey"] ||
					name == "ASPM_COLLECTION_S3_SECRET_KEY" && ref.Name == publisher["name"] && ref.Key == publisher["secretKeyKey"]
				if !valid || ref.Optional {
					t.Fatal("collection received an unselected Secret authority or optional credential")
				}
			}
		}
	}
	for _, c := range output.Roles["core"].Spec.Template.Spec.InitContainers {
		env := environment(t, c)
		for name := range env {
			if strings.HasPrefix(name, "ASPM_COLLECTION_S3_") {
				t.Fatal("core collection evidence credentials belong to the core application, not new init containers")
			}
		}
	}
	noCollectionAuthority(t, output, "ingestion", "reports", "delivery")
	for _, role := range []string{"ingestion", "reports"} {
		for _, c := range append(output.Roles[role].Spec.Template.Spec.InitContainers, output.Roles[role].Spec.Template.Spec.Containers...) {
			for name, item := range environment(t, c) {
				if name == "ASPM_INTEGRATION_ENCRYPTION_KEY" ||
					item.ValueFrom != nil && item.ValueFrom.Secret.Name == "synthetic-integration-key" {
					t.Fatal("ingestion/reports must not receive the shared integration encryption key")
				}
			}
		}
	}
}

func requireCollection(t *testing.T, output rendered, input object, replicas int, resources object, lease, endpoint string) {
	t.Helper()
	main := mainContainer(t, output, "collection")
	pod := output.Roles["collection"]
	if !reflect.DeepEqual(main.Command, []string{"/app/bin/collection-worker"}) || len(main.Args) != 0 {
		t.Fatal("collection must run its separate actual command without a wrapper or inline credential arguments")
	}
	if pod.Spec.Replicas == nil || *pod.Spec.Replicas != replicas ||
		!reflect.DeepEqual(main.Resources, resources) {
		t.Fatal("collection must use its own selected replicas and resources")
	}
	if pod.Spec.Selector.MatchLabels["app.kubernetes.io/component"] != "collection" ||
		pod.Spec.Template.Metadata.Labels["app.kubernetes.io/component"] != "collection" {
		t.Fatal("collection Deployment/pod selectors do not isolate the worker role")
	}
	env := environment(t, main)
	requireReference(t, env, "ASPM_DATABASE_URL", "synthetic-operator-db", "database-url")
	requireIntegration(t, env)
	requireStorage(t, env, input, input["collection"].(object)["s3Secret"].(object))
	requireString(t, env, "ASPM_COLLECTION_LEASE_DURATION", lease)
	requireString(t, env, "ASPM_GITHUB_ENDPOINT", endpoint)
	requireIntegration(t, environment(t, mainContainer(t, output, "core")))
	requireStorage(t, environment(t, mainContainer(t, output, "core")), input, input["core"].(object)["collectionS3Secret"].(object))
	for _, p := range []struct {
		value probe
		path  string
	}{{main.StartupProbe, "/readyz"}, {main.ReadinessProbe, "/readyz"}, {main.LivenessProbe, "/healthz"}} {
		if p.value.HTTPGet.Path != p.path {
			t.Fatal("collection must use existing runtime readiness/liveness paths, not provider certification probes")
		}
		matches := false
		for _, port := range main.Ports {
			matches = matches || p.value.HTTPGet.Port == port.Name && port.Name != "" ||
				p.value.HTTPGet.Port == port.ContainerPort
		}
		if !matches {
			t.Fatal("collection HTTP probe port does not resolve to its declared container port")
		}
	}
	collectionIsolation(t, output, input)
}

func TestCollectionDeploymentHelmOptInAndSeparateRoleAuthorities(t *testing.T) {
	baseline := successfulRender(t, legacyValues(false))
	t.Run("default-and-explicit-off-controls", func(t *testing.T) {
		if len(baseline.Roles) != 3 {
			t.Fatal("omitted collection/delivery flags must leave exactly the existing three roles")
		}
		noCollectionAuthority(t, baseline, "core", "ingestion", "reports")
		input := legacyValues(false)
		input["collection"] = object{"enabled": false}
		off := successfulRender(t, input)
		if !reflect.DeepEqual(baseline.Documents, off.Documents) {
			t.Fatal("explicit collection=false changed the original default rendered resources")
		}
	})
	slack := successfulRender(t, legacyValues(true))
	t.Run("existing-delivery-control", func(t *testing.T) {
		if len(slack.Roles) != 4 {
			t.Fatal("existing Slack delivery opt-in was altered by the absent collection feature")
		}
		delivery := mainContainer(t, slack, "delivery")
		if !reflect.DeepEqual(delivery.Command, []string{"/app/bin/delivery-worker"}) {
			t.Fatal("original optional Slack command changed")
		}
		requireIntegration(t, environment(t, delivery))
		requireIntegration(t, environment(t, mainContainer(t, slack, "core")))
		requireString(t, environment(t, delivery), "ASPM_DELIVERY_LEASE_DURATION", "15s")
		requireString(t, environment(t, delivery), "ASPM_SLACK_ENDPOINT", "https://slack.com")
		noCollectionAuthority(t, slack, "core", "ingestion", "reports", "delivery")
	})
	t.Run("complete-core-read-only-preconfiguration", func(t *testing.T) {
		input := collectionValues(false, true)
		output := successfulRender(t, input)
		if _, present := output.Roles["collection"]; present {
			t.Fatal("core-only collection configuration auto-enabled a worker")
		}
		requireStorage(t, environment(t, mainContainer(t, output, "core")), input, input["core"].(object)["collectionS3Secret"].(object))
		requireIntegration(t, environment(t, mainContainer(t, output, "core")))
		noCollectionAuthority(t, output, "ingestion", "reports", "delivery")
		preserveLegacy(t, slack, output)
	})
	t.Run("enabled-native-defaults", func(t *testing.T) {
		input := collectionValues(true, false)
		output := successfulRender(t, input)
		if len(output.Roles) != 4 {
			t.Fatal("enabled collection must add one independent role and leave Slack delivery off")
		}
		requireCollection(t, output, input, 1, object{
			"requests": object{"cpu": "100m", "memory": "128Mi"},
			"limits":   object{"cpu": "1", "memory": "512Mi"},
		}, "15s", "https://api.github.com")
		preserveLegacy(t, baseline, output)
	})
	t.Run("selected-settings-coexist-with-delivery-distinct-key-tuples", func(t *testing.T) {
		input := collectionValues(true, true)
		selection := input["collection"].(object)
		selection["replicas"], selection["leaseDuration"], selection["githubEndpoint"] = 2, "30s", "https://gateway.synthetic.invalid/github"
		resources := object{"requests": object{"cpu": "137m", "memory": "143Mi"}, "limits": object{"cpu": "700m", "memory": "443Mi"}}
		selection["resources"] = resources
		// One Secret can contain two distinct reference tuples; identical tuples, not names alone, are forbidden.
		input["core"].(object)["collectionS3Secret"].(object)["name"] = "synthetic-collection-identities"
		selection["s3Secret"].(object)["name"] = "synthetic-collection-identities"
		output := successfulRender(t, input)
		if len(output.Roles) != 5 {
			t.Fatal("collection and delivery must coexist as independent opt-in roles")
		}
		requireCollection(t, output, input, 2, resources, "30s", "https://gateway.synthetic.invalid/github")
		preserveLegacy(t, slack, output)
	})
}

func TestCollectionDeploymentHelmRejectsMissingPartialAndUnsafeSelections(t *testing.T) {
	type invalidCase struct {
		name, path string
		change     func(object)
	}
	var cases []invalidCase
	for _, field := range []string{"endpoint", "bucket", "prefix", "region"} {
		cases = append(cases, invalidCase{"missing-storage-" + field, "collection.storage." + field, func(v object) {
			delete(v["collection"].(object)["storage"].(object), field)
		}})
	}
	for _, role := range []string{"core", "collection"} {
		selector := "s3Secret"
		if role == "core" {
			selector = "collectionS3Secret"
		}
		for _, field := range []string{"name", "accessKeyKey", "secretKeyKey"} {
			cases = append(cases, invalidCase{"missing-" + role + "-" + field, role + "." + selector + "." + field, func(v object) {
				delete(v[role].(object)[selector].(object), field)
			}})
		}
	}
	for _, field := range []string{"name", "key"} {
		cases = append(cases, invalidCase{"missing-integration-" + field, "integrationKeySecret." + field, func(v object) {
			delete(v["integrationKeySecret"].(object), field)
		}})
	}
	cases = append(cases,
		invalidCase{"missing-whole-integration-selector", "integrationKeySecret.name", func(v object) { delete(v, "integrationKeySecret") }},
		invalidCase{"off-partial-storage", "collection.storage.region", func(v object) {
			v["collection"].(object)["enabled"] = false
			delete(v["collection"].(object), "s3Secret")
			delete(v["collection"].(object)["storage"].(object), "region")
		}},
		invalidCase{"off-partial-reader", "core.collectionS3Secret.secretKeyKey", func(v object) {
			v["collection"].(object)["enabled"] = false
			delete(v["collection"].(object), "s3Secret")
			delete(v["core"].(object)["collectionS3Secret"].(object), "secretKeyKey")
		}},
		invalidCase{"off-partial-publisher", "collection.s3Secret.secretKeyKey", func(v object) {
			v["collection"].(object)["enabled"] = false
			delete(v["collection"].(object)["s3Secret"].(object), "secretKeyKey")
		}},
		invalidCase{"string-true", "collection.enabled", func(v object) { v["collection"].(object)["enabled"] = "true" }},
		invalidCase{"string-false", "collection.enabled", func(v object) { v["collection"].(object)["enabled"] = "false" }},
		invalidCase{"numeric-enabled", "collection.enabled", func(v object) { v["collection"].(object)["enabled"] = 1 }},
		invalidCase{"numeric-storage-bucket", "collection.storage.bucket", func(v object) {
			v["collection"].(object)["storage"].(object)["bucket"] = 17
		}},
		invalidCase{"numeric-lease", "collection.leaseDuration", func(v object) { v["collection"].(object)["leaseDuration"] = 15 }},
		invalidCase{"identical-reader-publisher-tuple", "collection.s3Secret", func(v object) {
			v["collection"].(object)["s3Secret"] = v["core"].(object)["collectionS3Secret"]
		}},
		invalidCase{"credentials-in-storage-endpoint", "collection.storage.endpoint", func(v object) {
			v["collection"].(object)["storage"].(object)["endpoint"] = "https://synthetic:not-an-authority@evidence.synthetic.invalid"
		}},
		invalidCase{"credentials-in-github-endpoint", "collection.githubEndpoint", func(v object) {
			v["collection"].(object)["githubEndpoint"] = "https://synthetic:not-an-authority@gateway.synthetic.invalid/github"
		}},
		invalidCase{"unsigned-provider-gateway", "collection.githubEndpoint", func(v object) {
			v["collection"].(object)["githubEndpoint"] = "http://gateway.synthetic.invalid/github"
		}},
	)
	for _, invalid := range cases {
		t.Run(invalid.name, func(t *testing.T) {
			input := collectionValues(true, false)
			invalid.change(input)
			_, diagnostic, err := render(t, input)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(diagnostic, invalid.path) {
				t.Errorf("actual Helm render must reject %s with that precise values path; error type %T", invalid.path, err)
			}
		})
	}
}
