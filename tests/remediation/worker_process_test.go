//go:build integration

package remediation

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type privateWorkerInput struct {
	Database      databaseConfig
	EncryptionKey []byte
	WorkerID      string
	LeaseDuration time.Duration
	Endpoint      string
	Certificate   []byte
}

func TestMain(m *testing.M) {
	if os.Getenv("ASPM_REMEDIATION_CHILD") == "1" {
		os.Exit(workerChild())
	}
	os.Exit(m.Run())
}

func workerChild() int {
	var input privateWorkerInput
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || Production.OpenWorker == nil {
		_, _ = io.WriteString(os.Stderr, "worker private input/binding unavailable\n")
		return 2
	}
	database, err := url.Parse(input.Database.URL)
	if err != nil || database.Hostname() != "127.0.0.1" || database.Port() == "" ||
		!strings.HasPrefix(input.Database.Schema, "remediation_") {
		return 2
	}
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() != "127.0.0.1" ||
		endpoint.Port() == "" || endpoint.RawQuery != "" || endpoint.User != nil {
		return 2
	}
	certificate, err := x509.ParseCertificate(input.Certificate)
	if err != nil {
		return 2
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	ambient := http.DefaultTransport.(*http.Transport).Clone()
	ambient.Proxy = nil
	ambient.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("ambient HTTP blocked in database-only delivery process")
	}
	http.DefaultTransport = ambient
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != endpoint.Host {
			return nil, errors.New("unapproved child provider address")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("child redirects denied") }}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	worker, err := Production.OpenWorker(ctx, workerConfig{
		Database: input.Database, EncryptionKey: input.EncryptionKey, WorkerID: input.WorkerID,
		LeaseDuration: input.LeaseDuration, SlackEndpoint: input.Endpoint, Client: client, LogOutput: io.Discard,
	})
	if err != nil || worker == nil {
		_, _ = io.WriteString(os.Stderr, "independent worker construction failed; private details withheld\n")
		return 3
	}
	defer worker.Close()
	_, err = worker.ProcessNext(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		_, _ = io.WriteString(os.Stderr, "independent worker processing failed; private details withheld\n")
		return 4
	}
	return 0
}

type ownedProcess struct {
	command *exec.Cmd
	done    chan error
}

func startWorkerProcess(h *harness, server *slackServer, lease time.Duration) *ownedProcess {
	h.t.Helper()
	config := h.workerConfig(server, "crash-owned", lease)
	input := privateWorkerInput{Database: config.Database, EncryptionKey: config.EncryptionKey,
		WorkerID: config.WorkerID, LeaseDuration: config.LeaseDuration, Endpoint: config.SlackEndpoint,
		Certificate: server.server.Certificate().Raw}
	command := exec.Command(os.Args[0], "-test.run=^$")
	command.Stdin = bytes.NewReader(encode(h.t, input))
	command.Stdout, command.Stderr = &h.log, &h.log
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	command.Env = append(command.Env, "ASPM_REMEDIATION_CHILD=1", "AWS_EC2_METADATA_DISABLED=true")
	must(h.t, "start only the owned independent worker process", command.Start())
	process := &ownedProcess{command: command, done: make(chan error, 1)}
	go func() { process.done <- command.Wait() }()
	h.t.Cleanup(func() {
		_ = command.Process.Kill()
		select {
		case <-process.done:
		case <-time.After(5 * time.Second):
			h.t.Error("owned worker process did not terminate")
		}
	})
	return process
}

func (p *ownedProcess) kill(t *testing.T) {
	t.Helper()
	must(t, "crash the specific owned worker PID", p.command.Process.Kill())
	select {
	case err := <-p.done:
		check(t, err != nil, "crash fixture unexpectedly performed a graceful worker completion")
		p.done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("owned worker crash did not complete")
	}
}
