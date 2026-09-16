package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

const assetColumns = "id,workspace_id,name,kind,environment,criticality,tags,owner_id"

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanAsset(row pgx.Row) (Asset, error) {
	var v Asset
	err := row.Scan(&v.ID, &v.WorkspaceID, &v.Name, &v.Kind, &v.Environment, &v.Criticality, &v.Tags, &v.OwnerID)
	return v, err
}

func (a *Application) checkOwner(ctx context.Context, db queryRower, workspace string, owner *string) error {
	if owner == nil {
		return nil
	}
	if !validID(*owner) {
		return errInvalid
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("memberships")+` WHERE workspace_id=$1 AND user_id=$2)`,
		workspace, *owner).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errInvalid
	}
	return nil
}

func validAsset(v Asset) bool {
	if !validText(v.Name, 256) || !validText(v.Kind, 64) ||
		len(v.Environment) > 128 || strings.ContainsRune(v.Environment, 0) || len(v.Tags) > 64 {
		return false
	}
	switch v.Criticality {
	case "low", "medium", "high", "critical":
	default:
		return false
	}
	for _, tag := range v.Tags {
		if !validText(tag, 128) {
			return false
		}
	}
	return true
}

func (a *Application) createAsset(w http.ResponseWriter, r *http.Request, workspace string) error {
	var input struct {
		Name        string   `json:"name"`
		Kind        string   `json:"kind"`
		Environment string   `json:"environment"`
		Criticality string   `json:"criticality"`
		Tags        []string `json:"tags"`
		OwnerID     *string  `json:"ownerId"`
	}
	if err := a.decode(w, r, &input, 64<<10); err != nil {
		return err
	}
	if input.Criticality == "" {
		input.Criticality = "medium"
	}
	if input.Tags == nil {
		input.Tags = []string{}
	}
	asset := Asset{ID: newID(), WorkspaceID: workspace, Name: input.Name, Kind: input.Kind,
		Environment: input.Environment, Criticality: input.Criticality, Tags: input.Tags, OwnerID: input.OwnerID}
	if !validAsset(asset) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.checkOwner(r.Context(), tx, workspace, asset.OwnerID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("assets")+`
		(id,workspace_id,name,kind,environment,criticality,tags,owner_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		asset.ID, workspace, asset.Name, asset.Kind, asset.Environment, asset.Criticality, asset.Tags, asset.OwnerID, a.config.Now().UTC()); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 201, map[string]any{"asset": asset})
	return nil
}

func (a *Application) assetResource(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	if r.Method == http.MethodGet {
		asset, err := scanAsset(a.pool.QueryRow(r.Context(), `SELECT `+assetColumns+` FROM `+a.table("assets")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"asset": asset})
		return nil
	}
	if r.Method == http.MethodDelete {
		result, err := a.pool.Exec(r.Context(), `DELETE FROM `+a.table("assets")+` WHERE workspace_id=$1 AND id=$2`, workspace, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return errNotFound
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	var input struct {
		Name        optional[string]   `json:"name"`
		Kind        optional[string]   `json:"kind"`
		Environment optional[string]   `json:"environment"`
		Criticality optional[string]   `json:"criticality"`
		Tags        optional[[]string] `json:"tags"`
		OwnerID     optional[string]   `json:"ownerId"`
	}
	if err := a.decode(w, r, &input, 64<<10); err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	asset, err := scanAsset(tx.QueryRow(r.Context(), `SELECT `+assetColumns+` FROM `+a.table("assets")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	for _, field := range []struct {
		patch optional[string]
		dest  *string
	}{{input.Name, &asset.Name}, {input.Kind, &asset.Kind}, {input.Environment, &asset.Environment}, {input.Criticality, &asset.Criticality}} {
		if field.patch.Set {
			if field.patch.Value == nil {
				return errInvalid
			}
			*field.dest = *field.patch.Value
		}
	}
	if input.Tags.Set {
		if input.Tags.Value == nil {
			return errInvalid
		}
		asset.Tags = *input.Tags.Value
	}
	if input.OwnerID.Set {
		asset.OwnerID = input.OwnerID.Value
	}
	if !validAsset(asset) {
		return errInvalid
	}
	if err = a.checkOwner(r.Context(), tx, workspace, asset.OwnerID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("assets")+` SET name=$3,kind=$4,environment=$5,criticality=$6,tags=$7,owner_id=$8
		WHERE workspace_id=$1 AND id=$2`, workspace, id, asset.Name, asset.Kind, asset.Environment, asset.Criticality, asset.Tags, asset.OwnerID); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"asset": asset})
	return nil
}

func (a *Application) listAssets(w http.ResponseWriter, r *http.Request, workspace string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	var total int
	if err = a.pool.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("assets")+` WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := a.pool.Query(r.Context(), `SELECT `+assetColumns+` FROM `+a.table("assets")+` WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []Asset{}
	for rows.Next() {
		v, err := scanAsset(rows)
		if err != nil {
			return err
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}
