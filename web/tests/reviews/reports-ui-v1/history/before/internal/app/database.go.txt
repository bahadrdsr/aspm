package app

import (
	"context"
	"errors"
	"io"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DatabaseConfig struct {
	DatabaseURL             string `json:"-"`
	Schema, ApplicationName string
	MaxConnections          int32
	Now                     func() time.Time `json:"-"`
	LogOutput               io.Writer        `json:"-"`
	QueryTracer             pgx.QueryTracer  `json:"-"`
}

type database struct {
	pool      *pgxpool.Pool
	schema    string
	now       func() time.Time
	log       *log.Logger
	closeOnce sync.Once
}

func ValidateDatabaseConfig(config DatabaseConfig) error {
	u, err := url.Parse(config.DatabaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Host == "" || u.Path == "" || u.Path == "/" || u.User == nil || u.User.Username() == "" ||
		!schemaName.MatchString(config.Schema) || config.ApplicationName == "" ||
		len(config.ApplicationName) > 63 || config.MaxConnections < 1 || config.MaxConnections > 100 {
		return errors.New("invalid explicit application database configuration")
	}
	if password, present := u.User.Password(); !present || password == "" {
		return errors.New("explicit application database credentials are required")
	}
	if _, err := pgxpool.ParseConfig(config.DatabaseURL); err != nil {
		return errors.New("invalid application database configuration")
	}
	return nil
}

func openDatabase(ctx context.Context, config DatabaseConfig) (*database, error) {
	if err := ValidateDatabaseConfig(config); err != nil {
		return nil, err
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.LogOutput == nil {
		config.LogOutput = io.Discard
	}
	pc, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return nil, errors.New("invalid application database configuration")
	}
	pc.MaxConns, pc.MinConns = config.MaxConnections, 0
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = config.ApplicationName
	pc.ConnConfig.Tracer = config.QueryTracer
	db := &database{schema: config.Schema, now: config.Now, log: log.New(config.LogOutput, "application ", log.LstdFlags)}
	db.pool, err = pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("open application database failed")
	}
	if err = db.migrate(ctx); err != nil {
		db.pool.Close()
		return nil, errors.New("apply application migrations failed")
	}
	return db, nil
}

func (db *database) table(name string) string {
	return pgx.Identifier{db.schema, "app_" + name}.Sanitize()
}

func (db *database) ping(ctx context.Context) error { return db.pool.Ping(ctx) }

func (db *database) close() error {
	db.closeOnce.Do(db.pool.Close)
	return nil
}
