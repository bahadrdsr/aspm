//go:build integration

package saved_work_views

import (
	"encoding/csv"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/internal/app"
)

func (h *harness) work(who actor, selector string) []app.WorkItem {
	h.t.Helper()
	result, cursor := []app.WorkItem{}, ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		value := h.json(who, "GET", "/api/v1/work?"+selector+"&limit=1&cursor="+cursor, nil, 200)
		check(h.t, value.DataOrigin == "live" && len(value.Items) <= 1 && value.Items != nil, "Work lost native bounded live page")
		for _, raw := range value.Items {
			var item app.WorkItem
			must(h.t, "decode real Work page", json.Unmarshal(raw, &item))
			check(h.t, item.ID > cursor && !seen[item.ID], "Work reordered or duplicated native IDs")
			seen[item.ID] = true
			result = append(result, item)
		}
		if value.NextCursor == nil {
			check(h.t, value.Total == len(result), "quiescent Work total disagrees with actual IDs")
			return result
		}
		check(h.t, len(value.Items) == 1 && *value.NextCursor == result[len(result)-1].ID &&
			*value.NextCursor > cursor, "Work continuation is not last returned native ID")
		cursor = *value.NextCursor
	}
	h.t.Fatal("small quiescent Work fixture exceeded native page budget")
	return nil
}
func ids(items []app.WorkItem) []string {
	result := []string{}
	for _, item := range items {
		result = append(result, item.ID)
	}
	sort.Strings(result)
	return result
}
func (h *harness) csv(who actor, selector string, expected []app.WorkItem) {
	h.t.Helper()
	rr := h.request(who, "GET", "/api/v1/work/export?format=csv&"+selector, nil, 200)
	response := rr.Result()
	defer response.Body.Close()
	check(h.t, strings.HasPrefix(response.Header.Get("Content-Type"), "text/csv") &&
		response.Header.Get("Content-Disposition") == `attachment; filename="work.csv"` &&
		response.Trailer.Get("X-ASPM-Export-Status") == "complete", "CSV format or completed-stream behavior changed")
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	must(h.t, "read actual CSV", err)
	check(h.t, len(rows) == len(expected)+1, "CSV row count differs from actual filtered finding count")
	same(h.t, "CSV native column contract changed", rows[0], []string{"id", "title", "assetName", "severity",
		"ownerName", "workflowState", "sourceScanAt", "collectedAt", "importedAt", "changeKind", "changeAt"})
	byID := map[string]app.WorkItem{}
	for _, item := range expected {
		byID[item.ID] = item
	}
	actual := []string{}
	for _, row := range rows[1:] {
		item, present := byID[row[0]]
		check(h.t, present, "CSV contains a duplicate or unauthorized/nonmatching actual finding ID")
		title := item.Title
		if strings.HasPrefix(title, "+") {
			title = "'" + title
		}
		check(h.t, row[1] == title && row[2] == item.AssetName && row[3] == item.Severity &&
			row[5] == item.WorkflowState, "CSV changed canonical finding fields or formula escaping")
		actual = append(actual, row[0])
		delete(byID, row[0])
	}
	check(h.t, len(byID) == 0 && sort.StringsAreSorted(actual), "CSV lost IDs or applied saved presentation as server sort")
}

func TestSavedWorkViewsQuiescentQueryCSVAndDeniedScope(t *testing.T) {
	h := newHarness(t)
	_, _, first := h.seed(h.admin, "Source repository", &h.admin.User.ID, "Title Needle", "+Literal 100%_needle")
	_, _, second := h.seed(h.admin, "Asset Beacon", nil, "Unrelated configuration", "Different setting")
	all := append(append([]app.WorkItem{}, first...), second...)
	byTitle := map[string]string{}
	for _, item := range all {
		byTitle[item.Title] = item.ID
	}
	other := h.workspace()
	_, _, outside := h.seed(other, "Foreign repository", nil, "Foreign Needle")
	check(t, len(outside) == 1, "foreign-scope finding fixture missing")
	t.Log("real S3 intake completed: four selected-workspace finding IDs and one foreign-workspace ID")
	tables := []string{"assets", "findings", "observations", "notes", "imports", "source_collections", "assessment_previews", "assessment_jobs", "finding_deliveries"}
	before, writes := h.snapshot(tables), h.writes.Load()
	var allView savedView
	for i, row := range []struct {
		query string
		want  []string
	}{
		{"nEeDlE", []string{byTitle["Title Needle"], byTitle["+Literal 100%_needle"]}},
		{"asset beacon", ids(second)}, {"morgan owner", ids(first)},
		{"%_", []string{byTitle["+Literal 100%_needle"]}}, {"description-token", []string{}}, {"", ids(all)},
	} {
		v := h.create(h.admin, "Personal filter", " \t"+row.query+" \n", []string{"source-order", "severity", "title"}[i%3])
		check(t, v.Query == row.query, "saved query did not return canonical exact Work search text")
		sort.Strings(row.want)
		direct := h.work(h.admin, "q="+url.QueryEscape(v.Query))
		selected := h.work(h.admin, "viewId="+v.ID)
		same(t, "q-only Work IDs disagree with independently imported expected membership", ids(direct), row.want)
		same(t, "authorized saved view changed actual Work finding membership", ids(selected), row.want)
		h.csv(h.admin, "viewId="+v.ID, selected)
		h.csv(h.admin, "q="+url.QueryEscape(v.Query), direct)
		if row.query == "" {
			allView = v
		}
	}
	// Existing q is literal, whereas saving canonicalizes surrounding whitespace.
	same(t, "q-only server semantics changed", ids(h.work(h.admin, "q="+url.QueryEscape(" Title Needle "))), []string{})
	for _, sortOrder := range []string{"source-order", "severity", "title"} {
		allView = h.patch(h.admin, allView, object{"sort": sortOrder})
		same(t, "saved loaded sort changed native Work pages", ids(h.work(h.admin, "viewId="+allView.ID)), ids(all))
	}
	h.csv(h.admin, "viewId="+allView.ID+"&limit=1&cursor="+strings.Repeat("f", 32), all)
	for _, selector := range []string{
		"viewId=", "viewId=bad", "viewId=" + allView.ID + "&viewId=" + allView.ID,
		"q=&viewId=" + allView.ID, "q=needle&viewId=" + allView.ID,
	} {
		for _, path := range []string{"/api/v1/work?", "/api/v1/work/export?format=csv&"} {
			h.denied(h.admin, "GET", path+selector, nil, 400, "invalid-input")
		}
	}
	for _, suffix := range []string{"limit=0", "limit=501", "cursor=bad"} {
		h.denied(h.admin, "GET", "/api/v1/work?viewId="+allView.ID+"&"+suffix, nil, 400, "invalid-input")
	}
	h.denied(h.admin, "GET", "/api/v1/work/export?format=json&viewId="+allView.ID, nil, 415, "unsupported-format")
	same(t, "preferences/reads changed findings, evidence or action queues", h.snapshot(tables), before)
	check(t, writes == h.writes.Load(), "saving/applying/exporting preferences performed native S3 I/O")
	control := h.operatorControl()
	_, _, retainedWork := h.seed(control.witness, "Retained membership repository", nil, "Retained membership finding")
	before, writes = h.snapshot(tables), h.writes.Load()
	viewer := control.target
	own := h.create(viewer, "Personal even after demotion", "", "severity")
	retainedView := h.create(control.retained, "Other membership preference", "", "title")
	same(t, "analyst could not apply own preference", ids(h.work(viewer, "viewId="+own.ID)), ids(all))
	h.json(h.admin, "PATCH", "/api/v1/users/"+viewer.User.ID, object{"role": "viewer"}, 200)
	own = h.patch(viewer, own, object{"name": "Still personal as viewer"})
	same(t, "current viewer cannot read own Work filter", ids(h.work(viewer, "viewId="+own.ID)), ids(all))
	h.csv(viewer, "viewId="+own.ID, all)
	h.denied(viewer, "PATCH", "/api/v1/findings/"+first[0].ID, object{"workflowState": "resolved"}, 403, "forbidden")
	h.denied(viewer, "POST", "/api/v1/findings/"+first[0].ID+"/notes", object{"text": "Denied"}, 403, "forbidden")
	for _, path := range []string{"/api/v1/work?", "/api/v1/work/export?format=csv&"} {
		h.denied(h.admin, "GET", path+"viewId="+own.ID, nil, 404, "not-found")
		h.denied(other, "GET", path+"viewId="+allView.ID, nil, 404, "not-found")
		h.denied(h.admin, "GET", path+"viewId="+strings.Repeat("f", 32), nil, 404, "not-found")
		notMember := viewer
		notMember.Workspace = other.Workspace
		h.denied(notMember, "GET", path+"viewId="+own.ID, nil, 403, "forbidden")
		h.denied(actor{}, "GET", path+"viewId="+allView.ID, nil, 401, "unauthorized")
	}
	same(t, "owned metadata was not readable before revocation", h.get(viewer, own.ID), own)
	check(t, h.json(viewer, "GET", viewsPath, nil, 200).Total == 1, "original personal list was not scoped before revocation")
	same(t, "retained saved filter was not previously allowed", ids(h.work(control.retained, "viewId="+retainedView.ID)), ids(retainedWork))
	h.csv(control.retained, "viewId="+retainedView.ID, retainedWork)
	control.revoke()
	control.sessionsRemainValid()
	for _, path := range []string{viewsPath, viewsPath + "/" + own.ID,
		"/api/v1/work?viewId=" + own.ID, "/api/v1/work/export?format=csv&viewId=" + own.ID} {
		h.revoked(viewer, "GET", path, nil, own.ID, own.Name, own.Query, first[0].ID, first[0].Title)
	}
	h.revoked(viewer, "POST", viewsPath, object{"name": "Denied", "query": "", "sort": "title"}, own.ID, own.Name)
	h.revoked(viewer, "PATCH", viewsPath+"/"+own.ID, object{"revision": own.Revision, "name": "Denied"}, own.ID, own.Name)
	h.revoked(viewer, "DELETE", viewsPath+"/"+own.ID, object{"revision": own.Revision}, own.ID, own.Name)
	same(t, "another membership's saved view was lost", h.get(control.retained, retainedView.ID), retainedView)
	same(t, "retained membership cannot apply its saved filter", ids(h.work(control.retained, "viewId="+retainedView.ID)), ids(retainedWork))
	h.csv(control.retained, "viewId="+retainedView.ID, retainedWork)
	same(t, "independent session cannot read its own workspace", ids(h.work(control.witness, "q=")), ids(retainedWork))
	same(t, "another actor lost access in the revoked workspace", ids(h.work(h.admin, "viewId="+allView.ID)), ids(all))
	same(t, "preferences/reads changed findings, evidence or action queues", h.snapshot(tables), before)
	check(t, writes == h.writes.Load(), "saving/applying/exporting preferences performed native S3 I/O")
	t.Log("actual quiescent q/viewId/CSV membership, operator-side membership-loss replay and retained session/workspace isolation reached; no public removal workflow claimed")
}
