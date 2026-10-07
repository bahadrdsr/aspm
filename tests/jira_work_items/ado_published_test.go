//go:build integration && ado_collection

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

	"github.com/bahadrdsr/aspm/internal/app"
)

const adoPublishedCommit = "69f3ef9c2fc154742a168df9329d89cfd4b8a020"
const adoPublishedClosure = "16cc2019f023a42a9efde1bfada1b53b5a64e96625a93d5db3affe36b13f72f0"
const adoPublishedHelper = "6716003a1377841ae0ab5bed0f82d076d402d0d1a40db9b02965c7827cce6272"

type adoPublished struct {
	publishedReceipt
	ClosureSHA256 string
	control       func(string) error
}

func adoChildEnvironment() []string {
	result := []string{}
	for _, entry := range safeChildEnvironment() {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.ToUpper(name)
		if strings.HasPrefix(name, "ADO_") || strings.HasPrefix(name, "AZDO_") ||
			strings.HasPrefix(name, "VSS_") || strings.HasPrefix(name, "TEAMS_") ||
			strings.HasPrefix(name, "ASMP_") || name == "SYSTEM_ACCESSTOKEN" {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func adoStorageInput(config app.StorageConfig) object {
	return object{"Endpoint": config.Endpoint, "Bucket": config.Bucket, "Prefix": config.Prefix, "Region": config.Region,
		"AccessKey": config.AccessKey, "SecretKey": config.SecretKey}
}

func (h *adoHarness) adoOpenPublished() *adoPublished {
	h.t.Helper()
	path := os.Getenv("ASPM_ADO_V11_RECEIPT")
	check(h.t, path != "", "BLOCKED: Sol-produced exact published V11 build receipt required")
	data, err := os.ReadFile(path)
	must(h.t, "read bounded private V11 receipt", err)
	check(h.t, len(data) <= 128<<10, "published build receipt exceeded its bound")
	build := decoded[adoPublished](h.t, data)
	check(h.t, build.Origin == adoPublishedCommit && build.ClosureSHA256 == adoPublishedClosure &&
		build.HelperSHA256 == adoPublishedHelper && len(build.Files) == 89 && build.ExitCode == 0 &&
		!build.BusinessSQLSeeds && !build.ProviderCalls && !build.DataExecution,
		"receipt is not the exact bounded nonexecuted published V11 production build")
	root, err := filepath.Abs(filepath.Join("..", "..", ".cache", "ado-collection", "published-v11"))
	must(h.t, "resolve owned pinned build cache", err)
	relative, err := filepath.Rel(root, build.Executable)
	must(h.t, "scope published executable", err)
	check(h.t, filepath.IsAbs(build.Executable) && relative != "." && !strings.HasPrefix(relative, "..") &&
		filepath.Base(build.Executable) == "owned-v11-core.exe", "published binary is outside the approved owned cache")
	executable, err := os.ReadFile(build.Executable)
	must(h.t, "read exact published executable", err)
	check(h.t, digest(executable) == build.ExecutableSHA256, "published executable drifted")
	var manifest strings.Builder
	last := ""
	for _, entry := range build.Files {
		check(h.t, !filepath.IsAbs(entry.Path) && !strings.Contains(entry.Path, "..") &&
			filepath.Clean(entry.Path) == entry.Path && last < entry.Path, "published source path/order drifted")
		body, err := os.ReadFile(filepath.Join(filepath.Dir(build.Executable), entry.Path))
		must(h.t, "read exact pinned production member", err)
		blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(body))), body...))
		check(h.t, digest(body) == entry.SHA256 && hex.EncodeToString(blob[:]) == entry.GitBlob,
			"published source bytes differ from their pinned Git blob")
		manifest.WriteString(entry.Path + "\x00" + entry.GitBlob + "\n")
		last = entry.Path
	}
	check(h.t, digest([]byte(manifest.String())) == adoPublishedClosure, "published production closure identity changed")
	helper, err := os.ReadFile(filepath.Join("ado", "published-core.main.go.txt"))
	must(h.t, "read authored forwarding-only helper", err)
	cached, err := os.ReadFile(filepath.Join(filepath.Dir(build.Executable), "cmd", "owned-v11-core", "main.go"))
	must(h.t, "read compiled forwarding-only helper input", err)
	check(h.t, digest(helper) == adoPublishedHelper && bytes.Equal(helper, cached), "published helper changed or was substituted")

	ctx, cancel := context.WithTimeout(h.ctx, 70*time.Second)
	command := exec.CommandContext(ctx, build.Executable)
	command.Env, command.Stderr = adoChildEnvironment(), &h.log
	stdin, err := command.StdinPipe()
	must(h.t, "open private published input pipe", err)
	stdout, err := command.StdoutPipe()
	must(h.t, "open bounded published output pipe", err)
	must(h.t, "start verified actual published V11 core", command.Start())
	h.publishedStarted.Store(true)
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	h.t.Cleanup(func() { _ = command.Process.Kill(); cancel() })
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(io.LimitReader(stdout, 16<<10))
	readRaw := h.cfg.Storage
	readRaw.AccessKey, readRaw.SecretKey = h.capabilities.RawReader.AccessKey, h.capabilities.RawReader.SecretKey
	publisher := h.adoWorkerConfig().Storage
	in := object{
		"DatabaseURL": h.cfg.DatabaseURL, "Schema": h.cfg.Schema, "Bootstrap": h.cfg.BootstrapToken,
		"AssessmentScope": h.cfg.AssessmentScope, "EncryptionKey": h.cfg.IntegrationEncryptionKey,
		"Raw": adoStorageInput(h.cfg.Storage), "RawReader": adoStorageInput(readRaw),
		"CollectionReader": adoStorageInput(*h.cfg.CollectionStorage), "CollectionPublisher": adoStorageInput(publisher),
		"NativeOrigin": h.native.server.URL, "RootDER": h.native.server.Certificate().Raw,
	}
	must(h.t, "send explicit private capabilities to published app", encoder.Encode(in))
	var ready struct{ Address string }
	must(h.t, "receive actual published HTTP readiness", decoder.Decode(&ready))
	address, err := url.Parse(ready.Address)
	must(h.t, "parse published listener authority", err)
	check(h.t, address.Scheme == "http" && address.Hostname() == "127.0.0.1" && address.Port() != "" &&
		address.Path == "" && address.RawQuery == "" && address.User == nil, "published HTTP listener is not owned")
	h.base, h.client = ready.Address, directClient(h.t, ready.Address)
	build.control = func(operation string) error {
		if encoder.Encode(object{"Operation": operation}) != nil {
			return errors.New("published control input failed")
		}
		var outcome struct {
			OK bool
			Queries int32
			Steps int
			S3ObservedByParent bool
		}
		if decoder.Decode(&outcome) != nil || !outcome.OK || !outcome.S3ObservedByParent ||
			outcome.Queries < h.publishedQueries.Load() || outcome.Queries > 700 {
			return errors.New("published real query/forwarded-storage accounting failed")
		}
		if operation == "collect" && outcome.Steps != 1 {
			return errors.New("published collection did not process exactly one intent")
		}
		h.publishedQueries.Store(outcome.Queries)
		if operation == "close" {
			h.publishedClosed.Store(true)
		}
		return nil
	}
	h.imports = func(context.Context) error { return build.control("imports") }
	var once sync.Once
	var closeErr error
	h.closeCore = func() error {
		once.Do(func() {
			closeErr = build.control("close")
			_ = stdin.Close()
			select {
			case err := <-waited:
				if closeErr == nil {
					closeErr = err
				}
			case <-time.After(3 * time.Second):
				_ = command.Process.Kill()
				closeErr = errors.New("published shutdown bound exceeded")
			}
			cancel()
		})
		return closeErr
	}
	closePublished := h.closeCore
	h.t.Cleanup(func() { must(h.t, "close actual published V11 core", closePublished()) })
	return &build
}

func adoHistoricalMigrations(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"schema.go", "reports_schema.go", "oidc_schema.go", "import_security_schema.go",
		"remediation_schema.go", "source_schema.go", "ai_schema.go", "assessment_schema.go", "work_view_schema.go",
		"jira_schema.go", "teams_schema.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "internal", "app", name), nil, 0)
		must(t, "inspect historical literals without freezing mutable producer files", err)
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
					if err != nil || !strings.HasPrefix(identifier.Name, "schemaV") || version < 1 || version > 11 {
						continue
					}
					check(t, i < len(value.Values), "historical migration is no longer an explicit literal")
					literal, ok := value.Values[i].(*ast.BasicLit)
					check(t, ok && literal.Kind == token.STRING, "historical migration changed representation")
					sql, err := strconv.Unquote(literal.Value)
					must(t, "decode historical SQL string literal", err)
					_, duplicate := result[identifier.Name]
					check(t, !duplicate, "historical migration identity duplicated")
					result[identifier.Name] = sql
				}
			}
		}
	}
	check(t, len(result) == 11, "published V1-V11 migration set changed")
	return result
}
