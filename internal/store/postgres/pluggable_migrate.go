package postgres

import (
	"context"
	"database/sql"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

func migratePluggable(ctx context.Context, tx *sql.Tx, box *secrets.Box) error {
	return store.MigrateLegacyRecords(ctx, tx, box, true)
}
