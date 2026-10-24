package postgres

import (
	"context"
	"database/sql"
)

func HasEncryptedConnections(ctx context.Context, dsn string, pool PoolConfig) (bool, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return false, err
	}
	defer db.Close()
	pool.apply(db)
	if err := validateSchema(ctx, db); err != nil {
		return false, err
	}
	for _, probe := range []struct{ table, query string }{
		{"connections", `SELECT EXISTS(SELECT 1 FROM connections WHERE octet_length(secrets_enc)>0)`},
		{"org_secrets", `SELECT EXISTS(SELECT 1 FROM org_secrets)`},
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`, probe.table).Scan(&exists); err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var encrypted bool
		if err := db.QueryRowContext(ctx, probe.query).Scan(&encrypted); err != nil {
			return false, err
		}
		if encrypted {
			return true, nil
		}
	}
	return false, nil
}
