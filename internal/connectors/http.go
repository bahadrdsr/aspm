package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	numericID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	uuidID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	nameID    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
	codeID    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:#-]{0,127}$`)
)

func connectorError(cause error) *Error {
	code := "unavailable"
	switch {
	case errors.Is(cause, ErrUncertain):
		code = "uncertain"
	case errors.Is(cause, ErrScope):
		code = "scope"
	case errors.Is(cause, ErrAuth):
		code = "auth"
	case errors.Is(cause, ErrRateLimited):
		code = "rate_limited"
	case errors.Is(cause, ErrRequiredFields):
		code = "required_fields"
	case errors.Is(cause, ErrLimit):
		code = "limit"
	case errors.Is(cause, ErrProtocol):
		code = "protocol"
	case errors.Is(cause, ErrUnsupported):
		code = "unsupported"
	case errors.Is(cause, context.Canceled):
		code = "canceled"
	case errors.Is(cause, context.DeadlineExceeded):
		code = "deadline_exceeded"
	}
	return &Error{Code: code, cause: cause}
}

func normalizedLimits(l Limits) (Limits, error) {
	if l.Requests < 0 || l.Pages < 0 || l.PageSize < 0 || l.Bytes < 0 ||
		l.Requests > 128 || l.Pages > 64 || l.PageSize > 100 || l.Bytes > 32<<20 {
		return l, connectorError(ErrLimit)
	}
	if l.Requests == 0 {
		l.Requests = 32
	}
	if l.Pages == 0 {
		l.Pages = 8
	}
	if l.PageSize == 0 {
		l.PageSize = 50
	}
	if l.Bytes == 0 {
		l.Bytes = 8 << 20
	}
	return l, nil
}

func textID(s string, maximum int) bool {
	return s != "" && len(s) <= maximum && strings.TrimSpace(s) == s &&
		!strings.ContainsFunc(s, unicode.IsControl)
}

func safePath(p string) bool {
	if len(p) > 4096 || strings.ContainsAny(p, "\\%?#") || strings.ContainsFunc(p, unicode.IsControl) {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return !strings.Contains(p, "//")
}

func tlsURL(value string, full bool) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || len(value) > 16384 || u == nil || u.Scheme != "https" ||
		u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		strings.ContainsFunc(value, unicode.IsControl) || !safePath(u.Path) ||
		(!full && (u.RawQuery != "" || u.ForceQuery)) {
		return nil, connectorError(ErrScope)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, connectorError(ErrScope)
		}
	}
	// Encoded separators and double-encoding must not acquire new path meaning
	// in a proxy or vendor router.
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return nil, connectorError(ErrScope)
	}
	return u, nil
}

func apiURL(base *url.URL, segments ...string) *url.URL {
	u := *base
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		escaped[i] = url.PathEscape(segment)
	}
	u.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/" + strings.Join(escaped, "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery, u.Fragment, u.ForceQuery = "", "", false
	return &u
}

func withQuery(u *url.URL, q url.Values) *url.URL {
	v := *u
	v.RawQuery = q.Encode()
	return &v
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() == "" {
			return "443"
		}
		return u.Port()
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func scopedLink(current *url.URL, value string) (*url.URL, error) {
	ref, err := url.Parse(value)
	if err != nil || ref.User != nil || ref.Fragment != "" || len(value) > 16384 {
		return nil, connectorError(ErrScope)
	}
	next, err := tlsURL(current.ResolveReference(ref).String(), true)
	if err != nil || !sameOrigin(current, next) || current.EscapedPath() != next.EscapedPath() {
		return nil, connectorError(ErrScope)
	}
	return next, nil
}

func exactQuery(u *url.URL, expected url.Values) bool {
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != len(expected) {
		return false
	}
	for key, want := range expected {
		got := q[key]
		if len(want) != 1 || len(got) != 1 || got[0] != want[0] {
			return false
		}
	}
	return true
}

func artifactPath(value string) bool {
	return textID(value, 2048) && safePath(value) && !strings.HasPrefix(value, "/") &&
		!strings.Contains(value, ":") && path.Clean(value) == value &&
		len(strings.Split(value, "/")) <= 32 && !strings.HasSuffix(value, "/")
}

type boundedHTTP struct {
	client *http.Client
	limits Limits
}

func newHTTP(client *http.Client, limits Limits) (*boundedHTTP, error) {
	if client == nil {
		return nil, connectorError(ErrScope)
	}
	normalized, err := normalizedLimits(limits)
	if err != nil {
		return nil, err
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	copyClient.Jar = nil
	if copyClient.Timeout == 0 {
		copyClient.Timeout = 30 * time.Second
	}
	return &boundedHTTP{client: &copyClient, limits: normalized}, nil
}

type httpBudget struct {
	http     *boundedHTTP
	requests int
}

type nativeResponse struct {
	Status    int
	Header    http.Header
	Body      []byte
	Attempted bool
}

func (b *httpBudget) do(ctx context.Context, method string, target *url.URL, headers http.Header, body []byte, sign func(*http.Request) error) (nativeResponse, error) {
	var result nativeResponse
	if err := ctx.Err(); err != nil {
		return result, connectorError(err)
	}
	if b.requests >= b.http.limits.Requests || int64(len(body)) > b.http.limits.Bytes {
		return result, connectorError(ErrLimit)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return result, connectorError(ErrScope)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Accept-Encoding", "identity")
	// A write cannot be replayed by net/http after an ambiguous connection
	// failure. In particular, do not attach an Idempotency-Key or GetBody.
	request.GetBody = nil
	if sign != nil {
		if err := sign(request); err != nil {
			return result, err
		}
	}
	b.requests++
	result.Attempted = true
	response, err := b.http.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return result, connectorError(ctx.Err())
		}
		if errors.Is(err, ErrScope) {
			return result, connectorError(ErrScope)
		}
		return result, connectorError(ErrUnavailable)
	}
	if response == nil || response.Body == nil {
		return result, connectorError(ErrProtocol)
	}
	defer response.Body.Close()
	result.Status, result.Header = response.StatusCode, response.Header.Clone()
	if result.Status >= 300 && result.Status < 400 {
		return result, responseError(result, ErrScope)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, b.http.limits.Bytes+1))
	if int64(len(data)) > b.http.limits.Bytes {
		cause := ErrLimit
		if result.Status >= 400 {
			cause = errors.Join(statusCause(result.Status, ""), ErrLimit)
		}
		return result, responseError(result, cause)
	}
	result.Body = data
	if result.Status < 200 || result.Status >= 300 {
		return result, responseError(result, statusCause(result.Status, nativeErrorCode(data)))
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return result, responseError(result, ctx.Err())
		}
		return result, responseError(result, ErrUnavailable)
	}
	return result, nil
}

func responseError(r nativeResponse, cause error) *Error {
	e := connectorError(cause)
	e.HTTPStatus = r.Status
	e.RetryAfter = retryAfter(r.Header.Get("Retry-After"), time.Now())
	e.NativeCode = nativeErrorCode(r.Body)
	return e
}

func statusCause(status int, native string) error {
	if _, suffix, found := strings.Cut(native, "#"); found {
		native = suffix
	}
	switch strings.ToLower(native) {
	case "throttling", "throttlingexception", "toomanyrequestsexception", "requestlimitexceeded", "limitexceededexception", "rate_limited", "ratelimited":
		return ErrRateLimited
	case "accessdenied", "accessdeniedexception", "unauthorizedoperation", "authfailure", "invalidclienttokenid", "expiredtoken", "expiredtokenexception", "unrecognizedclientexception", "requestexpired":
		return ErrAuth
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		if status >= 500 {
			return ErrUnavailable
		}
		return ErrProtocol
	}
}

func nativeErrorCode(data []byte) string {
	var value struct {
		Code  string          `json:"code"`
		Type  string          `json:"__type"`
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(data, &value)
	code := value.Code
	if code == "" {
		code = value.Type
	}
	if code == "" && len(value.Error) > 0 {
		if json.Unmarshal(value.Error, &code) != nil {
			var nested struct{ Code string }
			if json.Unmarshal(value.Error, &nested) == nil {
				code = nested.Code
			}
		}
	}
	if code == "" && len(data) > 0 && data[0] == '<' {
		var native struct {
			Code   string `xml:"Code"`
			Nested string `xml:"Errors>Error>Code"`
		}
		if xml.Unmarshal(data, &native) == nil {
			code = native.Code
			if code == "" {
				code = native.Nested
			}
		}
	}
	if codeID.MatchString(code) {
		return code
	}
	return ""
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		const maxSeconds = int64((1<<63 - 1) / time.Second)
		if seconds > maxSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func jsonHeaders(token string) http.Header {
	return http.Header{
		"Accept":        {"application/json"},
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer " + token},
	}
}
