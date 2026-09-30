package app

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type WorkView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	Sort      string    `json:"sort"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

const workViewColumns = `id,name,query,sort,revision::text,created_at,updated_at`
const workViewScope = `workspace_id=$1 AND user_id=$2`

func scanWorkView(row pgx.Row) (WorkView, error) {
	var view WorkView
	err := row.Scan(&view.ID, &view.Name, &view.Query, &view.Sort, &view.Revision, &view.CreatedAt, &view.UpdatedAt)
	return view, err
}

type workViewInput struct {
	Name  optional[string] `json:"name"`
	Query optional[string] `json:"query"`
	Sort  optional[string] `json:"sort"`
}

func applyWorkViewInput(view WorkView, input workViewInput) (WorkView, bool, error) {
	next := view
	for _, field := range []struct {
		input  optional[string]
		target *string
	}{{input.Name, &next.Name}, {input.Query, &next.Query}} {
		if field.input.Set {
			if field.input.Value == nil {
				return WorkView{}, false, errInvalid
			}
			*field.target = strings.TrimSpace(*field.input.Value)
		}
	}
	if input.Sort.Set {
		if input.Sort.Value == nil {
			return WorkView{}, false, errInvalid
		}
		next.Sort = *input.Sort.Value
	}
	if !utf8.ValidString(next.Name) || !validText(next.Name, 256) ||
		!utf8.ValidString(next.Query) || len(next.Query) > 512 || strings.ContainsRune(next.Query, 0) ||
		(next.Sort != "source-order" && next.Sort != "severity" && next.Sort != "title") {
		return WorkView{}, false, errInvalid
	}
	return next, next != view, nil
}

func workViewRevision(input optional[string]) (string, error) {
	if !input.Set || input.Value == nil {
		return "", errInvalid
	}
	value := *input.Value
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision <= 0 || strconv.FormatInt(revision, 10) != value {
		return "", errInvalid
	}
	return value, nil
}

func (a *Application) authorizeWorkViewWrite(ctx context.Context, tx pgx.Tx, workspace string, session authenticatedSession) error {
	// Workspace, session, membership, then preference matches membership changes.
	// Shared authority locks keep a committed revocation ahead of a later write.
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM `+a.table("workspaces")+` WHERE id=$1 FOR SHARE`,
		workspace).Scan(&id); err != nil {
		return err
	}
	_, err := a.sessionRole(ctx, tx, session, workspace)
	return err
}

func (a *Application) readWorkView(ctx context.Context, workspace, user, id string) (WorkView, error) {
	return scanWorkView(a.pool.QueryRow(ctx, `SELECT `+workViewColumns+` FROM `+a.table("work_views")+
		` WHERE `+workViewScope+` AND id=$3`, workspace, user, id))
}

func (a *Application) getWorkView(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	view, err := a.readWorkView(r.Context(), workspace, session.User.ID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"view": view})
	return nil
}

func (a *Application) listWorkViews(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("work_views")+
		` WHERE `+workViewScope, workspace, session.User.ID).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+workViewColumns+` FROM `+a.table("work_views")+
		` WHERE `+workViewScope+` AND id>$3 ORDER BY id LIMIT $4`, workspace, session.User.ID, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []WorkView{}
	for rows.Next() {
		view, scanErr := scanWorkView(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, view)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}

func (a *Application) createWorkView(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input workViewInput
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !input.Name.Set || !input.Query.Set || !input.Sort.Set {
		return errInvalid
	}
	view, _, err := applyWorkViewInput(WorkView{}, input)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeWorkViewWrite(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	view, err = scanWorkView(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("work_views")+`
		(id,workspace_id,user_id,name,query,sort,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$7) RETURNING `+workViewColumns,
		newID(), workspace, session.User.ID, view.Name, view.Query, view.Sort, a.config.Now().UTC()))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"view": view})
	return nil
}

func (a *Application) updateWorkView(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		workViewInput
		Revision optional[string] `json:"revision"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	revision, err := workViewRevision(input.Revision)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeWorkViewWrite(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	view, err := scanWorkView(tx.QueryRow(r.Context(), `SELECT `+workViewColumns+` FROM `+a.table("work_views")+
		` WHERE `+workViewScope+` AND id=$3 FOR UPDATE`, workspace, session.User.ID, id))
	if err != nil {
		return err
	}
	if view.Revision != revision {
		return errConflict
	}
	next, changed, err := applyWorkViewInput(view, input.workViewInput)
	if err != nil {
		return err
	}
	if changed {
		view, err = scanWorkView(tx.QueryRow(r.Context(), `UPDATE `+a.table("work_views")+`
			SET name=$4,query=$5,sort=$6,revision=revision+1,
				updated_at=GREATEST($7::timestamptz,updated_at+interval '1 microsecond')
			WHERE `+workViewScope+` AND id=$3 RETURNING `+workViewColumns,
			workspace, session.User.ID, id, next.Name, next.Query, next.Sort, a.config.Now().UTC()))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"view": view})
	return nil
}

func (a *Application) deleteWorkView(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		Revision optional[string] `json:"revision"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	revision, err := workViewRevision(input.Revision)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeWorkViewWrite(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	view, err := scanWorkView(tx.QueryRow(r.Context(), `SELECT `+workViewColumns+` FROM `+a.table("work_views")+
		` WHERE `+workViewScope+` AND id=$3 FOR UPDATE`, workspace, session.User.ID, id))
	if err != nil {
		return err
	}
	if view.Revision != revision {
		return errConflict
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM `+a.table("work_views")+
		` WHERE `+workViewScope+` AND id=$3`, workspace, session.User.ID, id); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
