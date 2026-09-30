//go:build integration && saved_work_views_calibration

package saved_work_views

import (
	"net/url"
	"testing"
)

func TestSavedWorkViewsOperatorRevocationCalibration(t *testing.T) {
	h := newHarness(t)
	control := h.operatorControl()
	_, _, removed := h.seed(h.admin, "Operator calibration removed scope", &h.admin.User.ID, "Calibration private finding")
	_, _, retained := h.seed(control.witness, "Operator calibration retained scope", nil, "Calibration retained finding")
	selector := "q=" + url.QueryEscape(removed[0].Title)
	same(t, "ordinary Work did not allow the original membership", ids(h.work(control.target, selector)), ids(removed))
	h.csv(control.target, selector, removed)
	same(t, "ordinary Work did not allow the retained membership", ids(h.work(control.retained, "q=")), ids(retained))
	h.csv(control.retained, "q=", retained)
	session := h.json(control.target, "GET", "/api/v1/session", nil, 200)
	check(t, session.User.ID == control.target.User.ID && len(session.Workspaces) == 2, "calibration did not create two real HTTP memberships")
	tables := []string{"users", "sessions", "workspaces", "assets", "imports", "findings", "observations", "notes"}
	before, writes := h.snapshot(tables), h.writes.Load()
	control.revoke()
	control.sessionsRemainValid()
	for _, path := range []string{"/api/v1/work?" + selector, "/api/v1/work/export?format=csv&" + selector} {
		h.revoked(control.target, "GET", path, nil, removed[0].ID, removed[0].Title, removed[0].AssetName)
	}
	same(t, "retained membership lost actual Work access", ids(h.work(control.retained, "q=")), ids(retained))
	h.csv(control.retained, "q=", retained)
	same(t, "independent session lost Work access", ids(h.work(control.witness, "q=")), ids(retained))
	same(t, "operator deletion damaged another actor in the removed workspace", ids(h.work(h.admin, "q=")), ids(removed))
	same(t, "operator fixture changed other business/session state", h.snapshot(tables), before)
	check(t, h.writes.Load() == writes, "operator revocation or read replay performed S3 I/O")
	t.Log("CALIBRATION ONLY: current ordinary Work/CSV replay returned opaque 403 after actual membership deletion; same session and second membership plus independent session remained valid; no saved-view API exercised")
}
