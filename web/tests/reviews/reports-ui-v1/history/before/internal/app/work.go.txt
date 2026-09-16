package app

import (
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const workColumns = `f.id,f.title,asset.name,f.severity,owner.name,f.workflow_state,f.source_scan_at,f.collected_at,f.imported_at`
const workFilter = `f.workspace_id=$1 AND ($2='' OR strpos(lower(f.title),lower($2))>0
	OR strpos(lower(asset.name),lower($2))>0 OR strpos(lower(COALESCE(owner.name,'')),lower($2))>0)`

func (a *Application) workFrom() string {
	return ` FROM ` + a.table("findings") + ` f JOIN ` + a.table("assets") + ` asset
		ON asset.workspace_id=f.workspace_id AND asset.id=f.asset_id
		LEFT JOIN ` + a.table("users") + ` owner ON owner.id=f.owner_id`
}

func workDest(v *WorkItem) []any {
	return []any{&v.ID, &v.Title, &v.AssetName, &v.Severity, &v.OwnerName,
		&v.WorkflowState, &v.SourceScanAt, &v.CollectedAt, &v.ImportedAt}
}

func scanWork(row pgx.Row) (WorkItem, error) {
	var v WorkItem
	err := row.Scan(workDest(&v)...)
	return v, err
}

func queryText(r *http.Request) (string, error) {
	q := r.URL.Query().Get("q")
	if len(q) > 512 || strings.ContainsRune(q, 0) {
		return "", errInvalid
	}
	return q, nil
}

type workQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (a *Application) workPage(ctx context.Context, db workQuerier, workspace, q, cursor string, limit int) ([]WorkItem, *string, error) {
	rows, err := db.Query(ctx, `SELECT `+workColumns+a.workFrom()+` WHERE `+workFilter+
		` AND f.id>$3 ORDER BY f.id LIMIT $4`, workspace, q, cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []WorkItem{}
	for rows.Next() {
		item, err := scanWork(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	return items, next, nil
}

func (a *Application) listWork(w http.ResponseWriter, r *http.Request, workspace string) error {
	q, err := queryText(r)
	if err != nil {
		return err
	}
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int
	if err = tx.QueryRow(r.Context(), `SELECT count(*)`+a.workFrom()+` WHERE `+workFilter, workspace, q).Scan(&total); err != nil {
		return err
	}
	items, next, err := a.workPage(r.Context(), tx, workspace, q, cursor, limit)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"dataOrigin": "live", "items": items, "total": total, "nextCursor": next})
	return nil
}

func csvTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func csvText(text string) string {
	trimmed := strings.TrimLeft(text, " \r\n\t")
	if strings.HasPrefix(text, "\t") || strings.HasPrefix(text, "\r") ||
		(trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
		return "'" + text
	}
	return text
}

func (a *Application) exportWork(w http.ResponseWriter, r *http.Request, workspace string) error {
	if r.URL.Query().Get("format") != "csv" {
		return errUnsupported
	}
	q, err := queryText(r)
	if err != nil {
		return err
	}
	items, next, err := a.workPage(r.Context(), a.pool, workspace, q, "", 500)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="work.csv"`)
	w.Header().Set("Trailer", "X-ASPM-Export-Status")
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"id", "title", "assetName", "severity", "ownerName", "workflowState", "sourceScanAt", "collectedAt", "importedAt"}); err != nil {
		return nil
	}
	for {
		for _, item := range items {
			owner := ""
			if item.OwnerName != nil {
				owner = *item.OwnerName
			}
			if err := writer.Write([]string{item.ID, csvText(item.Title), csvText(item.AssetName), item.Severity,
				csvText(owner), item.WorkflowState, csvTime(item.SourceScanAt), csvTime(&item.CollectedAt), csvTime(&item.ImportedAt)}); err != nil {
				w.Header().Set("X-ASPM-Export-Status", "failed")
				return nil
			}
		}
		writer.Flush()
		if writer.Error() != nil {
			w.Header().Set("X-ASPM-Export-Status", "failed")
			return nil
		}
		if next == nil {
			w.Header().Set("X-ASPM-Export-Status", "complete")
			return nil
		}
		items, next, err = a.workPage(r.Context(), a.pool, workspace, q, *next, 500)
		if err != nil {
			w.Header().Set("X-ASPM-Export-Status", "failed")
			a.log.Print("export interrupted requestId=" + w.Header().Get("X-Request-ID"))
			return nil
		}
	}
}
