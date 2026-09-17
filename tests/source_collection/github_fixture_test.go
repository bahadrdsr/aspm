//go:build integration

package source_collection

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type githubScript struct {
	mode, repository, repositoryID string
	rawRepository, first, second   []byte
	holdStage                      string
	arrived, release, cancelled    chan struct{}
	once                           sync.Once
}

func (s *githubScript) allow() { s.once.Do(func() { close(s.release) }) }

type githubCall struct {
	Method, Path, Query string
}
type githubServer struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	token  string
	mu     sync.Mutex
	script *githubScript
	calls  []githubCall
}

func newGitHub(t *testing.T, token string) *githubServer {
	t.Helper()
	g := &githubServer{t: t, token: token}
	g.server = httptest.NewTLSServer(http.HandlerFunc(g.serve))
	target, err := url.Parse(g.server.URL)
	must(t, "parse owned source TLS endpoint", err)
	transport := g.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	require(t, !transport.TLSClientConfig.InsecureSkipVerify, "source fixture must use real TLS certificate verification")
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != target.Host {
			t.Error("source client attempted another network authority")
			return nil, errors.New("unapproved source dial")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	g.client = &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("source redirects denied") }}
	t.Cleanup(func() {
		g.mu.Lock()
		if g.script != nil {
			g.script.allow()
		}
		g.mu.Unlock()
		transport.CloseIdleConnections()
		g.server.Close()
	})
	return g
}

func (g *githubServer) install(mode, repository, repositoryID, hold string) *githubScript {
	parts := strings.Split(repository, "/")
	require(g.t, len(parts) == 2, "fixture requires one selected owner/repository")
	s := &githubScript{mode: mode, repository: repository, repositoryID: repositoryID, holdStage: hold,
		arrived: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	s.rawRepository = []byte(fmt.Sprintf(" \t{ \"id\" : %s, \"full_name\" : %q, \"private\":true, \"description\":\"RAW_REPO_ONLY_%s\", \"default_branch\":\"main\" }\r\n", repositoryID, repository, repositoryID))
	s.first = []byte(`{ "number" : 7, "state":"open", "updated_at":"2026-09-17T10:00:00Z", "rule":{"security_severity_level":"high"}, "most_recent_instance":{"location":{"path":"src/owned.go","start_line":17},"analysis_key":"RAW_ALERT_A_ONLY"}, "ref":"refs/heads/main", "tool":{"name":"Synthetic owned scanner"} }`)
	s.second = []byte(`{"number":8, "state" : "dismissed", "updated_at":"2026-09-17T11:00:00Z", "rule":{"security_severity_level":"low"}, "most_recent_instance":{"location":{"path":"src/other.go","start_line":23},"analysis_key":"RAW_ALERT_B_ONLY"}, "ref":"refs/heads/main"}`)
	if hold == "" {
		s.allow()
	}
	g.mu.Lock()
	g.script = s
	g.mu.Unlock()
	g.t.Cleanup(s.allow)
	return s
}

func (g *githubServer) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

func (g *githubServer) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	s := g.script
	g.calls = append(g.calls, githubCall{r.Method, r.URL.Path, r.URL.RawQuery})
	count := len(g.calls)
	g.mu.Unlock()
	if s == nil || count > 40 {
		g.t.Error("undeclared or excessive native source I/O")
		http.Error(w, "unapproved source operation", 400)
		return
	}
	base := "/repos/" + s.repository
	stage := ""
	if r.URL.Path == base && r.URL.RawQuery == "" {
		stage = "repository"
	} else if r.URL.Path == base+"/code-scanning/alerts" &&
		r.URL.Query().Get("per_page") == "1" && len(r.URL.Query()) == 2 {
		switch r.URL.Query().Get("page") {
		case "1":
			stage = "alerts-1"
		case "2":
			stage = "alerts-2"
		}
	}
	if stage == "" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+g.token ||
		r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
		g.t.Error("actual source request widened selection, changed protocol or omitted the selected credential; values withheld")
		http.Error(w, "invalid owned source request", 400)
		return
	}
	if stage == s.holdStage {
		close(s.arrived)
		select {
		case <-r.Context().Done():
			close(s.cancelled)
			return
		case <-s.release:
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if stage == "repository" {
		switch s.mode {
		case "auth":
			w.WriteHeader(403)
			fmt.Fprintf(w, `{"message":%q}`, g.token)
			return
		case "quota", "429":
			w.Header().Set("Retry-After", "7")
			if s.mode == "quota" {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(12*time.Second).Unix(), 10))
				w.WriteHeader(403)
			} else {
				w.WriteHeader(429)
			}
			fmt.Fprintf(w, `{"message":"secondary rate limit","private":%q}`, g.token)
			return
		case "scope":
			io.WriteString(w, `{"id":420001,"full_name":"other-owner/not-selected"}`)
			return
		case "oversize":
			io.WriteString(w, strings.Repeat("x", 32769))
			return
		case "redirect":
			w.Header().Set("Location", g.server.URL+"/not-approved")
			w.WriteHeader(307)
			return
		case "protocol":
			io.WriteString(w, `{"id":`)
			return
		}
		w.Write(s.rawRepository)
		return
	}
	if stage == "alerts-1" {
		w.Header().Set("Link", `<`+g.server.URL+base+`/code-scanning/alerts?per_page=1&page=2>; rel="next"`)
		w.Write(append(append([]byte("[\n"), s.first...), []byte("\n]")...))
		return
	}
	if s.mode == "partial" {
		w.WriteHeader(403)
		fmt.Fprintf(w, `{"message":%q}`, g.token)
		return
	}
	w.Write(append(append([]byte("[ "), s.second...), []byte(" ]")...))
}
