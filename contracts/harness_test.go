package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const apiVersion = "aspm/v1alpha1"

type document = map[string]any

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func() (any, error)
	value = func() (any, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch token {
		case json.Delim('{'):
			result := document{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("object key must be a string")
				}
				if _, exists := result[name]; exists {
					return nil, fmt.Errorf("duplicate JSON key %q", name)
				}
				result[name], err = value()
				if err != nil {
					return nil, err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return result, nil
		case json.Delim('['):
			result := []any{}
			for decoder.More() {
				item, err := value()
				if err != nil {
					return nil, err
				}
				result = append(result, item)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return result, nil
		default:
			return token, nil
		}
	}
	result, err := value()
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON value")
	}
	return result, nil
}

func object(t *testing.T, value any) document {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON object, got %T", value)
	}
	return result
}

func readDocument(t *testing.T, path string) document {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing M00 contract artifact: %s (coder must implement this artifact)", path)
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	value, err := decodeJSON(data)
	if err != nil {
		t.Fatalf("invalid JSON in %s: %v", path, err)
	}
	return object(t, value)
}

func artifact(t *testing.T, name, kind string) document {
	t.Helper()
	result := readDocument(t, filepath.Join("..", "docs", "contracts", "v1alpha1", name))
	equal(t, result, "apiVersion", apiVersion)
	equal(t, result, "kind", kind)
	return result
}

func get(t *testing.T, root document, path string) any {
	t.Helper()
	var current any = root
	for _, part := range strings.Split(path, ".") {
		item := object(t, current)
		var exists bool
		current, exists = item[part]
		if !exists {
			t.Fatalf("required field %q is missing", path)
		}
	}
	return current
}

func textAt(t *testing.T, root document, path string) string {
	t.Helper()
	value, ok := get(t, root, path).(string)
	if !ok || strings.TrimSpace(value) == "" {
		t.Fatalf("%s must be a nonempty string", path)
	}
	return value
}

func numberAt(t *testing.T, root document, path string) float64 {
	t.Helper()
	value, ok := get(t, root, path).(json.Number)
	if !ok {
		t.Fatalf("%s must be a JSON number", path)
	}
	number, err := value.Float64()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return number
}

func equal(t *testing.T, root document, path string, want any) {
	t.Helper()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := decodeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := get(t, root, path); !reflect.DeepEqual(got, normalized) {
		t.Errorf("%s = %v, want %v", path, got, normalized)
	}
}

func list(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("expected JSON array, got %T", value)
	}
	return result
}

func stringsAt(t *testing.T, root document, path string) []string {
	t.Helper()
	result := []string{}
	seen := map[string]bool{}
	for _, value := range list(t, get(t, root, path)) {
		item, ok := value.(string)
		if !ok || strings.TrimSpace(item) == "" || seen[item] {
			t.Fatalf("%s must contain unique nonempty strings, got %v", path, value)
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func requireSet(t *testing.T, root document, path string, exact bool, want ...string) {
	t.Helper()
	got := stringsAt(t, root, path)
	for _, item := range want {
		if !contains(got, item) {
			t.Errorf("%s is missing %q", path, item)
		}
	}
	if exact && len(got) != len(want) {
		t.Errorf("%s = %v; expected exactly %v", path, got, want)
	}
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func indexed(t *testing.T, root document, path string) map[string]document {
	t.Helper()
	result := map[string]document{}
	for _, value := range list(t, get(t, root, path)) {
		item := object(t, value)
		id := textAt(t, item, "id")
		if _, exists := result[id]; exists {
			t.Fatalf("%s has duplicate id %q", path, id)
		}
		result[id] = item
	}
	return result
}

func exactIDs(t *testing.T, items map[string]document, want ...string) {
	t.Helper()
	got := make([]string, 0, len(items))
	for id := range items {
		got = append(got, id)
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("IDs = %v, want exactly %v", got, expected)
	}
}

func requireFlags(t *testing.T, root document, paths []string, want bool) {
	t.Helper()
	for _, path := range paths {
		equal(t, root, path, want)
	}
}

func clone(t *testing.T, root document) document {
	t.Helper()
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return object(t, value)
}

func put(t *testing.T, root document, path string, value any) {
	t.Helper()
	parts := strings.Split(path, ".")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		parent = object(t, get(t, parent, part))
	}
	parent[parts[len(parts)-1]] = value
}

func remove(t *testing.T, root document, path string) {
	t.Helper()
	if index := strings.LastIndex(path, "."); index >= 0 {
		delete(object(t, get(t, root, path[:index])), path[index+1:])
		return
	}
	delete(root, path)
}

type denyExternalSchemas struct{}

func (denyExternalSchemas) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema loading forbidden: %s", location)
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	root := readDocument(t, filepath.Join("..", "docs", "contracts", "v1alpha1", name))
	equal(t, root, "$schema", "https://json-schema.org/draft/2020-12/schema")
	id := "https://aspm.invalid/schemas/v1alpha1/" + name
	equal(t, root, "$id", id)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(denyExternalSchemas{})
	if err := compiler.AddResource(id, root); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		t.Fatalf("compile self-contained %s: %v", name, err)
	}
	return schema
}

func assertValid(t *testing.T, schema *jsonschema.Schema, value document) {
	t.Helper()
	if err := schema.Validate(clone(t, value)); err != nil {
		t.Fatalf("valid synthetic configuration was rejected: %v", err)
	}
}

func assertInvalid(t *testing.T, schema *jsonschema.Schema, value document) {
	t.Helper()
	if err := schema.Validate(clone(t, value)); err == nil {
		t.Fatal("unsafe or incomplete configuration was accepted")
	}
}

func TestHarnessJSONDecoding(t *testing.T) {
	for _, source := range []string{`{"x":1,"x":2}`, `{"x":{"y":1,"y":2}}`, `{} {}`, `{"x":`, `[] false`} {
		if _, err := decodeJSON([]byte(source)); err == nil {
			t.Errorf("accepted invalid/ambiguous JSON: %s", source)
		}
	}
	value, err := decodeJSON([]byte(`{"x":[true,null,9007199254740993,{"y":"synthetic"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	root := object(t, value)
	copy := clone(t, root)
	copy["x"] = false
	if _, ok := root["x"].([]any); !ok {
		t.Fatal("test mutation changed the original fixture")
	}
}

func TestHarnessSchemaValidationOffline(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyExternalSchemas{})
	const id = "https://aspm.invalid/harness"
	schemaDoc := document{"type": "object", "required": []string{"ok"}, "properties": document{"ok": document{"const": true}}}
	if err := compiler.AddResource(id, clone(t, schemaDoc)); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	assertValid(t, schema, document{"ok": true})
	assertInvalid(t, schema, document{"ok": false})
	assertInvalid(t, schema, document{})
	if _, err := compiler.Compile("https://not-contacted.invalid/missing-schema"); err == nil {
		t.Fatal("external schema unexpectedly loaded")
	}
}
