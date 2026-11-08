package platform

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func OpenDB(ctx context.Context, dsn, service string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 12
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = service
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "10000"
	var db *pgxpool.Pool
	for i := 0; i < 60; i++ {
		db, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			err = db.Ping(ctx)
			if err == nil {
				break
			}
			db.Close()
		}
		if !Pause(ctx, time.Second) {
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if err = Migrate(ctx, db, service); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrations are immutable and serialized across replicas by a transaction advisory lock.
func Migrate(ctx context.Context, db *pgxpool.Pool, service string) error {
	files, err := fs.Glob(migrations, "migrations/"+service+"_[0-9][0-9][0-9].sql")
	if err != nil {
		return err
	}
	sort.Strings(files)
	files = append([]string{"migrations/" + service + ".sql"}, files...)
	for _, file := range files {
		version := service + "/001"
		if strings.Contains(file, service+"_") {
			version = service + "/" + strings.TrimSuffix(strings.TrimPrefix(file, "migrations/"+service+"_"), ".sql")
		}
		sql, e := migrations.ReadFile(file)
		if e != nil {
			return e
		}
		if e = applyMigration(ctx, db, version, sql); e != nil {
			return e
		}
	}
	return nil
}
func applyMigration(ctx context.Context, db *pgxpool.Pool, version string, sql []byte) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(20261107)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(sql))
	var previous string
	err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, version).Scan(&previous)
	if err == nil {
		if previous != checksum {
			return fmt.Errorf("migration checksum mismatch: %s", version)
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err = tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("migration %s: %w", version, err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
