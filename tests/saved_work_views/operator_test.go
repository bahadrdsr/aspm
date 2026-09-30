//go:build integration

package saved_work_views

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type operatorControl struct {
	h                            *harness
	target, retained, witness    actor
	actorID, workspaceID, schema string
}

func (h *harness) operatorControl() *operatorControl {
	h.t.Helper()
	target := h.user(h.admin, "admin")
	retained := h.workspaceFor(target)
	witness := h.user(retained, "admin")
	h.json(witness, "PATCH", "/api/v1/users/"+target.User.ID, object{"role": "viewer"}, 200)
	h.json(h.admin, "PATCH", "/api/v1/users/"+target.User.ID, object{"role": "analyst"}, 200)
	check(h.t, target.User.ID != h.admin.User.ID && target.Cookie != nil &&
		regexp.MustCompile(`^[a-f0-9]{24}@views\.invalid$`).MatchString(target.User.Email),
		"operator subject must be a newly generated HTTP fixture actor")
	return &operatorControl{h: h, target: target, retained: retained, witness: witness,
		actorID: target.User.ID, workspaceID: target.Workspace, schema: h.ownedSchema}
}

// The only approved business-SQL exception models an operator, not an HTTP API.
func (c *operatorControl) revoke() {
	h := c.h
	h.t.Helper()
	suffix := strings.TrimPrefix(c.schema, "work_views_")
	check(h.t, !h.operatorRevoked && c.schema != "" && c.schema == h.ownedSchema && c.schema == h.config.Schema &&
		regexp.MustCompile(`^work_views_[a-f0-9]{24}$`).MatchString(c.schema) &&
		h.config.ApplicationName == "work-views-"+suffix && h.config.Storage.Prefix == "saved-work-views/"+suffix+"/" &&
		c.target.User.ID == c.actorID && c.target.Workspace == c.workspaceID && c.workspaceID == h.admin.Workspace &&
		c.retained.User.ID == c.actorID && c.retained.Workspace != c.workspaceID,
		"refusing operator SQL outside this recorded generated actor/schema/workspace or more than once")
	tx, err := h.db.Begin(h.ctx)
	must(h.t, "begin independent owned operator transaction", err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			h.t.Errorf("operator fixture rollback failed (%T; details withheld)", err)
		}
	}()
	var schema, name string
	var owned bool
	must(h.t, "verify qualified relation belongs to current fixture owner", tx.QueryRow(h.ctx,
		`SELECT n.nspname,c.relname,pg_get_userbyid(n.nspowner)=current_user
		 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass($1)`,
		h.table("memberships")).Scan(&schema, &name, &owned))
	check(h.t, owned && schema == c.schema && name == "app_memberships",
		"operator fixture schema/name/owner verification failed")
	var role string
	must(h.t, "lock and verify generated subject's current membership", tx.QueryRow(h.ctx,
		"SELECT role FROM "+h.table("memberships")+" WHERE workspace_id=$1 AND user_id=$2 FOR UPDATE",
		c.workspaceID, c.actorID).Scan(&role))
	check(h.t, role == "analyst" || role == "viewer", "operator exception cannot remove an admin membership")
	must(h.t, "verify retained membership is also non-admin", tx.QueryRow(h.ctx,
		"SELECT role FROM "+h.table("memberships")+" WHERE workspace_id=$1 AND user_id=$2",
		c.retained.Workspace, c.actorID).Scan(&role))
	check(h.t, role == "viewer", "operator subject's retained membership is not the expected viewer")
	rows, err := tx.Query(h.ctx, "DELETE FROM "+h.table("memberships")+
		" WHERE workspace_id=$1 AND user_id=$2 RETURNING workspace_id,user_id", c.workspaceID, c.actorID)
	must(h.t, "remove only the approved generated membership", err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var workspace, user string
		must(h.t, "read operator DELETE RETURNING identity", rows.Scan(&workspace, &user))
		check(h.t, workspace == c.workspaceID && user == c.actorID, "operator DELETE returned an out-of-scope identity")
		count++
	}
	must(h.t, "finish exact operator RETURNING rows", rows.Err())
	rows.Close()
	check(h.t, count == 1, "operator exception must return exactly one membership")
	must(h.t, "commit the single approved operator revocation", tx.Commit(h.ctx))
	h.operatorRevoked = true
	h.t.Log("OPERATOR FIXTURE: verified owned schema/name; one non-admin membership DELETE committed with both keys and exactly one RETURNING row; not a public removal API")
}

func (c *operatorControl) sessionsRemainValid() {
	h := c.h
	h.t.Helper()
	sameActor := h.json(c.target, "GET", "/api/v1/session", nil, 200)
	check(h.t, sameActor.User.ID == c.actorID && len(sameActor.Workspaces) == 1 &&
		sameActor.Workspaces[0].ID == c.retained.Workspace && sameActor.Workspaces[0].Role == "viewer",
		"revocation invalidated the session, removed the other membership or retained the revoked membership")
	witness := h.json(c.witness, "GET", "/api/v1/session", nil, 200)
	check(h.t, witness.User.ID == c.witness.User.ID && len(witness.Workspaces) == 1 &&
		witness.Workspaces[0].ID == c.retained.Workspace && witness.Workspaces[0].Role == "admin",
		"operator revocation damaged an independent actor's membership/session")
}

func (h *harness) revoked(who actor, method, path string, body any, privateValues ...string) {
	h.t.Helper()
	rr := h.request(who, method, path, body, 403)
	failure := h.decode(rr).Error
	check(h.t, failure != nil && failure.Code == "forbidden" && failure.Message == "This operation is not permitted",
		"removed membership did not return the ordinary opaque 403")
	for _, value := range privateValues {
		if value != "" {
			check(h.t, !bytes.Contains(rr.Body.Bytes(), []byte(value)), "revocation denial disclosed private view/finding payload")
		}
	}
}
