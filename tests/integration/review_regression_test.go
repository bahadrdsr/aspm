//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestM02ReviewAttemptCapsSurviveReopen(t *testing.T) {
	requireJobs(t)
	for _, tc := range []struct {
		name, phase                      string
		jobMax, originalMax, reopenedMax int
	}{
		{"retry-wait-lower-cap", "retry-wait", 3, 3, 1},
		{"expired-lease-lower-cap", "expired-lease", 3, 3, 1},
		{"retryable-failure-lower-cap", "fail", 3, 3, 1},
		{"raised-cap-preserves-job-limit", "expired-lease", 1, 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.jobs.MaxAttempts = tc.originalMax
			original := f.openJobs(t, "-review-original")
			request := f.envelope(t)
			request.MaxAttempts = tc.jobMax
			queued := enqueue(t, f, original, request)
			first := claim(t, f, original, "review-original", f.jobs.MaxLease)
			if first.JobID != queued.JobID || first.Attempt != 1 || !reflect.DeepEqual(first.Envelope, request) {
				t.Fatal("first attempt changed the acknowledged identity or envelope")
			}
			failure := Failure{Code: "review-transient", Message: "Controlled retry-cap fixture", Retryable: true}
			eligibleAt := first.ExpiresAt
			if tc.phase == "retry-wait" {
				requireOK(t, "schedule retry under original cap", original.Fail(f.ctx, first, failure))
				waiting := snapshot(t, f, original, queued.JobID)
				if waiting.State != "retry-wait" || waiting.Attempts != 1 {
					t.Fatal("fixture did not establish the original retry-wait attempt")
				}
				eligibleAt = waiting.AvailableAt
			}
			requireOK(t, "close original cap client", original.Close())
			f.jobs.MaxAttempts = tc.reopenedMax
			reopened := f.openJobs(t, "-review-reopened")
			if tc.phase == "fail" {
				requireOK(t, "fail current lease through lower-cap client", reopened.Fail(f.ctx, first, failure))
				failed := snapshot(t, f, reopened, queued.JobID)
				if failed.State != "failed" {
					t.Errorf("retryable failure under client max=%d/job max=%d scheduled %q; want failed",
						tc.reopenedMax, tc.jobMax, failed.State)
				}
				eligibleAt = failed.AvailableAt
			}
			f.after(t, eligibleAt.Add(20*time.Millisecond))
			extra, err := reopened.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "review-no-extra", LeaseFor: f.jobs.MaxLease})
			if !errors.Is(err, ErrNoJob) {
				t.Errorf("exhausted client max=%d/job max=%d granted attempt=%d (error %T); want ErrNoJob",
					tc.reopenedMax, tc.jobMax, extra.Attempt, err)
			}
			requireOK(t, "close cap enforcement client", reopened.Close())
			observer := f.openJobs(t, "-review-observer")
			final := snapshot(t, f, observer, queued.JobID)
			if final.State != "failed" || final.Attempts != 1 || final.Receipt != nil {
				t.Errorf("exhaustion must persist failed with one attempt and no receipt; state=%q attempts=%d receipt=%t",
					final.State, final.Attempts, final.Receipt != nil)
			}
			if !reflect.DeepEqual(final.Envelope, request) {
				t.Error("cap change altered the acknowledged immutable envelope")
			}
			if final.LastFailure == nil ||
				(tc.phase == "expired-lease" && final.LastFailure.Code != "lease-expired") ||
				(tc.phase == "fail" && final.LastFailure.Code != failure.Code) {
				t.Error("exhaustion lost the failure cause or required lease-expired diagnostic")
			}
		})
	}
}

func TestM02ReviewQuadletInvokesIngestion(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "quadlet", "aspm-ingestion@.container"))
	requireOK(t, "read declared ingestion Quadlet", err)
	settings := make(map[string]string)
	section := ""
	for _, line := range strings.Split(string(unit), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
		} else if section == "[Container]" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, ";") {
			if key, value, ok := strings.Cut(line, "="); ok {
				settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
	if settings["Image"] == "" {
		t.Fatal("ingestion Quadlet must declare its image")
	}
	for _, path := range []string{filepath.Join("..", "..", "Containerfile"), filepath.Join("..", "..", "deploy", "Containerfile.runtime")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			requireOK(t, "read declared image build artifact", err)
			metadata := make(map[string]string)
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				key := strings.ToUpper(fields[0])
				switch key {
				case "FROM":
					clear(metadata)
				case "ENTRYPOINT", "CMD":
					metadata[key] = strings.TrimSpace(line[len(fields[0]):])
				}
			}
			var entrypoint, command []string
			raw, declared := metadata["ENTRYPOINT"]
			if !declared {
				t.Fatal("source contract requires an explicit final-stage JSON ENTRYPOINT, not inferred base-image metadata")
			}
			requireOK(t, "decode runtime image ENTRYPOINT", json.Unmarshal([]byte(raw), &entrypoint))
			if raw, declared := metadata["CMD"]; declared {
				requireOK(t, "decode runtime image CMD", json.Unmarshal([]byte(raw), &command))
			}
			literalArgs := func(value string) []string {
				t.Helper()
				if strings.ContainsAny(value, "\"'\\$%") {
					t.Fatal("source contract requires literal Quadlet arguments, not shell/systemd interpretation")
				}
				return strings.Fields(value)
			}
			if override, present := settings["Entrypoint"]; present {
				entrypoint, command = nil, nil
				if strings.HasPrefix(override, "[") {
					requireOK(t, "decode Quadlet Entrypoint override", json.Unmarshal([]byte(override), &entrypoint))
				} else if override != "" {
					entrypoint = literalArgs(override)
					if len(entrypoint) != 1 {
						t.Fatal("multi-argument Quadlet Entrypoint must use JSON")
					}
				}
			}
			if exec, present := settings["Exec"]; present {
				command = literalArgs(exec)
			}
			// Source-contract composition only; this does not execute Linux/systemd.
			effective := append(entrypoint, command...)
			if len(effective) == 0 || effective[0] != "/app/bin/ingestion" {
				t.Fatalf("Quadlet with %s resolves argv=%q; want ingestion executable, not an argument to core-api",
					filepath.Base(path), effective)
			}
		})
	}
}
