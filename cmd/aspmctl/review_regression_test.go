package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const reviewOtherWriter = "configuration owned by another writer; preserve these exact bytes\n"

type reviewWizardInput struct {
	*strings.Reader
	destination string
	created     bool
	createErr   error
}

func (r *reviewWizardInput) Read(p []byte) (int, error) {
	if !r.created {
		r.created = true
		file, err := os.OpenFile(r.destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := io.WriteString(file, reviewOtherWriter)
			err = errors.Join(writeErr, file.Close())
		}
		r.createErr = err
		if err != nil {
			return 0, err
		}
	}
	return r.Reader.Read(p)
}

func TestM03ReviewInitPreservesWizardTimeWriter(t *testing.T) {
	directory := fmt.Sprintf(".aspmctl-review-%d", os.Getpid())
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	for _, overwrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-overwrite=%t", overwrite), func(t *testing.T) {
			destination := filepath.Join(directory, fmt.Sprintf("install-%t.json", overwrite))
			answers := strings.NewReader("linux\nlocalhost\nhttps://localhost\nyes\n")
			lateWriter := &reviewWizardInput{Reader: answers, destination: destination}
			var input io.Reader = lateWriter
			args := []string{"init", "--output", destination}
			if overwrite {
				if err := os.WriteFile(destination, []byte(reviewOtherWriter), 0o600); err != nil {
					t.Fatal(err)
				}
				input = answers
				args = append(args, "--overwrite")
			}
			var output bytes.Buffer
			err := run(args, input, &output)
			if !overwrite && (!lateWriter.created || lateWriter.createErr != nil) {
				t.Fatalf("wizard-time writer setup failed: created=%t err=%v", lateWriter.created, lateWriter.createErr)
			}
			saved, readErr := os.ReadFile(destination)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if overwrite {
				if err != nil || !json.Valid(saved) || bytes.Equal(saved, []byte(reviewOtherWriter)) {
					t.Fatalf("explicit overwrite did not save the confirmed configuration: %v", err)
				}
			} else {
				if err == nil {
					t.Error("init succeeded despite another writer creating the destination during the wizard")
				}
				if !bytes.Equal(saved, []byte(reviewOtherWriter)) {
					t.Error("init overwrote the other writer's bytes without --overwrite")
				}
				if strings.Contains(output.String(), "Saved ") {
					t.Error("conflicting init printed a save-success message")
				}
			}
		})
	}
}

func TestM03ReviewDoctorResponseByteLimit(t *testing.T) {
	const limit = 64 << 10
	const prefix, suffix = `{"status":"ok","padding":"`, `"}`
	exact := prefix + strings.Repeat("x", limit-len(prefix)-len(suffix)) + suffix
	if len(exact) != limit || !json.Valid([]byte(exact)) {
		t.Fatal("invalid exact-boundary fixture")
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
		wantErr    bool
		wantCalls  int32
	}{
		{"exactly-64KiB", exact, true, false, 2},
		{"oversized-valid-json", exact + " ", true, true, 1},
		{"valid-prefix-invalid-tail", exact + "invalid-tail", false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if json.Valid([]byte(tc.body)) != tc.valid || tc.wantErr && len(tc.body) <= limit {
				t.Fatal("invalid overflow fixture")
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
					t.Error("doctor requested an unexpected diagnostic route")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			var output bytes.Buffer
			err := doctor(server.URL, "", &output)
			if (err != nil) != tc.wantErr {
				t.Errorf("doctor error=%v, want failure=%t for %d bytes", err, tc.wantErr, len(tc.body))
			}
			success := strings.Contains(output.String(), "Process and dependency endpoints respond.")
			if success == tc.wantErr {
				t.Errorf("doctor final success message=%t, want %t", success, !tc.wantErr)
			}
			if calls.Load() != tc.wantCalls {
				t.Errorf("doctor diagnostic reads=%d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}
