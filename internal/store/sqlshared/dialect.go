package sqlshared

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

type RowScanner = interface{ Scan(...any) error }

type Dialect struct {
	Placeholder func(n int) string
	LockSuffix  string

	Wrap      func(op string, err error) error
	ScanErr   func(op string, err error) error
	UniqueErr func(op string, err error) error

	ConnectionCols string
	EndpointCols   string
	ServiceCols    string

	ScanConnection func(RowScanner) (*store.Connection, error)
	ScanEndpoint   func(scan func(...any) error) (*store.Endpoint, error)
	ScanService    func(RowScanner) (*store.Service, error)

	CreateServiceTx      func(ctx context.Context, tx *sql.Tx, svc *store.Service) error
	UpdateServiceTx      func(ctx context.Context, tx *sql.Tx, svc *store.Service) error
	DeleteServiceTx      func(ctx context.Context, tx *sql.Tx, id string) error
	CheckServiceDeleteTx func(ctx context.Context, tx *sql.Tx, id string) error
}

func QuestionMark(int) string { return "?" }

func Numbered(n int) string { return fmt.Sprintf("$%d", n) }

func (d Dialect) rebind(query string) string {
	if d.Placeholder(1) == "?" {
		return query
	}
	var out strings.Builder
	n := 0
	for _, char := range query {
		if char == '?' {
			n++
			out.WriteString(d.Placeholder(n))
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}

func (d Dialect) inList(first, count int) string {
	marks := make([]string, count)
	for i := range marks {
		marks[i] = d.Placeholder(first + i)
	}
	return strings.Join(marks, ", ")
}
