package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"

	contractdata "github.com/bahadrdsr/aspm/docs/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Plan struct {
	ID            string          `json:"id"`
	Configuration json.RawMessage `json:"configuration"`
}

var (
	once          sync.Once
	installSchema *jsonschema.Schema
	specError     error
	defaults      map[string]json.RawMessage
)

type offlineLoader struct{}

func (offlineLoader) Load(string) (any, error) {
	return nil, errors.New("external schema loading is disabled")
}

func loadSpecification() {
	source, err := contractdata.Files.ReadFile("v1alpha1/install.schema.json")
	if err != nil {
		specError = err
		return
	}
	var schema map[string]any
	if err = json.Unmarshal(source, &schema); err != nil {
		specError = err
		return
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(offlineLoader{})
	const id = "https://aspm.invalid/schemas/v1alpha1/install.schema.json"
	if err = compiler.AddResource(id, schema); err != nil {
		specError = err
		return
	}
	installSchema, specError = compiler.Compile(id)
	if specError != nil {
		return
	}
	source, err = contractdata.Files.ReadFile("v1alpha1/install.defaults.json")
	if err != nil {
		specError = err
		return
	}
	var document struct {
		Profiles []struct {
			ID            string
			Configuration json.RawMessage
		}
	}
	if err = json.Unmarshal(source, &document); err != nil {
		specError = err
		return
	}
	defaults = make(map[string]json.RawMessage)
	for _, profile := range document.Profiles {
		defaults[profile.ID] = profile.Configuration
	}
}

// Resolve previews configuration only. Its digest is not a target inspection,
// artifact signature or permission to run deployment commands.
func Resolve(ctx context.Context, input json.RawMessage) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if len(input) == 0 || len(input) > 1<<20 {
		return Plan{}, errors.New("installation configuration must be between 1 byte and 1 MiB")
	}
	var supplied map[string]any
	if err := json.Unmarshal(input, &supplied); err != nil || supplied == nil {
		return Plan{}, errors.New("installation configuration must be valid JSON")
	}
	if supplied["apiVersion"] != "aspm/v1alpha1" || supplied["kind"] != "Installation" {
		return Plan{}, errors.New("unsupported installation configuration version or kind")
	}
	target, ok := supplied["target"].(map[string]any)
	if !ok {
		return Plan{}, errors.New("an explicit deployment target is required")
	}
	kind, _ := target["kind"].(string)
	var required []string
	switch kind {
	case "linux":
		required = []string{"host"}
	case "kubernetes":
		required = []string{"context", "namespace"}
	default:
		return Plan{}, errors.New("target must be linux or kubernetes")
	}
	for _, field := range required {
		if value, ok := target[field].(string); !ok || value == "" {
			return Plan{}, fmt.Errorf("target.%s is required", field)
		}
	}
	access, ok := supplied["access"].(map[string]any)
	if !ok || access["baseURL"] == nil {
		return Plan{}, errors.New("access.baseURL is required")
	}
	once.Do(loadSpecification)
	if specError != nil {
		return Plan{}, fmt.Errorf("load the embedded installation specification: %w", specError)
	}
	var resolved map[string]any
	if err := json.Unmarshal(defaults[kind+"-small"], &resolved); err != nil {
		return Plan{}, errors.New("selected default profile is unavailable")
	}
	merge(resolved, supplied)
	resolved["target"] = target
	if err := installSchema.Validate(resolved); err != nil {
		// Validator diagnostics can contain rejected inline credential values.
		return Plan{}, errors.New("installation configuration violates the versioned schema; check required fields, safe exposure and secret references")
	}
	access = resolved["access"].(map[string]any)
	baseURL, err := url.Parse(access["baseURL"].(string))
	if err != nil || baseURL.Hostname() == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return Plan{}, errors.New("access.baseURL must be an uncredentialed HTTPS URL")
	}
	budget := resolved["databaseConnectionBudget"].(map[string]any)
	connections := budget["reservedMaintenance"].(float64)
	for _, value := range resolved["services"].(map[string]any) {
		service := value.(map[string]any)
		connections += service["replicas"].(float64) * service["dbConnectionsPerReplica"].(float64)
	}
	if connections > budget["max"].(float64) {
		return Plan{}, errors.New("configured service connections plus maintenance exceed the database connection budget")
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return Plan{}, err
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	sum := sha256.Sum256(encoded)
	return Plan{ID: "config-sha256:" + hex.EncodeToString(sum[:]), Configuration: encoded}, nil
}

func merge(base, supplied map[string]any) {
	for key, value := range supplied {
		object, isObject := value.(map[string]any)
		existing, hasObject := base[key].(map[string]any)
		if isObject && hasObject {
			if kind, exists := object["kind"]; exists && existing["kind"] != kind {
				base[key] = object
				continue
			}
			if object["mode"] == "external" {
				for _, field := range []string{"volumeName", "image", "seaweedfs"} {
					delete(existing, field)
				}
			}
			merge(existing, object)
		} else {
			base[key] = value
		}
	}
}
