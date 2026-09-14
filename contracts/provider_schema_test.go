package contracts

import "testing"

func syntheticProvider(family string) document {
	protocol := "openai-v1"
	endpoint := "https://provider.example.invalid"
	auth := document{
		"mode":      "secret-reference",
		"secretRef": document{"kind": "environment", "name": "SYNTHETIC_PROVIDER_REFERENCE"},
	}
	policy := document{
		"mode": "approved-hosted", "approvalRef": "synthetic-outbound-policy",
		"workspaceIds": []string{"synthetic-workspace"}, "tasks": []string{"assessment"},
		"allowedDataClasses": []string{"synthetic"}, "allowedDestinations": []string{endpoint},
		"fallbackProfileIds": []string{},
	}
	if family == "anthropic" {
		protocol = "anthropic-messages"
	}
	if family == "local" {
		protocol, endpoint = "ollama", "http://127.0.0.1:11434"
		auth = document{"mode": "none"}
		policy["mode"] = "local-only"
		policy["allowedDestinations"] = []string{endpoint}
		policy["egressPolicy"] = "deny-hosted"
		delete(policy, "approvalRef")
	}
	result := document{
		"apiVersion": apiVersion, "kind": "ProviderProfile", "id": "synthetic-" + family,
		"family": family, "endpoint": endpoint, "protocol": protocol, "model": "synthetic-model-alias",
		"modelIdentityStatus": "unresolved-alias", "auth": auth, "policy": policy,
		"requestedCapabilities": []string{},
		"limits": document{
			"inputTokens": 4096, "outputTokens": 512, "concurrency": 1,
			"requestsPerMinute": 10, "tokensPerMinute": 10000, "timeoutSeconds": 30, "maxAttempts": 3,
		},
		"retention": document{
			"localPromptDays": 0, "localResultDays": 30, "providerPolicyStatus": "requested-not-confirmed",
		},
	}
	if family == "azure-foundry" {
		result["deployment"] = "synthetic-deployment"
	}
	return result
}

func TestAIProviderSchema(t *testing.T) {
	schema := compileSchema(t, "provider.schema.json")
	for _, family := range []string{"openai", "azure-foundry", "anthropic", "local"} {
		t.Run(family, func(t *testing.T) {
			base := syntheticProvider(family)
			assertValid(t, schema, base)
			for _, field := range []string{
				"apiVersion", "kind", "id", "family", "endpoint", "protocol", "model", "modelIdentityStatus",
				"auth", "policy", "requestedCapabilities", "limits", "retention",
				"auth.mode", "policy.mode", "policy.workspaceIds", "policy.tasks", "policy.allowedDataClasses",
				"policy.allowedDestinations", "policy.fallbackProfileIds",
				"limits.inputTokens", "limits.outputTokens", "limits.concurrency", "limits.requestsPerMinute",
				"limits.tokensPerMinute", "limits.timeoutSeconds", "limits.maxAttempts",
				"retention.localPromptDays", "retention.localResultDays", "retention.providerPolicyStatus",
			} {
				t.Run("missing-"+field, func(t *testing.T) {
					candidate := clone(t, base)
					remove(t, candidate, field)
					assertInvalid(t, schema, candidate)
				})
			}
			for _, mutation := range []struct {
				path  string
				value any
			}{
				{"apiVersion", "aspm/v99"}, {"family", "unapproved-provider"}, {"model", ""},
				{"protocol", "universal-ai-protocol"}, {"endpoint", "not-an-endpoint"},
				{"limits.concurrency", 0}, {"limits.maxAttempts", -1}, {"limits.timeoutSeconds", 0},
				{"limits.inputTokens", 0}, {"limits.outputTokens", 0}, {"limits.requestsPerMinute", 0},
				{"limits.tokensPerMinute", 0}, {"limits.concurrency", 0.5},
				{"policy.workspaceIds", []string{}}, {"policy.tasks", []string{}},
				{"policy.allowedDataClasses", []string{}}, {"policy.allowedDestinations", []string{}},
				{"retention.providerPolicyStatus", "universal-zero-retention-guaranteed"},
				{"auth.apiKey", "synthetic-not-a-real-key"}, {"arbitraryCommand", "not-executed"},
			} {
				t.Run(mutation.path, func(t *testing.T) {
					candidate := clone(t, base)
					put(t, candidate, mutation.path, mutation.value)
					assertInvalid(t, schema, candidate)
				})
			}
			if family == "local" {
				missingEgressPolicy := clone(t, base)
				remove(t, missingEgressPolicy, "policy.egressPolicy")
				assertInvalid(t, schema, missingEgressPolicy)
				openAICompatible := clone(t, base)
				put(t, openAICompatible, "protocol", "openai-compatible")
				assertValid(t, schema, openAICompatible)
				for _, mutation := range []struct {
					path, value string
				}{
					{"policy.mode", "approved-hosted"}, {"policy.egressPolicy", "allow-all"},
				} {
					candidate := clone(t, base)
					put(t, candidate, mutation.path, mutation.value)
					assertInvalid(t, schema, candidate)
				}
			} else {
				for _, field := range []string{"policy.approvalRef", "auth.secretRef"} {
					candidate := clone(t, base)
					remove(t, candidate, field)
					assertInvalid(t, schema, candidate)
				}
				for _, mutation := range []struct {
					path  string
					value any
				}{
					{"policy.approvalRef", ""}, {"policy.mode", "local-only"}, {"auth.mode", "none"},
					{"auth.secretRef", "synthetic-inline-secret"},
					{"auth.secretRef", document{"kind": "environment", "name": "SYNTHETIC_TOKEN", "value": "not-a-real-token"}},
				} {
					candidate := clone(t, base)
					put(t, candidate, mutation.path, mutation.value)
					assertInvalid(t, schema, candidate)
				}
			}
			if family == "azure-foundry" {
				missingDeployment := clone(t, base)
				delete(missingDeployment, "deployment")
				assertInvalid(t, schema, missingDeployment)
				workloadIdentity := clone(t, base)
				put(t, workloadIdentity, "auth", document{
					"mode": "workload-identity", "identityRef": "synthetic-workload-identity",
				})
				assertValid(t, schema, workloadIdentity)
			}
			if family == "anthropic" {
				wrongProtocol := clone(t, base)
				put(t, wrongProtocol, "protocol", "openai-v1")
				assertInvalid(t, schema, wrongProtocol)
			}
		})
	}
}
