package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *recoveryRuntime) requireSourceDatabase(ctx context.Context) ([]int, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`,
		r.config.Database.Schema).Scan(&exists); err != nil || !exists {
		return nil, errors.New("selected ASPM schema is unavailable")
	}
	rows, err := r.pool.Query(ctx, `SELECT c.relname,c.relkind FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relkind IN ('r','p','v','m','S','f')
		ORDER BY c.relname`, r.config.Database.Schema)
	if err != nil {
		return nil, errors.New("selected ASPM schema catalog is unavailable")
	}
	var tables []string
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		if kind != "r" && kind != "p" {
			rows.Close()
			return nil, errors.New("selected ASPM schema contains unsupported relations")
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !reflect.DeepEqual(tables, recoveryTables) {
		return nil, errors.New("selected ASPM schema is not the exact V27 catalog")
	}
	var functions int
	if err = r.pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		WHERE n.nspname=$1`, r.config.Database.Schema).Scan(&functions); err != nil || functions != 0 {
		return nil, errors.New("selected ASPM schema contains unsupported functions")
	}
	ledgerRows, err := r.pool.Query(ctx, `SELECT version FROM `+
		pgx.Identifier{r.config.Database.Schema, "app_schema_versions"}.Sanitize()+` ORDER BY version`)
	if err != nil {
		return nil, errors.New("selected migration ledger is unavailable")
	}
	var ledger []int
	for ledgerRows.Next() {
		var version int
		if err = ledgerRows.Scan(&version); err != nil {
			ledgerRows.Close()
			return nil, err
		}
		ledger = append(ledger, version)
	}
	err = ledgerRows.Err()
	ledgerRows.Close()
	want := make([]int, 27)
	for index := range want {
		want[index] = index + 1
	}
	if err != nil || !reflect.DeepEqual(ledger, want) {
		return nil, errors.New("selected migration ledger is not exactly V27")
	}
	return ledger, nil
}

func (r *recoveryRuntime) requireQuiescent(ctx context.Context) error {
	if err := r.requireNoApplicationConnections(ctx); err != nil {
		return err
	}
	total, err := r.transitionalRows(ctx)
	if err != nil || total != 0 {
		return errors.New("ASPM durable work is not quiescent")
	}
	return nil
}

func (r *recoveryRuntime) requireNoApplicationConnections(ctx context.Context) error {
	var connections int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
		WHERE pid<>pg_backend_pid() AND application_name IN
		('aspm-core','aspm-core-application','aspm-ingestion','aspm-retention',
		 'aspm-reports','aspm-delivery','aspm-collection','aspm-assessment','aspm-verification',
		 'aspm-schema-migration')`).Scan(&connections)
	if err != nil || connections != 0 {
		return errors.New("ASPM application processes must be stopped for recovery")
	}
	return nil
}

func (r *recoveryRuntime) transitionalRows(ctx context.Context) (int64, error) {
	specs := []struct{ table, predicate string }{
		{"imports", `state='processing'`},
		{"report_snapshots", `state='processing'`},
		{"report_exports", `state='processing'`},
		{"finding_deliveries", `state='dispatching'`},
		{"source_collections", `state='collecting'`},
		{"assessment_jobs", `state='dispatching' OR (io_token IS NOT NULL AND io_released_at IS NULL)`},
		{"verification_jobs", `state='processing'`},
		{"finding_change_events", `state='processing'`},
		{"retention_runs", `state='processing'`},
		{"retention_run_items", `state='processing'`},
		{"archive_publications", `state='publishing'`},
	}
	var total int64
	for _, spec := range specs {
		var count int64
		query := `SELECT count(*) FROM ` +
			pgx.Identifier{r.config.Database.Schema, "app_" + spec.table}.Sanitize() +
			` WHERE ` + spec.predicate
		if err := r.pool.QueryRow(ctx, query).Scan(&count); err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func recoveryInventoryFor(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, schema string, restored bool) (recoveryInventory, error) {
	inventory := recoveryInventory{APIVersion: recoveryAPIVersion, Kind: "DatabaseInventory", SchemaVersion: 1}
	for _, name := range recoveryTables {
		table := pgx.Identifier{schema, name}.Sanitize()
		if recoveryEphemeral[name] {
			var count int64
			if err := query.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
				return recoveryInventory{}, err
			}
			source, restoredCount := count, int64(0)
			if restored {
				source = 0
				restoredCount = count
			}
			inventory.Tables = append(inventory.Tables, recoveryInventoryEntry{
				Name: name, Policy: "schema-only", SourceRowCount: &source, RestoredRowCount: &restoredCount,
			})
			continue
		}
		var count int64
		var canonical string
		if err := query.QueryRow(ctx, `SELECT count(*),
			COALESCE(jsonb_agg(to_jsonb(row_value) ORDER BY to_jsonb(row_value)::text),
			'[]'::jsonb)::text FROM `+table+` AS row_value`).Scan(&count, &canonical); err != nil {
			return recoveryInventory{}, err
		}
		digest := recoveryHash([]byte(canonical))
		inventory.Tables = append(inventory.Tables, recoveryInventoryEntry{
			Name: name, Policy: "exact", RowCount: &count, CopySHA256: &digest,
		})
	}
	return inventory, nil
}

func (r *recoveryRuntime) createDatabaseBackup(ctx context.Context, staging string) (
	recoveryInventory, time.Time, time.Time, int64, error) {
	started := time.Now().UTC()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("begin recovery database snapshot failed")
	}
	defer tx.Rollback(context.Background())
	var snapshot string
	if err = tx.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&snapshot); err != nil {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("export recovery database snapshot failed")
	}
	inventory, err := recoveryInventoryFor(ctx, tx, r.config.Database.Schema, false)
	if err != nil {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("read recovery database inventory failed")
	}
	dumpPath := filepath.Join(staging, "database.dump")
	args := []string{
		"--format=custom", "--no-owner", "--no-acl",
		"--schema=" + r.config.Database.Schema,
		"--snapshot=" + snapshot,
		"--file=" + dumpPath,
		"--exclude-table-data=" + r.config.Database.Schema + ".app_sessions",
		"--exclude-table-data=" + r.config.Database.Schema + ".app_oidc_flows",
		"--exclude-table-data=" + r.config.Database.Schema + ".app_auth_throttle",
		"--dbname=" + recoveryDatabaseArgument(r.config.Database.URL),
	}
	command := exec.CommandContext(ctx, r.config.Tools.PGDump, args...)
	command.Env = recoveryToolEnvironment(r.config.Database.URL)
	var diagnostic bytes.Buffer
	command.Stdout, command.Stderr = io.Discard, &diagnostic
	if err = command.Run(); err != nil {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("PostgreSQL recovery dump failed")
	}
	if err = tx.Commit(ctx); err != nil {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("complete recovery database snapshot failed")
	}
	completed := time.Now().UTC()
	info, err := os.Stat(dumpPath)
	if err != nil || info.Size() < 1 || info.Size() > r.config.Limits.MaxDatabaseBytes {
		return recoveryInventory{}, time.Time{}, time.Time{}, 0, errors.New("recovery database dump exceeds its bound")
	}
	return inventory, started, completed, info.Size(), nil
}

func (r *recoveryRuntime) restoreDatabase(ctx context.Context, dumpPath string) error {
	if _, err := r.pool.Exec(ctx,
		"CREATE SCHEMA "+pgx.Identifier{r.config.Database.Schema}.Sanitize()); err != nil {
		return errors.New("create exact recovery schema failed")
	}
	command := exec.CommandContext(ctx, r.config.Tools.PGRestore,
		"--exit-on-error", "--no-owner", "--no-acl",
		"--schema="+r.config.Database.Schema,
		"--dbname="+recoveryDatabaseArgument(r.config.Database.URL),
		dumpPath)
	command.Env = recoveryToolEnvironment(r.config.Database.URL)
	var diagnostic bytes.Buffer
	command.Stdout, command.Stderr = io.Discard, &diagnostic
	if err := command.Run(); err != nil {
		return errors.New("PostgreSQL recovery restore failed")
	}
	return nil
}

func (r *recoveryRuntime) schemaExists(ctx context.Context) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`,
		r.config.Database.Schema).Scan(&exists)
	return exists, err
}

func recoveryInventoryEqual(expected, actual recoveryInventory) bool {
	if expected.APIVersion != actual.APIVersion || expected.Kind != actual.Kind ||
		expected.SchemaVersion != actual.SchemaVersion || len(expected.Tables) != len(actual.Tables) {
		return false
	}
	for index := range expected.Tables {
		left, right := expected.Tables[index], actual.Tables[index]
		if left.Name != right.Name || left.Policy != right.Policy {
			return false
		}
		if left.Policy == "exact" {
			if left.RowCount == nil || right.RowCount == nil || *left.RowCount != *right.RowCount ||
				left.CopySHA256 == nil || right.CopySHA256 == nil || *left.CopySHA256 != *right.CopySHA256 {
				return false
			}
		} else if right.RestoredRowCount == nil || *right.RestoredRowCount != 0 {
			return false
		}
	}
	return true
}

func canonicalInventoryBytes(inventory recoveryInventory) ([]byte, error) {
	sort.Slice(inventory.Tables, func(i, j int) bool { return inventory.Tables[i].Name < inventory.Tables[j].Name })
	data, err := json.Marshal(inventory)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func parseRecoveryInventory(data []byte) (recoveryInventory, error) {
	var inventory recoveryInventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&inventory) != nil {
		return recoveryInventory{}, errors.New("recovery database inventory is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return recoveryInventory{}, errors.New("recovery database inventory contains trailing JSON")
	}
	if inventory.APIVersion != recoveryAPIVersion || inventory.Kind != "DatabaseInventory" ||
		inventory.SchemaVersion != 1 || len(inventory.Tables) != len(recoveryTables) {
		return recoveryInventory{}, errors.New("recovery database inventory identity is invalid")
	}
	names := make([]string, len(inventory.Tables))
	for index, item := range inventory.Tables {
		names[index] = item.Name
		switch item.Policy {
		case "exact":
			if item.RowCount == nil || *item.RowCount < 0 ||
				item.CopySHA256 == nil || !recoveryDigestPattern.MatchString(*item.CopySHA256) ||
				item.SourceRowCount != nil || item.RestoredRowCount != nil {
				return recoveryInventory{}, errors.New("recovery database inventory exact policy is invalid")
			}
		case "schema-only":
			if item.SourceRowCount == nil || *item.SourceRowCount < 0 ||
				item.RestoredRowCount == nil || *item.RestoredRowCount != 0 ||
				item.RowCount != nil || item.CopySHA256 != nil ||
				!recoveryEphemeral[item.Name] {
				return recoveryInventory{}, errors.New("recovery database inventory schema-only policy is invalid")
			}
		default:
			return recoveryInventory{}, errors.New("recovery database inventory policy is invalid")
		}
		if recoveryEphemeral[item.Name] != (item.Policy == "schema-only") {
			return recoveryInventory{}, errors.New("recovery database inventory policy does not match the table")
		}
	}
	if !reflect.DeepEqual(names, recoveryTables) {
		return recoveryInventory{}, errors.New("recovery database inventory table set is invalid")
	}
	return inventory, nil
}
