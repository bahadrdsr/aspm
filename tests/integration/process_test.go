//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

type childRequest struct {
	Mode        string
	Schema      string
	Prefix      string
	WorkspaceID string
	WorkerID    string
	LeaseFor    time.Duration
	Reference   EvidenceRef
}

type childReport struct {
	Lease  *Lease
	Result *Result
}

type childProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	waited bool
}

func TestMain(m *testing.M) {
	if marker := os.Getenv("ASPM_M02_TEST_CHILD"); marker != "" {
		if marker != "1" {
			fmt.Fprintln(os.Stderr, "invalid internal M02 child marker")
			os.Exit(2)
		}
		if err := runChild(); err != nil {
			fmt.Fprintf(os.Stderr, "controlled M02 child failed (%T; sensitive details withheld)\n", err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sendChildReport(report childReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stdout, "M02_CHILD_RESULT %s\n", data)
	return err
}

func runChild() (resultErr error) {
	var request childRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16<<10)).Decode(&request); err != nil {
		return err
	}
	if !regexp.MustCompile(`^m02_it_[a-f0-9]{24}$`).MatchString(request.Schema) ||
		request.Prefix != "m02-it/"+strings.TrimPrefix(request.Schema, "m02_it_")+"/" {
		return errors.New("child namespace is not an owned M02 test scope")
	}
	jobsConfig, evidenceConfig, err := environment()
	if err != nil {
		return err
	}
	jobsConfig.Schema, jobsConfig.ApplicationName = request.Schema, request.Schema+"-child"
	evidenceConfig.Prefix = request.Prefix
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch request.Mode {
	case "hold-lease":
		if Production.OpenJobs == nil {
			return errors.New("missing real jobs binding in child")
		}
		jobs, err := Production.OpenJobs(ctx, jobsConfig)
		if err != nil {
			return err
		}
		if jobs == nil {
			return errors.New("nil production jobs client in child")
		}
		defer func() { resultErr = errors.Join(resultErr, jobs.Close()) }()
		lease, err := jobs.Claim(ctx, Claim{WorkspaceID: request.WorkspaceID, WorkerID: request.WorkerID, LeaseFor: request.LeaseFor})
		if err != nil {
			return err
		}
		if err := sendChildReport(childReport{Lease: &lease}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	case "read-evidence":
		if Production.OpenEvidence == nil {
			return errors.New("missing real evidence binding in child")
		}
		store, err := Production.OpenEvidence(ctx, evidenceConfig)
		if err != nil {
			return err
		}
		if store == nil {
			return errors.New("nil production evidence client in child")
		}
		defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
		data, err := readEvidence(ctx, store, request.WorkspaceID, request.Reference)
		if err != nil {
			return err
		}
		return sendChildReport(childReport{Result: &Result{SHA256: digest(data), SizeBytes: int64(len(data))}})
	default:
		return errors.New("unsupported controlled child mode")
	}
}

func startChild(t *testing.T, f *fixture, request childRequest) (*childProcess, childReport) {
	t.Helper()
	request.Schema, request.Prefix = f.jobs.Schema, f.evidence.Prefix
	input, err := json.Marshal(request)
	requireOK(t, "encode credential-free child control message", err)
	executable, err := os.Executable()
	requireOK(t, "locate the current Go test executable", err)
	ctx, cancel := context.WithTimeout(f.ctx, 12*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^$")
	cmd.Env = append(os.Environ(), "ASPM_M02_TEST_CHILD=1")
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.StdoutPipe()
	requireOK(t, "open owned child result pipe", err)
	process := &childProcess{cmd: cmd, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		if !process.waited && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			process.waited = true
		}
	})
	requireOK(t, "start only the fixed Go test worker executable", cmd.Start())
	scanner := bufio.NewScanner(io.LimitReader(output, 1<<20))
	scanner.Buffer(make([]byte, 1024), 16<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "M02_CHILD_RESULT ") {
			continue
		}
		var report childReport
		err := json.Unmarshal([]byte(strings.TrimPrefix(line, "M02_CHILD_RESULT ")), &report)
		requireOK(t, "decode the bounded credential-free child result", err)
		return process, report
	}
	t.Fatalf("owned worker did not acknowledge its controlled operation (%T; output withheld)", scanner.Err())
	return nil, childReport{}
}

func (p *childProcess) kill(t *testing.T) {
	t.Helper()
	requireOK(t, "terminate only the worker process created by this test", p.cmd.Process.Kill())
	err := p.cmd.Wait()
	p.waited = true
	p.cancel()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal("controlled worker termination did not produce the expected process exit")
	}
}

func (p *childProcess) finish(t *testing.T) {
	t.Helper()
	err := p.cmd.Wait()
	p.waited = true
	p.cancel()
	requireOK(t, "finish the independently reopened evidence reader", err)
}
