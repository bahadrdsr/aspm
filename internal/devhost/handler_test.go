package devhost

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDevelopmentHostBoundaries(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>aspm development fixture</title>")},
		"assets/app-hash.js": {Data: []byte("export const fixture = true;")},
	}
	handler, err := New(assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, method, path string
		status             int
	}{
		{"index", "GET", "/", 200},
		{"asset", "GET", "/assets/app-hash.js", 200},
		{"health", "GET", "/healthz", 200},
		{"unimplemented-work", "GET", "/api/v1/work", 503},
		{"unimplemented-finding", "GET", "/api/v1/findings/synthetic", 503},
		{"unimplemented-catalog", "GET", "/api/v1/integrations/catalog", 503},
		{"unknown-api-not-spa", "GET", "/api/unknown", 503},
		{"no-api-writes", "POST", "/api/v1/work", 405},
		{"no-static-writes", "POST", "/", 405},
		{"no-directory-listing", "GET", "/assets/", 404},
		{"missing-file", "GET", "/missing.js", 404},
	} {
		t.Run(item.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(item.method, item.path, nil))
			if recorder.Code != item.status {
				t.Fatalf("status %d, want %d: %s", recorder.Code, item.status, recorder.Body)
			}
			if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing static-content protection")
			}
			if strings.HasPrefix(item.path, "/api/") {
				var response struct {
					APIVersion string `json:"apiVersion"`
					Error      struct {
						Code      string `json:"code"`
						RequestID string `json:"requestId"`
						Retryable bool   `json:"retryable"`
					} `json:"error"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.APIVersion != apiVersion || response.Error.Code != "unavailable" || response.Error.RequestID == "" || response.Error.Retryable {
					t.Fatal("unimplemented APIs need an explicit non-ready error, not demo data")
				}
				if strings.Contains(recorder.Body.String(), `"items"`) {
					t.Fatal("a development host must not manufacture finding or catalog data")
				}
			}
		})
	}
}

func TestMissingBuildFailsExplicitly(t *testing.T) {
	if _, err := New(fstest.MapFS{}); err == nil {
		t.Fatal("missing frontend build must be an actionable startup error")
	}
}
