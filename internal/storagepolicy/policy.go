package storagepolicy

import (
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid storage policy input")

type Credential struct {
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
}

func (Credential) String() string   { return "storage credential [redacted]" }
func (Credential) GoString() string { return "storage credential [redacted]" }

type Config struct {
	Bucket           string
	RawPrefix        string
	NormalizedPrefix string
	ApprovedPrefix   string
	Operator         Credential `json:"-"`
	Core             Credential `json:"-"`
	Ingestion        Credential `json:"-"`
	AI               Credential `json:"-"`
}

func (Config) String() string   { return "storage policy configuration [credentials redacted]" }
func (Config) GoString() string { return "storage policy configuration [credentials redacted]" }

type seaweedPolicy struct {
	Identities []identity `json:"identities"`
}

type identity struct {
	Name        string           `json:"name"`
	Credentials []wireCredential `json:"credentials"`
	Actions     []string         `json:"actions"`
}

type wireCredential struct {
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Build explicitly serializes a credential-bearing SeaweedFS 4.47 policy.
// The result is private operator configuration, not loggable diagnostic output.
// Failure always returns nil bytes and an error without caller-provided values.
func Build(config Config) (json.RawMessage, error) {
	if !validBucket(config.Bucket) {
		return nil, ErrInvalid
	}
	prefixes := []string{config.RawPrefix, config.NormalizedPrefix, config.ApprovedPrefix}
	for i, prefix := range prefixes {
		if !validPrefix(prefix) {
			return nil, ErrInvalid
		}
		for _, previous := range prefixes[:i] {
			if strings.HasPrefix(previous, prefix) || strings.HasPrefix(prefix, previous) {
				return nil, ErrInvalid
			}
		}
	}
	credentials := []Credential{config.Operator, config.Core, config.Ingestion, config.AI}
	seen := make(map[string]struct{}, 8)
	for _, credential := range credentials {
		// Also reject a secret reused as an access key: access-key disclosure
		// must never disclose another role's signing secret.
		for _, value := range []string{credential.AccessKey, credential.SecretKey} {
			if !validCredentialValue(value) {
				return nil, ErrInvalid
			}
			if _, reused := seen[value]; reused {
				return nil, ErrInvalid
			}
			seen[value] = struct{}{}
		}
	}
	raw := config.Bucket + "/" + config.RawPrefix + "*"
	normalized := config.Bucket + "/" + config.NormalizedPrefix + "*"
	approved := config.Bucket + "/" + config.ApprovedPrefix + "*"
	policy := seaweedPolicy{Identities: []identity{
		role("operator", config.Operator, "Admin"),
		role("core", config.Core, "Read:"+raw, "Write:"+raw, "Read:"+approved, "Write:"+approved),
		role("ingestion", config.Ingestion, "Read:"+raw, "Write:"+normalized),
		role("ai", config.AI, "Read:"+approved),
	}}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return nil, errors.New("storage policy encoding failed")
	}
	return json.RawMessage(encoded), nil
}

func role(name string, credential Credential, actions ...string) identity {
	return identity{
		Name: name, Actions: actions,
		Credentials: []wireCredential{{AccessKey: credential.AccessKey, SecretKey: credential.SecretKey}},
	}
}

func validBucket(bucket string) bool {
	return bucketPattern.MatchString(bucket) && !strings.Contains(bucket, "..") &&
		!strings.Contains(bucket, ".-") && !strings.Contains(bucket, "-.") && net.ParseIP(bucket) == nil
}

func validPrefix(prefix string) bool {
	if len(prefix) < 2 || len(prefix) > 1024 || !strings.HasSuffix(prefix, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if !segmentPattern.MatchString(part) {
			return false
		}
	}
	return true
}

func validCredentialValue(value string) bool {
	if len(value) == 0 || len(value) > 4096 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
