//go:build integration && teams_workflows

package jira_work_items

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const teamsPublishedCommit = "21080a3cbe04957e4ff55a6236d13bb4fde0fcc2"
const teamsPublishedClosure = "57acf410cb798843f3dc1b09b1c6a83ecbfd2ff260118d9c75a1960b04f5b6a6"
const teamsPublishedCoreHelper = "40d4acf4b14ed7045f1030353eaa51ac8dd43ac49d651836e6847221c8ef2825"

type teamsPublished struct {
	publishedReceipt
	ClosureSHA256, WorkerHelperSHA256 string
	workerQueries                    int32
}

func teamsChildEnvironment() []string {
	result := []string{}
	for _, value := range safeChildEnvironment() {
		name, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "TEAMS_") && !strings.HasPrefix(strings.ToUpper(name), "ASMP_") {
			result = append(result, value)
		}
	}
	return result
}

func (h *harness) openTeamsPublished() *teamsPublished {
	h.t.Helper()
	path := os.Getenv("ASPM_TEAMS_V10_RECEIPT")
	check(h.t, path != "", "BLOCKED: exact published V10 build receipt required; authoring did not build it")
	data, err := os.ReadFile(path)
	must(h.t, "read private pinned V10 build receipt", err)
	build := decoded[teamsPublished](h.t, data)
	check(h.t, build.Origin == teamsPublishedCommit && build.ClosureSHA256 == teamsPublishedClosure &&
		build.ExitCode == 0 && len(build.Files) == 86 && !build.BusinessSQLSeeds && !build.ProviderCalls && !build.DataExecution,
		"historical input is not the exact nonexecuted published production build")
	cacheRoot, err := filepath.Abs(filepath.Join("..", "..", ".cache", "teams-workflows", "published-v10"))
	must(h.t, "resolve owned pinned cache", err)
	relative, err := filepath.Rel(cacheRoot, build.Executable)
	must(h.t, "scope pinned executable to ignored owned cache", err)
	check(h.t, filepath.IsAbs(build.Executable) && !strings.HasPrefix(relative, "..") &&
		filepath.Base(build.Executable) == "owned-v10-core.exe", "published executable is outside the selected cache")
	binary, err := os.ReadFile(build.Executable)
	must(h.t, "read pinned V10 executable", err)
	check(h.t, digest(binary) == build.ExecutableSHA256, "published V10 executable drifted")
	var manifest strings.Builder
	previous := ""
	for _, entry := range build.Files {
		check(h.t, !filepath.IsAbs(entry.Path) && !strings.Contains(entry.Path, "..") &&
			filepath.Clean(entry.Path) == entry.Path && previous < entry.Path,
			"published source identity order/path differs")
		data, err := os.ReadFile(filepath.Join(filepath.Dir(build.Executable), entry.Path))
		must(h.t, "read pinned closure member", err)
		check(h.t, digest(data) == entry.SHA256, "pinned source SHA256 differs")
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
		check(h.t, hex.EncodeToString(blob[:]) == entry.GitBlob, "pinned bytes do not match their immutable Git blob")
		manifest.WriteString(entry.Path + "\x00" + entry.GitBlob + "\n")
		previous = entry.Path
	}
	check(h.t, digest([]byte(manifest.String())) == teamsPublishedClosure, "closure is not the independently pinned path/blob set")
	for _, helper := range []struct{ source, cached, hash string }{
		{filepath.Join("testdata", "published-core.main.go.txt"), "main.go", teamsPublishedCoreHelper},
		{filepath.Join("teams", "published-worker.main.go.txt"), "worker.go", build.WorkerHelperSHA256},
	} {
		source, err := os.ReadFile(helper.source)
		must(h.t, "read forwarding helper control", err)
		cached, err := os.ReadFile(filepath.Join(filepath.Dir(build.Executable), "cmd", "owned-v10-core", helper.cached))
		must(h.t, "read compiled forwarding helper input", err)
		check(h.t, digest(source) == helper.hash && bytes.Equal(source, cached), "published forwarding helper drifted")
	}
	check(h.t, build.HelperSHA256 == teamsPublishedCoreHelper, "original published core helper was amended")
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	h.t.Cleanup(cancel)
	command := exec.CommandContext(ctx, build.Executable)
	command.Env, command.Stderr = teamsChildEnvironment(), &h.log
	stdin, err := command.StdinPipe()
	must(h.t, "open private V10 input pipe", err)
	stdout, err := command.StdoutPipe()
	must(h.t, "open bounded V10 output pipe", err)
	must(h.t, "start actual pinned V10 core", command.Start())
	done := make(chan error, 1)
	var waitOnce sync.Once
	wait := func() { waitOnce.Do(func() { go func() { done <- command.Wait() }() }) }
	h.t.Cleanup(func() { _ = command.Process.Kill(); cancel(); wait() })
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(io.LimitReader(stdout, 16<<10))
	input := object{
		"DatabaseURL": h.cfg.DatabaseURL, "Schema": h.cfg.Schema, "ApplicationName": h.cfg.ApplicationName+"-v10",
		"BootstrapToken": h.cfg.BootstrapToken, "AssessmentScope": h.cfg.AssessmentScope,
		"StorageEndpoint": h.cfg.Storage.Endpoint, "Bucket": h.cfg.Storage.Bucket, "Prefix": h.cfg.Storage.Prefix,
		"AccessKey": h.cfg.Storage.AccessKey, "SecretKey": h.cfg.Storage.SecretKey, "EncryptionKey": h.cfg.IntegrationEncryptionKey,
	}
	must(h.t, "send private pinned core configuration", encoder.Encode(input))
	var ready struct{ Address string }
	must(h.t, "read published listener address", decoder.Decode(&ready))
	address, err := url.Parse(ready.Address)
	must(h.t, "parse published owned listener", err)
	check(h.t, address.Scheme == "http" && address.Hostname() == "127.0.0.1" && address.Port() != "" &&
		address.Path == "" && address.RawQuery == "" && address.User == nil, "published core listener is not owned loopback")
	h.base, h.client = ready.Address, directClient(h.t, ready.Address)
	control := func(operation string) error {
		if encoder.Encode(object{"operation": operation}) != nil {
			return errors.New("published V10 control write failed")
		}
		var result struct {
			OK              bool
			Queries, Writes int32
		}
		if decoder.Decode(&result) != nil || !result.OK {
			return errors.New("published V10 control outcome failed")
		}
		h.publishedQueries.Store(result.Queries + build.workerQueries)
		h.publishedWrites.Store(result.Writes)
		return nil
	}
	h.imports = func(context.Context) error { return control("imports") }
	var closeOnce sync.Once
	var closeErr error
	h.closeCore = func() error {
		closeOnce.Do(func() {
			closeErr = control("close")
			_ = stdin.Close()
			wait()
			select {
			case err := <-done:
				if closeErr == nil {
					closeErr = err
				}
			case <-time.After(3*time.Second):
				_ = command.Process.Kill()
				closeErr = errors.New("published V10 shutdown exceeded its bound")
			}
			cancel()
		})
		return closeErr
	}
	closePublished := h.closeCore
	h.t.Cleanup(func() { must(h.t, "close actual published V10 core", closePublished()) })
	return &build
}

func (h *harness) publishedTeamsBaselineWorker(build *teamsPublished, n *jiraServer) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, build.Executable)
	command.Env = append(teamsChildEnvironment(), "ASPM_TEAMS_PUBLISHED_WORKER=1")
	command.Stdin = bytes.NewReader(encoded(h.t, object{
		"DatabaseURL": h.cfg.DatabaseURL, "Schema": h.cfg.Schema, "WorkerID": "published-v10-"+nonce(h.t),
		"JiraOrigin": n.server.URL, "SlackOrigin": n.slack.URL,
		"EncryptionKey": h.cfg.IntegrationEncryptionKey, "RootDER": n.rootDER,
	}))
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &h.log
	must(h.t, "run one actual published V10 delivery step", command.Run())
	check(h.t, output.Len() <= 1024, "published worker output exceeded its bound")
	result := decoded[struct{ Queries int32; Steps int }](h.t, output.Bytes())
	check(h.t, result.Queries > 0 && result.Queries <= 400 && result.Steps == 1, "published worker lost its query/step budget")
	build.workerQueries += result.Queries
	h.publishedQueries.Add(result.Queries)
}

func teamsHistoricalMigrations(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"schema.go", "reports_schema.go", "oidc_schema.go", "import_security_schema.go",
		"remediation_schema.go", "source_schema.go", "ai_schema.go", "assessment_schema.go", "work_view_schema.go", "jira_schema.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal", "app", name), nil, 0)
		must(t, "read historical migration literals without pinning producer files", err)
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, identifier := range value.Names {
					version, err := strconv.Atoi(strings.TrimPrefix(identifier.Name, "schemaV"))
					if err != nil || !strings.HasPrefix(identifier.Name, "schemaV") || version < 1 || version > 10 {
						continue
					}
					check(t, i < len(value.Values), "historical migration is no longer an explicit literal")
					literal, ok := value.Values[i].(*ast.BasicLit)
					check(t, ok && literal.Kind == token.STRING, "historical migration is no longer a string literal")
					sql, err := strconv.Unquote(literal.Value)
					must(t, "decode historical migration literal", err)
					_, duplicate := result[identifier.Name]
					check(t, !duplicate, "historical migration identity duplicated")
					result[identifier.Name] = sql
				}
			}
		}
	}
	check(t, len(result) == 10, "historical V1-V10 migration set changed")
	return result
}
