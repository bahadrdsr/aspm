package app

import (
	"bytes"
	"context"
	"net/http"
	"regexp"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

var sourceRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}/[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
var sourceRepositoryID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func validSourceRepository(repository string) bool {
	return len(repository) <= 256 && sourceRepositoryName.MatchString(repository)
}

func sourceCredentialAAD(workspace, id string) []byte {
	return []byte("aspm/source-credential/v1\x00" + connectors.GitHubCloudApp + "\x00" + workspace + "\x00" + id)
}

func (a *Application) sourceSessionRole(ctx context.Context, tx pgx.Tx, session authenticatedSession, workspace string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM `+a.table("workspaces")+` WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&id); err != nil {
		return "", err
	}
	return a.sessionRole(ctx, tx, session, workspace)
}

func (a *Application) createSource(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input struct {
		Profile    string `json:"profile"`
		Name       string `json:"name"`
		Repository string `json:"repository"`
		Token      string `json:"token"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if input.Profile != connectors.GitHubCloudApp || !validText(input.Name, 256) ||
		!validSourceRepository(input.Repository) || !validIntegrationToken(input.Token) || input.Enabled == nil {
		return errInvalid
	}
	if a.integrationCredentials == nil {
		return errUnavailable
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sourceSessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	id := newID()
	ciphertext, err := sealCredential(a.integrationCredentials, sourceCredentialAAD(workspace, id), input.Token)
	if err != nil {
		return err
	}
	record, err := scanSourceConnection(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("source_connections")+`
		(id,workspace_id,profile,name,repository,credential_ciphertext,enabled,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING `+sourceConnectionColumns,
		id, workspace, input.Profile, input.Name, input.Repository, ciphertext, *input.Enabled, a.config.Now().UTC()))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source": record.SourceConnection})
	return nil
}

func (a *Application) updateSource(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		Name       optional[string] `json:"name"`
		Repository optional[string] `json:"repository"`
		Token      optional[string] `json:"token"`
		Enabled    optional[bool]   `json:"enabled"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if input.Name.Set && (input.Name.Value == nil || !validText(*input.Name.Value, 256)) ||
		input.Repository.Set && (input.Repository.Value == nil || !validSourceRepository(*input.Repository.Value)) ||
		input.Token.Set && (input.Token.Value == nil || !validIntegrationToken(*input.Token.Value)) ||
		input.Enabled.Set && input.Enabled.Value == nil {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sourceSessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	record, err := scanSourceConnection(tx.QueryRow(r.Context(), `SELECT `+sourceConnectionColumns+
		` FROM `+a.table("source_connections")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	changed := false
	if input.Name.Set && record.Name != *input.Name.Value {
		record.Name, changed = *input.Name.Value, true
	}
	if input.Repository.Set && record.Repository != *input.Repository.Value {
		record.Repository, changed = *input.Repository.Value, true
	}
	if input.Enabled.Set && record.Enabled != *input.Enabled.Value {
		record.Enabled, changed = *input.Enabled.Value, true
	}
	if input.Token.Set {
		plain, err := openCredential(a.integrationCredentials, sourceCredentialAAD(workspace, id), record.ciphertext)
		if err != nil {
			return err
		}
		same := bytes.Equal(plain, []byte(*input.Token.Value))
		clear(plain)
		if !same {
			record.ciphertext, err = sealCredential(a.integrationCredentials, sourceCredentialAAD(workspace, id), *input.Token.Value)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		record, err = scanSourceConnection(tx.QueryRow(r.Context(), `UPDATE `+a.table("source_connections")+`
			SET name=$3,repository=$4,credential_ciphertext=$5,enabled=$6,revision=revision+1,updated_at=$7
			WHERE workspace_id=$1 AND id=$2 RETURNING `+sourceConnectionColumns,
			workspace, id, record.Name, record.Repository, record.ciphertext, record.Enabled, a.config.Now().UTC()))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": record.SourceConnection})
	return nil
}

func (a *Application) getSource(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanSourceConnection(a.pool.QueryRow(r.Context(), `SELECT `+sourceConnectionColumns+
		` FROM `+a.table("source_connections")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": record.SourceConnection})
	return nil
}

func (a *Application) listSources(w http.ResponseWriter, r *http.Request, workspace string) error {
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
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("source_connections")+`
		WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sourceConnectionColumns+` FROM `+a.table("source_connections")+`
		WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []SourceConnection{}
	for rows.Next() {
		record, scanErr := scanSourceConnection(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.SourceConnection)
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
