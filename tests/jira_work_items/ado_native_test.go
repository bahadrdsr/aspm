//go:build integration && ado_collection

package jira_work_items

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type adoScript struct {
	target                          adoTarget
	selection                       adoSelection
	token, mode, holdStage, name     string
	report, repository, build, artifact, archive []byte
	gate                            *adoGate
	step                            atomic.Int32
}

type adoNative struct {
	h                    *adoHarness
	server               *httptest.Server
	client               *http.Client
	calls                atomic.Int32
	mu                   sync.Mutex
	script               *adoScript
	githubToken          string
	githubRepositoryBody []byte
}

func (h *adoHarness) newNative() *adoNative {
	n := &adoNative{h: h}
	n.server = httptest.NewTLSServer(http.HandlerFunc(n.serve))
	u, err := url.Parse(n.server.URL)
	must(h.t, "parse owned native TLS origin", err)
	transport := n.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	check(h.t, !transport.TLSClientConfig.InsecureSkipVerify, "native fixture must verify its actual certificate")
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != u.Host {
			h.t.Error("native request tried an unapproved network authority")
			return nil, errors.New("unapproved native address")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	n.client = &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h.t.Cleanup(func() { n.client.CloseIdleConnections(); n.server.Close() })
	return n
}

func adoSARIF(scanned bool, empty bool) []byte {
	invocation, results := "", `[{"ruleId":"ADO-1","level":"warning","message":{"text":"`+adoReportCanary+` 安全"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/selected.go"},"region":{"startLine":17}}}],"properties":{"owner":"not-an-owner"}}]`
	if scanned {
		invocation = `"invocations":[{"startTime":"2026-09-29T09:00:00Z","executionSuccessful":true}],`
	}
	if empty {
		results = "[]"
	}
	return []byte(" \r\n" + `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Owned ADO report","rules":[{"id":"ADO-1","shortDescription":{"text":"Selected ADO report finding"}}]}},` +
		invocation + `"results":` + results + `}]}` + "\r\n")
}

func (n *adoNative) arm(target adoTarget, selection adoSelection, token string, report []byte, mode, hold string) *adoScript {
	s := &adoScript{target: target, selection: selection, token: token, report: bytes.Clone(report),
		mode: mode, holdStage: hold, name: "selected-native-repository", gate: newADOGate(n.h.t)}
	n.h.remember(token)
	project, repository := target.ProjectID, target.RepositoryID
	if mode == "wrong-repository" {
		repository = "99999999-9999-4999-8999-999999999999"
	}
	s.repository = []byte(fmt.Sprintf(" \t{\"id\":%q,\"name\":%q,\"project\":{\"id\":%q},\"owner\":\"untrusted-owner\",\"criticality\":\"critical\",\"tags\":[\"untrusted\"],\"createdAt\":\"2026-09-20T01:00:00Z\"}\r\n", repository, s.name, project))
	buildProject := project
	if mode == "wrong-build-project" {
		buildProject = "99999999-9999-4999-8999-999999999999"
	}
	s.build = []byte(fmt.Sprintf(`{"id":%s,"status":"completed","result":"succeeded","finishTime":"2026-09-30T18:00:00Z","project":{"id":%q},"repository":{"id":%q}}`, selection.BuildID, buildProject, target.RepositoryID))
	path := "/" + target.Organization + "/" + target.ProjectID + "/_apis/build/builds/" + selection.BuildID + "/artifacts"
	query := url.Values{"artifactName": {selection.ArtifactName}, "api-version": {"7.1"}, "$format": {"zip"}}
	download := n.server.URL + path + "?" + query.Encode()
	switch mode {
	case "wrong-org":
		download = strings.Replace(download, "/"+target.Organization+"/", "/other-org/", 1)
	case "wrong-project":
		download = strings.Replace(download, "/"+target.ProjectID+"/", "/99999999-9999-4999-8999-999999999999/", 1)
	case "wrong-build":
		download = strings.Replace(download, "/builds/"+selection.BuildID+"/", "/builds/999/", 1)
	case "foreign-origin":
		download = strings.Replace(download, n.server.URL, "https://unapproved.invalid", 1)
	case "query-repeat":
		download += "&api-version=7.1"
	case "query-artifact":
		query.Set("artifactName", "other-artifact")
		download = n.server.URL + path + "?" + query.Encode()
	}
	s.artifact = []byte(fmt.Sprintf(`{"id":9,"name":%q,"resource":{"type":"Container","downloadUrl":%q},"createdAt":"2026-09-30T18:01:00Z"}`, selection.ArtifactName, download))
	s.archive = n.zip(selection.ArtifactPath, report, mode)
	n.mu.Lock()
	n.script = s
	n.mu.Unlock()
	return s
}

func (n *adoNative) zip(path string, report []byte, mode string) []byte {
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	add := func(name string, data []byte, symlink bool) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if symlink {
			header.SetMode(os.ModeSymlink | 0777)
		}
		entry, err := writer.CreateHeader(header)
		must(n.h.t, "create in-memory selected ZIP entry", err)
		_, err = entry.Write(data)
		must(n.h.t, "write in-memory selected ZIP bytes", err)
	}
	data, name := report, path
	if mode == "zip-large" {
		data = bytes.Repeat([]byte("x"), (32<<10)+1)
	}
	if mode == "zip-missing" {
		name = "reports/other.sarif"
	}
	add(name, data, mode == "zip-symlink")
	if mode == "zip-traversal" {
		add("../outside.txt", []byte("never extract"), false)
	}
	if mode == "zip-duplicate" {
		add(path, report, false)
	}
	if mode == "zip-entries" {
		for i := 0; i < 4096; i++ {
			add(fmt.Sprintf("bounded/%04d.txt", i), nil, false)
		}
	}
	must(n.h.t, "close bounded in-memory ZIP", writer.Close())
	if mode == "download-large" {
		return bytes.Repeat([]byte("x"), (32<<10)+1)
	}
	return body.Bytes()
}

func (n *adoNative) allowGitHub(token string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.githubToken = token
	n.githubRepositoryBody = []byte(" \r\n{\"id\":420001,\"full_name\":\"owned/legacy\",\"description\":\"exact published GitHub evidence\"}\r\n")
	n.h.remember(token)
}

func (n *adoNative) serve(w http.ResponseWriter, r *http.Request) {
	if n.calls.Add(1) > 64 {
		n.h.t.Error("native fixture request budget exceeded")
		w.WriteHeader(429)
		return
	}
	n.mu.Lock()
	s, githubToken, githubBody := n.script, n.githubToken, n.githubRepositoryBody
	n.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/repos/") && githubToken != "" {
		valid := r.Method == "GET" && r.Header.Get("Authorization") == "Bearer "+githubToken &&
			r.Header.Get("X-GitHub-Api-Version") == "2026-03-10" && r.Header.Get("Accept") == "application/vnd.github+json"
		if !valid {
			n.h.t.Error("historical GitHub native header behavior changed")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owned/legacy" && r.URL.RawQuery == "":
			_, _ = w.Write(githubBody)
		case r.URL.Path == "/repos/owned/legacy/code-scanning/alerts" &&
			reflect.DeepEqual(r.URL.Query(), url.Values{"page": {"1"}, "per_page": {"1"}}):
			_, _ = io.WriteString(w, "[]")
		default:
			n.h.t.Error("historical GitHub source widened its scope")
			w.WriteHeader(400)
		}
		return
	}
	if s == nil {
		n.h.t.Error("native request without an explicitly selected fixture")
		w.WriteHeader(400)
		return
	}
	stage, raw, query := "", []byte(nil), url.Values{"api-version": {"7.1"}}
	base := "/" + s.target.Organization + "/" + s.target.ProjectID + "/_apis"
	switch r.URL.Path {
	case base + "/git/repositories/" + s.target.RepositoryID:
		stage, raw = "repository", s.repository
	case base + "/build/builds/" + s.selection.BuildID:
		stage, raw = "pipeline", s.build
	case base + "/build/builds/" + s.selection.BuildID + "/artifacts":
		query.Set("artifactName", s.selection.ArtifactName)
		stage, raw = "artifact", s.artifact
		if r.URL.Query().Get("$format") == "zip" {
			stage, raw = "report", s.archive
			query.Set("$format", "zip")
		}
	}
	user, pat, ok := r.BasicAuth()
	expectedStep := map[string]int32{"repository": 1, "pipeline": 2, "artifact": 3, "report": 4}[stage]
	if stage == "" || r.Method != "GET" || !ok || user != "" || pat != s.token ||
		r.Header.Get("Accept") != "application/json" || r.Header.Get("Accept-Encoding") != "identity" ||
		r.Header.Get("X-GitHub-Api-Version") != "" ||
		!reflect.DeepEqual(r.URL.Query(), query) || s.step.Add(1) != expectedStep {
		n.h.t.Error("actual ADO request violated selected path/order/query/PAT/header bounds")
		w.WriteHeader(400)
		return
	}
	if stage == s.holdStage && !s.gate.wait(r.Context()) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if (stage == "artifact" && s.mode == "artifact-missing") || (stage == "pipeline" && s.mode == "build-read-denied") {
		status := 404
		if s.mode == "build-read-denied" {
			status = 403
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"message":%q}`, s.token)
		return
	}
	if stage == "repository" && s.mode == "code-read-denied" {
		w.WriteHeader(403)
		fmt.Fprintf(w, `{"message":%q}`, s.token)
		return
	}
	if stage == "report" {
		w.Header().Set("Content-Type", "application/zip")
		if s.mode == "redirect" {
			w.Header().Set("Location", n.server.URL+"/unapproved")
			w.WriteHeader(307)
			return
		}
	}
	_, _ = w.Write(raw)
}
