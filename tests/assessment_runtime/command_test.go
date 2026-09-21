//go:build integration

package assessment_runtime

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

type commandRole struct {
	role    *runningRole
	command *exec.Cmd
	killed  bool
}

func startCommand(t *testing.T, f *fixture, c runtimeConfig) *commandRole {
	t.Helper()
	binary := required(t, "ASPM_ASSESSMENT_RUNTIME_BINARY")
	info, err := os.Stat(binary)
	must(t, "locate actual compiled cmd/assessment-worker", err)
	check(t, info.Mode().IsRegular(), "actual command binary is not a regular file")
	address := ownedAddress(t)
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = &f.log, &f.log
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") ||
			strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") || strings.HasPrefix(upper, "GH_") ||
			strings.HasPrefix(upper, "GITHUB_") || strings.HasPrefix(upper, "SLACK_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	settings := map[string]string{"ASPM_DATABASE_URL": c.Database.DatabaseURL, "ASPM_SCHEMA": c.Database.Schema, "ASPM_DB_MAX_CONNECTIONS": "1",
		"ASPM_LISTEN": address, "ASPM_ASSESSMENT_SCOPE": c.Scope, "ASPM_ASSESSMENT_CA_FILE": c.CAFile,
		"ASPM_ASSESSMENT_LEASE_DURATION": c.Lease.String(), "ASPM_ASSESSMENT_AUTHORIZATION_INTERVAL": c.AuthorizationInterval.String(),
		"ASPM_ASSESSMENT_REQUEST_TIMEOUT": c.RequestTimeout.String(), "ASPM_ASSESSMENT_REQUEST_WINDOW": c.Window.String(),
		"ASPM_ASSESSMENT_MAX_CONCURRENT": strconv.Itoa(c.MaxConcurrent), "ASPM_ASSESSMENT_REQUESTS_PER_WINDOW": strconv.Itoa(c.RequestsPerWindow),
		"ASPM_ASSESSMENT_MAX_INPUT_BYTES": strconv.Itoa(c.MaxInput), "ASPM_ASSESSMENT_MAX_OUTPUT_TOKENS": strconv.Itoa(c.MaxOutput),
		"ASPM_ASSESSMENT_MAX_RESPONSE_BYTES": strconv.FormatInt(c.MaxResponse, 10), "AWS_EC2_METADATA_DISABLED": "true"}
	if c.Key != nil {
		settings["ASPM_INTEGRATION_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString(c.Key)
	}
	for name, value := range settings {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	for _, entry := range cmd.Env {
		name, _, _ := strings.Cut(entry, "=")
		check(t, !strings.HasPrefix(name, "ASPM_S3_") && !strings.HasPrefix(name, "ASPM_COLLECTION_") && name != "ASPM_BOOTSTRAP_TOKEN" && name != "ASPM_ASSETS",
			"assessment command received operator/core/storage capability")
	}
	must(t, "launch only actual owned assessment-worker command", cmd.Start())
	r := &runningRole{done: make(chan struct{}), address: address, client: ownedClient(t, address)}
	result := &commandRole{role: r, command: cmd}
	go func() { r.err = cmd.Wait(); close(r.done) }()
	t.Cleanup(func() { result.kill(t) })
	awaitReady(t, r)
	return result
}
func (c *commandRole) kill(t *testing.T) {
	t.Helper()
	if !c.killed {
		_ = c.command.Process.Kill()
		c.killed = true
	}
	select {
	case <-c.role.done:
	case <-time.After(5 * time.Second):
		t.Fatal("exact owned command PID did not terminate")
	}
}
