package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.message }

var (
	errUnauthorized = &apiError{401, "unauthorized", "Authentication is required"}
	errForbidden    = &apiError{403, "forbidden", "This operation is not permitted"}
	errNotFound     = &apiError{404, "not-found", "The requested resource was not found"}
	errConflict     = &apiError{409, "conflict", "The request conflicts with existing state"}
	errInvalid      = &apiError{400, "invalid-input", "The request is invalid"}
	errTooLarge     = &apiError{413, "too-large", "The request exceeds the configured size limit"}
	errUnsupported  = &apiError{415, "unsupported-format", "The content or report format is not supported"}
	errMethod       = &apiError{405, "method-not-allowed", "The method is not allowed for this resource"}
	errUnavailable  = &apiError{503, "unavailable", "The operation could not be completed"}
)

func (a *Application) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Request-ID", newID())
	err := a.route(w, r)
	if err == nil {
		return
	}
	var problem *apiError
	var pgerr *pgconn.PgError
	switch {
	case errors.As(err, &problem):
	case errors.Is(err, pgx.ErrNoRows):
		problem = errNotFound
	case errors.As(err, &pgerr) && pgerr.Code == "23505":
		problem = errConflict
	case errors.As(err, &pgerr) && (pgerr.Code == "23503" || pgerr.Code == "23514"):
		problem = errInvalid
	default:
		problem = errUnavailable
		// Database, report, URL, and credential-bearing errors are never logged.
		a.log.Print("request failed requestId=" + w.Header().Get("X-Request-ID"))
	}
	writeJSON(w, problem.status, map[string]any{"error": Failure{
		Code: problem.code, Message: problem.message, RequestID: w.Header().Get("X-Request-ID"), Retryable: false,
	}})
}

func (a *Application) route(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	if path == "/api/v1/auth/oidc/start" || path == oidcCallbackPath {
		if err := requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		if path == oidcCallbackPath {
			return a.oidcCallback(w, r)
		}
		return a.startOIDC(w, r)
	}
	if path == "/api/v1/bootstrap" || path == "/api/v1/login" {
		if err := requireMethod(w, r, http.MethodPost); err != nil {
			return err
		}
		if !a.validOrigin(r) {
			return errForbidden
		}
		if path == "/api/v1/bootstrap" {
			return a.bootstrap(w, r)
		}
		return a.login(w, r)
	}
	if !strings.HasPrefix(path, "/api/v1/") {
		return errNotFound
	}
	session, err := a.authenticate(r)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.validOrigin(r) {
		return errForbidden
	}
	if path == "/api/v1/session" {
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.sessionResponse(w, r, session)
	}
	if path == "/api/v1/logout" {
		if err = requireMethod(w, r, http.MethodPost); err != nil {
			return err
		}
		return a.logout(w, r, session)
	}
	membership, err := a.selectWorkspace(r, session.User.ID)
	if err != nil {
		return err
	}
	switch path {
	case "/api/v1/workspaces":
		if err = requireMethod(w, r, http.MethodPost); err != nil {
			return err
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.createWorkspace(w, r, session.User)
	case "/api/v1/users":
		if err = requireMethod(w, r, http.MethodPost); err != nil {
			return err
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.createUser(w, r, membership)
	case "/api/v1/assets":
		if err = requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.listAssets(w, r, membership.ID)
		}
		if !canWrite(membership) {
			return errForbidden
		}
		return a.createAsset(w, r, membership.ID)
	case "/api/v1/imports":
		if err = requireMethod(w, r, http.MethodPost); err != nil {
			return err
		}
		if !canWrite(membership) {
			return errForbidden
		}
		return a.upload(w, r, membership.ID, session)
	case "/api/v1/work":
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.listWork(w, r, membership.ID)
	case "/api/v1/work/export":
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.exportWork(w, r, membership.ID)
	case "/api/v1/integrations/catalog":
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.catalog(w)
	case "/api/v1/integrations/connections":
		if err = requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.listIntegrationConnections(w, r, membership.ID)
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.createIntegrationConnection(w, r, membership.ID, session)
	case "/api/v1/reports/overview":
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.reportOverview(w, r, membership.ID)
	case "/api/v1/reports/snapshots":
		if err = requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.listReportSnapshots(w, r, membership.ID)
		}
		if !canWrite(membership) {
			return errForbidden
		}
		return a.createReportSnapshot(w, r, membership.ID, session.User.ID)
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 3 && parts[0] == "integrations" && validID(parts[2]) {
		if parts[1] == "connections" {
			if err = requireMethod(w, r, http.MethodGet, http.MethodPatch); err != nil {
				return err
			}
			if r.Method == http.MethodGet {
				return a.getIntegrationConnection(w, r, membership.ID, parts[2])
			}
			if membership.Role != "admin" {
				return errForbidden
			}
			return a.updateIntegrationConnection(w, r, membership.ID, session, parts[2])
		}
		if parts[1] == "deliveries" {
			if err = requireMethod(w, r, http.MethodGet); err != nil {
				return err
			}
			return a.getFindingDelivery(w, r, membership.ID, parts[2])
		}
	}
	if len(parts) == 3 && parts[0] == "reports" && parts[1] == "snapshots" && validID(parts[2]) {
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.getReportSnapshot(w, r, membership.ID, parts[2])
	}
	if len(parts) < 2 || !validID(parts[1]) {
		return errNotFound
	}
	switch parts[0] {
	case "users":
		if len(parts) != 2 {
			return errNotFound
		}
		if err = requireMethod(w, r, http.MethodPatch); err != nil {
			return err
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.changeUserRole(w, r, membership.ID, session, parts[1])
	case "assets":
		if len(parts) != 2 {
			return errNotFound
		}
		if err = requireMethod(w, r, http.MethodGet, http.MethodPatch, http.MethodDelete); err != nil {
			return err
		}
		if r.Method != http.MethodGet && !canWrite(membership) {
			return errForbidden
		}
		return a.assetResource(w, r, membership.ID, parts[1])
	case "imports":
		if len(parts) != 2 && !(len(parts) == 3 && parts[2] == "evidence") {
			return errNotFound
		}
		if err = requireMethod(w, r, http.MethodGet); err != nil {
			return err
		}
		return a.importResource(w, r, membership.ID, parts[1], len(parts) == 3)
	case "findings":
		if len(parts) == 3 && parts[2] == "deliveries" {
			if err = requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
				return err
			}
			if r.Method == http.MethodGet {
				return a.listFindingDeliveries(w, r, membership.ID, parts[1])
			}
			if !canWrite(membership) {
				return errForbidden
			}
			return a.enqueueFindingDelivery(w, r, membership.ID, session, parts[1])
		}
		if len(parts) == 3 && parts[2] == "notes" {
			if err = requireMethod(w, r, http.MethodPost); err != nil {
				return err
			}
			if !canWrite(membership) {
				return errForbidden
			}
			return a.addNote(w, r, membership.ID, session.User.ID, parts[1])
		}
		if len(parts) != 2 {
			return errNotFound
		}
		if err = requireMethod(w, r, http.MethodGet, http.MethodPatch); err != nil {
			return err
		}
		if r.Method == http.MethodPatch {
			if !canWrite(membership) {
				return errForbidden
			}
			return a.patchFinding(w, r, membership.ID, parts[1])
		}
		return a.findingResponse(w, r, membership.ID, parts[1])
	}
	return errNotFound
}

func requireMethod(w http.ResponseWriter, r *http.Request, methods ...string) error {
	for _, method := range methods {
		if r.Method == method {
			return nil
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	return errMethod
}

func canWrite(w Workspace) bool { return w.Role == "admin" || w.Role == "analyst" }

func (a *Application) validOrigin(r *http.Request) bool {
	if len(r.Header.Values("Origin")) != 1 {
		return false
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" {
		return false
	}
	expected := a.config.PublicOrigin
	if expected == "" {
		if r.TLS == nil {
			return false
		}
		expected = "https://" + r.Host
	}
	target, err := url.Parse(expected)
	return err == nil && strings.EqualFold(origin.Hostname(), target.Hostname()) &&
		firstPort(origin) == firstPort(target)
}

func firstPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "443"
}

func (a *Application) decode(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errUnsupported
	}
	if charset := params["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") {
		return errUnsupported
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errUnsupported
	}
	if r.ContentLength > limit {
		return errTooLarge
	}
	reader := http.MaxBytesReader(w, r.Body, limit)
	data, err := io.ReadAll(reader)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return errTooLarge
		}
		return errInvalid
	}
	if !utf8.Valid(data) || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return errInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return errInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errInvalid
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	payload["apiVersion"] = APIVersion
	data, err := json.Marshal(payload)
	if err != nil {
		status = http.StatusServiceUnavailable
		data, _ = json.Marshal(map[string]any{"apiVersion": APIVersion, "error": Failure{
			Code: "unavailable", Message: "The response could not be encoded", RequestID: w.Header().Get("X-Request-ID"),
		}})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func pageParameters(r *http.Request) (int, string, error) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			return 0, "", errInvalid
		}
		limit = parsed
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !validID(cursor) {
		return 0, "", errInvalid
	}
	return limit, cursor, nil
}
