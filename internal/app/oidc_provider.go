package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Issuer, ClientID, WorkspaceID string
	ClientSecret                  string       `json:"-"`
	Client                        *http.Client `json:"-"`
}

const (
	oidcHTTPTimeout = 5 * time.Second
	oidcRequestTime = 20 * time.Second
	oidcBodyLimit   = 512 << 10
)

var (
	errOIDCUnavailable = &apiError{503, "unavailable", "Federated login is unavailable; local login remains available"}
	errOIDCUpstream    = errors.New("identity-provider request could not be completed")
)

type discoveredOIDC struct {
	endpoint oauth2.Endpoint
	verifier *oidc.IDTokenVerifier
}

type oidcAuth struct {
	config OIDCConfig
	valid  bool
	client *http.Client
	guard  *oidcHTTPGuard

	load       chan struct{}
	mu         sync.RWMutex
	discovered *discoveredOIDC
	retryAfter time.Time
	closeIdle  func()
}

// No discovery is performed here. Optional provider or configuration failures
// cannot make local authentication or application startup depend on an IdP.
func newOIDCAuth(config *OIDCConfig) *oidcAuth {
	if config == nil {
		return nil
	}
	s := &oidcAuth{config: *config, load: make(chan struct{}, 1), closeIdle: func() {}}
	s.config.Client = nil
	s.valid = len(config.Issuer) <= 1024 && validOIDCURL(config.Issuer, false) &&
		validText(config.ClientID, 256) && validText(config.ClientSecret, 4096) && validID(config.WorkspaceID)
	if !s.valid {
		return s
	}
	var base http.RoundTripper = http.DefaultTransport
	timeout := oidcHTTPTimeout
	if config.Client != nil {
		if config.Client.Transport != nil {
			base = config.Client.Transport
		}
		if config.Client.Timeout > 0 {
			timeout = min(timeout, config.Client.Timeout)
		}
	}
	if transport, ok := base.(*http.Transport); ok {
		owned := transport.Clone()
		if owned.MaxResponseHeaderBytes == 0 || owned.MaxResponseHeaderBytes > 64<<10 {
			owned.MaxResponseHeaderBytes = 64 << 10
		}
		if owned.MaxConnsPerHost == 0 || owned.MaxConnsPerHost > 2 {
			owned.MaxConnsPerHost = 2
		}
		if owned.MaxIdleConnsPerHost == 0 || owned.MaxIdleConnsPerHost > 2 {
			owned.MaxIdleConnsPerHost = 2
		}
		if owned.ResponseHeaderTimeout == 0 || owned.ResponseHeaderTimeout > timeout {
			owned.ResponseHeaderTimeout = timeout
		}
		if owned.TLSHandshakeTimeout == 0 || owned.TLSHandshakeTimeout > timeout {
			owned.TLSHandshakeTimeout = timeout
		}
		base, s.closeIdle = owned, owned.CloseIdleConnections
	}
	s.guard = &oidcHTTPGuard{base: base, discovery: strings.TrimSuffix(config.Issuer, "/") + "/.well-known/openid-configuration"}
	s.client = &http.Client{
		Transport: s.guard, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errOIDCUpstream },
	}
	return s
}

func (s *oidcAuth) close() { s.closeIdle() }

func validOIDCURL(value string, queryAllowed bool) bool {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\\\r\n\x00") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		(!queryAllowed && (u.RawQuery != "" || u.ForceQuery)) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if queryAllowed {
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return false
		}
		for _, field := range []string{"client_secret", "client_assertion", "access_token", "id_token"} {
			if values.Has(field) {
				return false
			}
		}
	}
	return true
}

func (s *oidcAuth) discover(ctx context.Context) (*discoveredOIDC, error) {
	if !s.valid {
		return nil, errOIDCUnavailable
	}
	s.mu.RLock()
	cached := s.discovered
	s.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	select {
	case s.load <- struct{}{}:
		defer func() { <-s.load }()
	case <-ctx.Done():
		return nil, errOIDCUnavailable
	}
	s.mu.RLock()
	cached, retryAfter := s.discovered, s.retryAfter
	s.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	if time.Now().Before(retryAfter) {
		return nil, errOIDCUnavailable
	}
	failed := func() (*discoveredOIDC, error) {
		s.mu.Lock()
		s.retryAfter = time.Now().Add(5 * time.Second)
		s.mu.Unlock()
		return nil, errOIDCUnavailable
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, s.client), s.config.Issuer)
	if err != nil {
		return failed()
	}
	var metadata struct {
		Issuer        string   `json:"issuer"`
		KeysURL       string   `json:"jwks_uri"`
		AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
		SigningAlgs   []string `json:"id_token_signing_alg_values_supported"`
		PKCEMethods   []string `json:"code_challenge_methods_supported"`
		ResponseTypes []string `json:"response_types_supported"`
	}
	if provider.Claims(&metadata) != nil || metadata.Issuer != s.config.Issuer {
		return failed()
	}
	endpoint := provider.Endpoint()
	if !validOIDCURL(endpoint.AuthURL, true) || !validOIDCURL(endpoint.TokenURL, true) ||
		!validOIDCURL(metadata.KeysURL, true) || !slices.Contains(metadata.ResponseTypes, "code") ||
		!slices.Contains(metadata.SigningAlgs, oidc.RS256) ||
		(len(metadata.PKCEMethods) != 0 && !slices.Contains(metadata.PKCEMethods, "S256")) {
		return failed()
	}
	// Prefer explicit body authentication and never probe by exchanging a code
	// twice. A provider advertising only Basic is also supported.
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	if len(metadata.AuthMethods) != 0 && !slices.Contains(metadata.AuthMethods, "client_secret_post") {
		if !slices.Contains(metadata.AuthMethods, "client_secret_basic") {
			return failed()
		}
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	s.guard.mu.Lock()
	s.guard.token, s.guard.keys = endpoint.TokenURL, metadata.KeysURL
	s.guard.mu.Unlock()
	cached = &discoveredOIDC{
		endpoint: endpoint,
		// Provider token expiry uses the actual clock, not the application
		// domain clock. No issuer, audience, expiry, or signature check is skipped.
		verifier: provider.Verifier(&oidc.Config{ClientID: s.config.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}),
	}
	s.mu.Lock()
	s.discovered = cached
	s.mu.Unlock()
	return cached, nil
}

type oidcHTTPGuard struct {
	base      http.RoundTripper
	discovery string
	mu        sync.RWMutex
	token     string
	keys      string
}

func (g *oidcHTTPGuard) RoundTrip(request *http.Request) (*http.Response, error) {
	target := request.URL.String()
	g.mu.RLock()
	token, keys := g.token, g.keys
	g.mu.RUnlock()
	credentialRequest := request.Method == http.MethodPost && target == token && token != ""
	metadataRequest := request.Method == http.MethodGet && (target == g.discovery || (keys != "" && target == keys))
	if !validOIDCURL(target, true) || (!credentialRequest && !metadataRequest) ||
		request.Header.Get("Cookie") != "" || (!credentialRequest && request.Header.Get("Authorization") != "") ||
		request.ContentLength > 64<<10 {
		return nil, errOIDCUpstream
	}
	response, err := g.base.RoundTrip(request)
	if err != nil {
		return nil, errOIDCUpstream
	}
	if response == nil || response.Body == nil {
		return nil, errOIDCUpstream
	}
	// Stop redirects before the HTTP client can forward a token POST or its
	// credentials. Bound decoded response bytes before libraries read JSON.
	if (response.StatusCode >= 300 && response.StatusCode < 400) || response.ContentLength > oidcBodyLimit {
		_ = response.Body.Close()
		return nil, errOIDCUpstream
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, oidcBodyLimit+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > oidcBodyLimit {
		return nil, errOIDCUpstream
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}
