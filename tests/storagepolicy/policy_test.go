package storagepolicy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

type Credential struct{ AccessKey, SecretKey string }
type Config struct {
	Bucket, RawPrefix, NormalizedPrefix, ApprovedPrefix string
	Operator, Core, Ingestion, AI                       Credential
}

var Production struct {
	Build func(Config) (json.RawMessage, error)
}

func configuration() Config {
	return Config{
		Bucket: "aspm-isolation", RawPrefix: "raw/team-a/", NormalizedPrefix: "normalized/team-a/", ApprovedPrefix: "approved/team-a/grant-1/",
		Operator:  Credential{"synthetic-operator-key", "synthetic-operator-secret"},
		Core:      Credential{"synthetic-core-key", "synthetic-core-secret"},
		Ingestion: Credential{"synthetic-ingestion-key", "synthetic-ingestion-secret"},
		AI:        Credential{"synthetic-ai-key", "synthetic-ai-secret"},
	}
}

func builder(t *testing.T) func(Config) (json.RawMessage, error) {
	t.Helper()
	if Production.Build == nil {
		t.Fatal("production role-policy binding missing; storage credentials must be scoped before deployment")
	}
	return Production.Build
}

func TestStoragePolicyRolesReceiveOnlyDeclaredCapabilities(t *testing.T) {
	config := configuration()
	raw, err := builder(t)(config)
	if err != nil {
		t.Fatalf("valid role policy failed (%T)", err)
	}
	var policy struct {
		Identities []struct {
			Name        string
			Actions     []string
			Credentials []Credential
		}
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal("role policy is not valid SeaweedFS identity JSON")
	}
	if len(policy.Identities) != 4 {
		t.Fatal("expected distinct operator, core, ingestion and AI identities")
	}
	expected := map[string][]string{
		"operator":  {"Admin"},
		"core":      {"Read:aspm-isolation/raw/team-a/*", "Write:aspm-isolation/raw/team-a/*", "Read:aspm-isolation/approved/team-a/grant-1/*", "Write:aspm-isolation/approved/team-a/grant-1/*"},
		"ingestion": {"Read:aspm-isolation/raw/team-a/*", "Write:aspm-isolation/normalized/team-a/*"},
		"ai":        {"Read:aspm-isolation/approved/team-a/grant-1/*"},
	}
	keys := map[string]Credential{"operator": config.Operator, "core": config.Core, "ingestion": config.Ingestion, "ai": config.AI}
	seen := map[string]bool{}
	for _, identity := range policy.Identities {
		want, exists := expected[identity.Name]
		if !exists || seen[identity.Name] || len(identity.Credentials) != 1 || identity.Credentials[0] != keys[identity.Name] {
			t.Fatal("role identity or explicit caller credential mapping changed")
		}
		seen[identity.Name] = true
		got := slices.Clone(identity.Actions)
		want = slices.Clone(want)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("role %s has missing or overbroad storage actions", identity.Name)
		}
		if identity.Name != "operator" {
			for _, action := range got {
				if action == "Admin" || strings.HasSuffix(action, ":aspm-isolation") || strings.HasPrefix(action, "List:") {
					t.Fatal("worker received broad bucket privileges to work around a probe")
				}
			}
		}
	}
}

func TestStoragePolicyMissingCredentialsCannotUseAmbientOrOperatorFallback(t *testing.T) {
	build := builder(t)
	t.Setenv("ASPM_S3_ACCESS_KEY", "synthetic-ambient-key")
	t.Setenv("ASPM_S3_SECRET_KEY", "synthetic-ambient-secret")
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Operator.AccessKey = "" }, func(c *Config) { c.Operator.SecretKey = "" },
		func(c *Config) { c.Core.AccessKey = "" }, func(c *Config) { c.Core.SecretKey = "" },
		func(c *Config) { c.Ingestion.AccessKey = "" }, func(c *Config) { c.Ingestion.SecretKey = "" },
		func(c *Config) { c.AI.AccessKey = "" }, func(c *Config) { c.AI.SecretKey = "" },
		func(c *Config) { c.AI.AccessKey = c.Operator.AccessKey },
		func(c *Config) { c.AI.SecretKey = c.Operator.SecretKey },
	} {
		input := configuration()
		mutate(&input)
		raw, err := build(input)
		if err == nil || len(raw) != 0 {
			t.Fatal("missing or reused role identity returned an actionable policy")
		}
	}
}

func TestStoragePolicyRejectsOverlappingOrEscapingPrefixes(t *testing.T) {
	build := builder(t)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Bucket = "bucket/*" },
		func(c *Config) { c.RawPrefix = "" },
		func(c *Config) { c.ApprovedPrefix = "*" },
		func(c *Config) { c.ApprovedPrefix = "approved/../raw/" },
		func(c *Config) { c.ApprovedPrefix = "/approved/" },
		func(c *Config) { c.NormalizedPrefix = c.RawPrefix },
		func(c *Config) { c.ApprovedPrefix = c.RawPrefix + "approved/" },
		func(c *Config) { c.RawPrefix = "approved/" },
	} {
		input := configuration()
		mutate(&input)
		raw, err := build(input)
		if err == nil || len(raw) != 0 {
			t.Fatal("overlapping/escaping/wildcard privilege scope returned an actionable policy")
		}
	}
}
