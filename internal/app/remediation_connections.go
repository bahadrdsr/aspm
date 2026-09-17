package app

import (
	"bytes"
	"net/http"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type IntegrationConnection struct {
	ID                   string    `json:"id"`
	WorkspaceID          string    `json:"workspaceId"`
	Profile              string    `json:"profile"`
	Name                 string    `json:"name"`
	Channel              string    `json:"channel"`
	Enabled              bool      `json:"enabled"`
	CredentialConfigured bool      `json:"credentialConfigured"`
	Revision             int64     `json:"revision"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type integrationConnectionRecord struct {
	IntegrationConnection
	ciphertext []byte
}

const integrationConnectionColumns = `id,workspace_id,profile,name,channel,enabled,
	revision,created_at,updated_at,credential_ciphertext`

func scanIntegrationConnection(row pgx.Row) (integrationConnectionRecord, error) {
	var record integrationConnectionRecord
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.Profile, &record.Name, &record.Channel,
		&record.Enabled, &record.Revision, &record.CreatedAt, &record.UpdatedAt, &record.ciphertext)
	record.CredentialConfigured = len(record.ciphertext) > 0
	return record, err
}

func (a *Application) createIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
		Channel string `json:"channel"`
		Token   string `json:"token"`
		Enabled *bool  `json:"enabled"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if input.Profile != connectors.SlackWorkspaceBot || !validText(input.Name, 256) ||
		!integrationChannel.MatchString(input.Channel) || !validIntegrationToken(input.Token) || input.Enabled == nil {
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
	var workspaceID string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("workspaces")+`
		WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&workspaceID); err != nil {
		return err
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	id := newID()
	ciphertext, err := sealIntegrationToken(a.integrationCredentials, workspace, id, input.Token)
	if err != nil {
		return err
	}
	record, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("integration_connections")+`
		(id,workspace_id,profile,name,channel,credential_ciphertext,enabled,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING `+integrationConnectionColumns,
		id, workspace, input.Profile, input.Name, input.Channel, ciphertext, *input.Enabled, a.config.Now().UTC()))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) updateIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		Name    optional[string] `json:"name"`
		Channel optional[string] `json:"channel"`
		Token   optional[string] `json:"token"`
		Enabled optional[bool]   `json:"enabled"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if input.Name.Set && (input.Name.Value == nil || !validText(*input.Name.Value, 256)) ||
		input.Channel.Set && (input.Channel.Value == nil || !integrationChannel.MatchString(*input.Channel.Value)) ||
		input.Token.Set && (input.Token.Value == nil || !validIntegrationToken(*input.Token.Value)) ||
		input.Enabled.Set && input.Enabled.Value == nil {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	record, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	changed := false
	if input.Name.Set && record.Name != *input.Name.Value {
		record.Name, changed = *input.Name.Value, true
	}
	if input.Channel.Set && record.Channel != *input.Channel.Value {
		record.Channel, changed = *input.Channel.Value, true
	}
	if input.Enabled.Set && record.Enabled != *input.Enabled.Value {
		record.Enabled, changed = *input.Enabled.Value, true
	}
	if input.Token.Set {
		prior, err := openIntegrationToken(a.integrationCredentials, workspace, id, record.ciphertext)
		if err != nil {
			return err
		}
		same := bytes.Equal(prior, []byte(*input.Token.Value))
		clear(prior)
		if !same {
			record.ciphertext, err = sealIntegrationToken(a.integrationCredentials, workspace, id, *input.Token.Value)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		record, err = scanIntegrationConnection(tx.QueryRow(r.Context(), `UPDATE `+a.table("integration_connections")+`
			SET name=$3,channel=$4,credential_ciphertext=$5,enabled=$6,revision=revision+1,updated_at=$7
			WHERE workspace_id=$1 AND id=$2 RETURNING `+integrationConnectionColumns,
			workspace, id, record.Name, record.Channel, record.ciphertext, record.Enabled, a.config.Now().UTC()))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) getIntegrationConnection(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanIntegrationConnection(a.pool.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": record.IntegrationConnection})
	return nil
}

func (a *Application) listIntegrationConnections(w http.ResponseWriter, r *http.Request, workspace string) error {
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
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("integration_connections")+`
		WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+integrationConnectionColumns+` FROM `+a.table("integration_connections")+`
		WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []IntegrationConnection{}
	for rows.Next() {
		record, scanErr := scanIntegrationConnection(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.IntegrationConnection)
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
