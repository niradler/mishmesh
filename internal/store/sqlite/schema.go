package sqlite

import (
	"context"
	"database/sql"

	"github.com/mishmesh/mishmesh/internal/store"
)

type schemaReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func validateSchema(ctx context.Context, reader schemaReader) error {
	rows, err := reader.QueryContext(ctx, `SELECT m.name,p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type='table'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tables := map[string]map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		if tables[table] == nil {
			tables[table] = map[string]bool{}
		}
		tables[table][column] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return store.ValidateSchema(tables)
}
