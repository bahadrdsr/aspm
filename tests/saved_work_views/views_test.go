//go:build integration

package saved_work_views

import (
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSavedWorkViewsOwnerWorkspaceAndStrictCRUD(t *testing.T) {
	h := newHarness(t)
	h.denied(actor{}, "GET", viewsPath, nil, 401, "unauthorized")
	empty := h.json(h.admin, "GET", viewsPath, nil, 200)
	check(t, empty.Items != nil && len(empty.Items) == 0 && empty.Total == 0 && empty.NextCursor == nil,
		"new personal list must be a genuine empty native page")
	viewer, analyst, other := h.user(h.admin, "viewer"), h.user(h.admin, "analyst"), h.workspace()
	foreignAdmin := h.user(other, "admin")
	tables := []string{"assets", "findings", "observations", "notes", "imports", "source_collections", "assessment_previews", "assessment_jobs", "finding_deliveries"}
	before := h.snapshot(tables)
	v := h.create(viewer, " \t My work \n", " \t Needle \n", "severity")
	check(t, v.Name == "My work" && v.Query == "Needle" && v.Sort == "severity" && v.Revision == "1",
		"create did not return canonical trimmed preferences")
	same(t, "ordinary GET changed saved view", h.get(viewer, v.ID), v)
	adminView := h.create(h.admin, v.Name, "", "source-order")
	otherView := h.create(other, v.Name, "", "title")
	check(t, adminView.ID != v.ID && otherView.ID != v.ID && otherView.ID != adminView.ID,
		"duplicate personal names were conflated")
	for _, row := range []struct {
		who actor
		id  string
	}{{viewer, v.ID}, {h.admin, adminView.ID}, {other, otherView.ID}} {
		page := h.json(row.who, "GET", viewsPath, nil, 200)
		check(t, page.Total == 1 && len(page.Items) == 1 && view(t, page.Items[0]).ID == row.id,
			"personal list disclosed another owner/workspace")
	}
	check(t, h.json(analyst, "GET", viewsPath, nil, 200).Total == 0 &&
		h.json(foreignAdmin, "GET", viewsPath, nil, 200).Total == 0, "unrelated user's list was not empty")
	missing := strings.Repeat("f", 32)
	baseline := h.json(viewer, "GET", viewsPath+"/"+missing, nil, 404).Error
	for _, row := range []struct {
		who actor
		id  string
	}{{h.admin, v.ID}, {analyst, v.ID}, {foreignAdmin, v.ID}, {other, adminView.ID}, {viewer, missing}} {
		for _, method := range []string{"GET", "PATCH", "DELETE"} {
			var body any
			if method != "GET" {
				body = object{"revision": "1"}
			}
			r := h.json(row.who, method, viewsPath+"/"+row.id, body, 404)
			check(t, r.Error.Code == baseline.Code && r.Error.Message == baseline.Message,
				"foreign/missing view responses reveal different resource information")
		}
	}
	notMember := viewer
	notMember.Workspace = other.Workspace
	for _, method := range []string{"GET", "POST"} {
		h.denied(notMember, method, viewsPath, object{"name": "Denied", "query": "", "sort": "title"}, 403, "forbidden")
	}
	h.denied(actor{}, "POST", viewsPath, object{"name": "Denied", "query": "", "sort": "title"}, 401, "unauthorized")
	h.json(viewer, "PATCH", viewsPath+"/"+v.ID, object{"revision": v.Revision, "name": "Denied"}, 403, "Origin", "https://other.invalid")

	for _, field := range []string{"name", "query", "sort"} {
		input := object{"name": "Valid", "query": "", "sort": "title"}
		delete(input, field)
		h.denied(viewer, "POST", viewsPath, input, 400, "invalid-input")
	}
	for _, row := range []struct {
		field string
		value any
	}{
		{"name", ""}, {"name", " \t\n "}, {"name", strings.Repeat("n", 257)}, {"name", strings.Repeat("é", 129)}, {"name", "bad\x00name"},
		{"query", strings.Repeat("é", 257)}, {"query", strings.Repeat("q", 513)}, {"query", "bad\x00query"},
		{"sort", ""}, {"sort", "Severity"}, {"sort", "server-severity"},
		{"name", nil}, {"query", nil}, {"sort", nil}, {"name", 7}, {"query", []string{"q"}}, {"sort", true},
	} {
		input := object{"name": "Valid", "query": "", "sort": "title", row.field: row.value}
		h.denied(viewer, "POST", viewsPath, input, 400, "invalid-input")
		h.denied(viewer, "PATCH", viewsPath+"/"+v.ID, object{"revision": v.Revision, row.field: row.value}, 400, "invalid-input")
	}
	key := secret(t)
	h.remember(key)
	rejectedFields := object{"id": v.ID, "ownerId": viewer.User.ID, "userId": h.admin.User.ID,
		"workspaceId": viewer.Workspace, "apiVersion": "aspm/v1alpha1", "apiKey": key,
		"credentials": object{"token": key}, "rows": []any{}, "evidence": object{"text": "Not a preference"},
		"selectedIds": []string{v.ID}, "cursor": v.ID, "unknown": "untrusted"}
	for field, value := range rejectedFields {
		input := object{"name": "Valid", "query": "", "sort": "title", field: value}
		h.denied(viewer, "POST", viewsPath, input, 400, "invalid-input")
		h.denied(viewer, "PATCH", viewsPath+"/"+v.ID, object{"revision": v.Revision, field: value}, 400, "invalid-input")
	}
	h.denied(viewer, "POST", viewsPath, object{"name": "Valid", "query": "", "sort": "title", "workspaceId": other.Workspace}, 400, "invalid-input")
	h.denied(viewer, "DELETE", viewsPath+"/"+v.ID, object{"revision": v.Revision, "ownerId": h.admin.User.ID}, 400, "invalid-input")
	h.denied(viewer, "POST", viewsPath, object{"name": "Valid", "query": "", "sort": "title", "revision": "1"}, 400, "invalid-input")
	for _, raw := range [][]byte{[]byte(`null`), []byte(`[]`), []byte(`{"name":"ok","query":"","sort":"title"} {}`),
		append(append([]byte(`{"name":"`), 0xff), []byte(`","query":"","sort":"title"}`)...)} {
		h.denied(viewer, "POST", viewsPath, raw, 400, "invalid-input")
	}
	same(t, "invalid or foreign writes changed the owner's view", h.get(viewer, v.ID), v)
	boundary := h.create(viewer, " "+strings.Repeat("é", 128)+" ", " "+strings.Repeat("é", 256)+" ", "source-order")
	check(t, len(boundary.Name) == 256 && len(boundary.Query) == 512, "UTF-8 byte boundaries were not accepted exactly")
	emptyQuery := h.patch(viewer, boundary, object{"query": " \t\n ", "sort": "title"})
	check(t, emptyQuery.Query == "" && emptyQuery.Sort == "title" && emptyQuery.Name == boundary.Name,
		"sparse update clobbered a field or rejected empty all-findings query")
	h.json(h.admin, "PATCH", "/api/v1/users/"+analyst.User.ID, object{"role": "viewer"}, 200)
	h.create(analyst, "After demotion", "", "title")
	deleted := h.request(viewer, "DELETE", viewsPath+"/"+v.ID, object{"revision": v.Revision}, 204)
	check(t, deleted.Body.Len() == 0, "DELETE should have no body")
	h.denied(viewer, "GET", viewsPath+"/"+v.ID, nil, 404, "not-found")
	h.denied(viewer, "DELETE", viewsPath+"/"+v.ID, object{"revision": v.Revision}, 404, "not-found")
	same(t, "saving preferences changed findings, intake or action queues", h.snapshot(tables), before)
	t.Log("personal CRUD, strict bounded input, current viewer role and no preference-triggered search/I/O reached")
}

func TestSavedWorkViewsFreshReopenNativePagesAndRevisionRaces(t *testing.T) {
	h := newHarness(t)
	values := []savedView{h.create(h.admin, "Third title", "needle", "title"),
		h.create(h.admin, "First title", "", "source-order"), h.create(h.admin, "Second title", "literal%_", "severity")}
	wantLedger := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}
	same(t, "fresh current core must apply through additive V12 exactly once", h.ledger(), wantLedger)
	other := h.workspace()
	h.create(other, "Not in selected list", "", "title")
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	h.reopen()
	for _, value := range values {
		same(t, "saved preference/session did not survive real app reopen", h.get(h.admin, value.ID), value)
	}
	first := h.json(h.admin, "GET", viewsPath+"?limit=2", nil, 200)
	check(t, first.Total == 3 && len(first.Items) == 2 && first.NextCursor != nil &&
		*first.NextCursor == values[1].ID && view(t, first.Items[0]).ID == values[0].ID &&
		view(t, first.Items[1]).ID == values[1].ID, "native first page has wrong owner/ID order or continuation")
	last := h.json(h.admin, "GET", viewsPath+"?limit=2&cursor="+*first.NextCursor, nil, 200)
	check(t, last.Total == 3 && len(last.Items) == 1 && last.NextCursor == nil &&
		view(t, last.Items[0]).ID == values[2].ID, "native continuation duplicated/lost preference")
	end := h.json(h.admin, "GET", viewsPath+"?limit=500&cursor="+strings.Repeat("f", 32), nil, 200)
	check(t, end.Items != nil && len(end.Items) == 0 && end.Total == 3 && end.NextCursor == nil, "terminal cursor response differs from native pages")
	check(t, h.json(h.admin, "GET", viewsPath, nil, 200).Total == 3, "default native list limit failed")
	for _, query := range []string{"limit=0", "limit=501", "limit=bad", "limit=1.5", "cursor=bad", "cursor=" + strings.Repeat("A", 32)} {
		h.denied(h.admin, "GET", viewsPath+"?"+query, nil, 400, "invalid-input")
	}
	v := values[0]
	h.clock.Add(int64(time.Second))
	updated := h.patch(h.admin, v, object{"name": " Renamed "})
	check(t, updated.Name == "Renamed" && updated.Query == v.Query && updated.Sort == v.Sort &&
		updated.Revision == "2" && updated.CreatedAt.Equal(v.CreatedAt) && updated.UpdatedAt.After(v.UpdatedAt),
		"meaningful sparse PATCH did not advance exactly one revision")
	same(t, "no-op PATCH changed revision/timestamps", h.patch(h.admin, updated, object{"name": " Renamed "}), updated)
	same(t, "revision-only no-op clobbered data", h.patch(h.admin, updated, object{}), updated)
	for _, method := range []string{"PATCH", "DELETE"} {
		h.denied(h.admin, method, viewsPath+"/"+v.ID, object{"revision": v.Revision}, 409, "conflict")
		h.denied(h.admin, method, viewsPath+"/"+v.ID, object{}, 400, "invalid-input")
		for _, bad := range []any{nil, 1, "", "0", "-1", "01", "bad", "9223372036854775808"} {
			h.denied(h.admin, method, viewsPath+"/"+v.ID, object{"revision": bad}, 400, "invalid-input")
		}
	}
	same(t, "stale/invalid mutations changed canonical data", h.get(h.admin, v.ID), updated)
	h.clock.Add(int64(time.Second))
	race := func(id, revision string, deleteSecond bool) [2]*httptest.ResponseRecorder {
		t.Helper()
		var responses [2]*httptest.ResponseRecorder
		var ready sync.WaitGroup
		start := make(chan struct{})
		for i := range responses {
			ready.Add(1)
			go func() {
				defer ready.Done()
				<-start
				method, body := "PATCH", object{"revision": revision, "name": []string{"Left edit", "Right edit"}[i]}
				if deleteSecond && i == 1 {
					method, body = "DELETE", object{"revision": revision}
				}
				responses[i] = h.request(h.admin, method, viewsPath+"/"+id, body, 0)
			}()
		}
		close(start)
		ready.Wait()
		check(t, responses[0] != nil && responses[1] != nil, "concurrent real HTTP call failed before response")
		return responses
	}
	concurrent := race(updated.ID, updated.Revision, false)
	wins, conflicts := 0, 0
	var winner savedView
	for _, response := range concurrent {
		value := h.decode(response)
		switch response.Code {
		case 200:
			wins++
			winner = view(t, value.View)
		case 409:
			conflicts++
			check(t, value.Error.Code == "conflict", "stale racing PATCH lacks conflict")
		default:
			t.Fatalf("concurrent PATCH returned %d; want one 200 and one 409", response.Code)
		}
	}
	check(t, wins == 1 && conflicts == 1 && winner.Revision == "3", "two stale revisions changed state or no writer won")
	same(t, "race winner differs from durable canonical value", h.get(h.admin, winner.ID), winner)
	target := values[2]
	deletion := race(target.ID, target.Revision, true)
	if deletion[0].Code == 200 {
		check(t, deletion[1].Code == 409 && h.decode(deletion[1]).Error.Code == "conflict", "stale DELETE removed a newer edit")
		current := view(t, h.decode(deletion[0]).View)
		same(t, "winning PATCH was not preserved", h.get(h.admin, target.ID), current)
		h.request(h.admin, "DELETE", viewsPath+"/"+target.ID, object{"revision": current.Revision}, 204)
	} else {
		check(t, deletion[1].Code == 204 && deletion[0].Code == 404 &&
			h.decode(deletion[0]).Error.Code == "not-found", "delete/PATCH race has neither allowed serial outcome")
	}
	h.denied(h.admin, "GET", viewsPath+"/"+target.ID, nil, 404, "not-found")
	h.reopen()
	same(t, "race winner lost on second app reopen", h.get(h.admin, winner.ID), winner)
	same(t, "reopen repeated/skipped migration", h.ledger(), wantLedger)
	t.Log("fresh V10, actual reopen, native ID pages, stale revisions and atomic mutation races reached")
}
