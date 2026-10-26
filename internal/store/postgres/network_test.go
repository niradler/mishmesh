package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldSerializeOpposingRequirementUpdates(t *testing.T) {
	sharedDSN := isolatedSchemaDSN(t)
	first, second := openStoreOn(t, sharedDSN, PoolConfig{}), openStoreOn(t, sharedDSN, PoolConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	orgID := store.NewID("org")
	if err := first.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		a := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: store.NewID("alpha"), Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		b := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: store.NewID("beta"), Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		for _, svc := range []*store.Service{a, b} {
			if err := first.CreateService(ctx, svc); err != nil {
				t.Fatal(err)
			}
		}
		a.Requires, b.Requires = []string{b.ID}, []string{a.ID}
		start, results := make(chan struct{}), make(chan error, 2)
		go func() { <-start; results <- first.UpdateService(ctx, a) }()
		go func() { <-start; results <- second.UpdateService(ctx, b) }()
		close(start)
		successes, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				successes++
			} else if errors.Is(err, store.ErrConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected concurrent error: %v", err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("opposing updates: %d successes, %d conflicts", successes, conflicts)
		}
		graph, err := first.ListServices(ctx, orgID)
		if err != nil {
			t.Fatal(err)
		}
		for _, svc := range graph {
			if err := store.ValidateRequirements(orgID, svc.ID, svc.Requires, graph); err != nil {
				t.Fatalf("persisted cycle: %v", err)
			}
		}
	}
}

func TestShouldRefreshRequirementGraphAfterWaitingWithRepeatableReadDefault(t *testing.T) {
	parsed, err := url.Parse(isolatedSchemaDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("default_transaction_isolation", "repeatable read")
	parsed.RawQuery = query.Encode()
	openWriter := func() (*Store, int) {
		writer, err := Open(parsed.String(), PoolConfig{MaxOpenConns: 1, MaxIdleConns: 1, QueryTimeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var isolation string
		if err := writer.db.QueryRowContext(ctx, `SHOW default_transaction_isolation`).Scan(&isolation); err != nil {
			t.Fatal(err)
		}
		if isolation != "repeatable read" {
			t.Fatalf("connection default isolation: %q", isolation)
		}
		var pid int
		if err := writer.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		t.Logf("writer default_transaction_isolation=%s", isolation)
		return writer, pid
	}
	first, firstPID := openWriter()
	second, secondPID := openWriter()
	observer := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	orgID := store.NewID("org")
	if err := first.CreateOrg(ctx, &store.Org{ID: orgID, Name: orgID, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	makeService := func(name string) *store.Service {
		service := &store.Service{ID: store.NewID("svc"), OrgID: orgID, Name: name, Driver: "custom", Config: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
		if err := first.CreateService(ctx, service); err != nil {
			t.Fatal(err)
		}
		return service
	}
	a, b, x, y := makeService("alpha"), makeService("beta"), makeService("xray"), makeService("yankee")
	x.Requires, y.Requires = []string{b.ID}, []string{a.ID}
	for _, service := range []*store.Service{x, y} {
		if err := first.UpdateService(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	transaction, err := first.beginNetworkWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	var isolation string
	if err := transaction.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		t.Fatal(err)
	}
	t.Logf("holder transaction_isolation=%s", isolation)
	a.Requires = []string{x.ID}
	if err := validateServiceRequirements(ctx, transaction, a, true); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE services SET updated_at = $1 WHERE id = $2 AND org_id = $3`, ns(now), a.ID, orgID); err != nil {
		t.Fatal(err)
	}
	if err := replaceServiceReferences(ctx, transaction, a); err != nil {
		t.Fatal(err)
	}
	b.Requires = []string{y.ID}
	result := make(chan error, 1)
	go func() { result <- second.UpdateService(ctx, b) }()
	barrierContext, barrierCancel := context.WithTimeout(ctx, 5*time.Second)
	defer barrierCancel()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var blocked bool
		err := observer.db.QueryRowContext(barrierContext, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND classid = 0 AND objid = $2 AND NOT granted) AND $3 = ANY(pg_blocking_pids($1))`, secondPID, networkWriteLockKey, firstPID).Scan(&blocked)
		if err != nil {
			t.Fatalf("observe advisory-lock waiter: %v", err)
		}
		if blocked {
			t.Log("confirmed waiter blocked on holder's network advisory lock before holder commit")
			break
		}
		select {
		case err := <-result:
			t.Fatalf("waiter returned before holder commit: %v", err)
		case <-barrierContext.Done():
			t.Fatal("waiter did not block on the holder's advisory lock")
		case <-poll.C:
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, store.ErrConflict) {
			graph, graphErr := observer.ListServices(ctx, orgID)
			if graphErr == nil {
				graphErr = store.ValidateRequirements(orgID, a.ID, a.Requires, graph)
			}
			t.Fatalf("waiter must reject refreshed cycle, got %v; persisted graph validation: %v", err, graphErr)
		}
	case <-ctx.Done():
		t.Fatal("waiter did not finish after holder commit")
	}
	graph, err := observer.ListServices(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range graph {
		if err := store.ValidateRequirements(orgID, service.ID, service.Requires, graph); err != nil {
			t.Fatalf("persisted cycle: %v", err)
		}
		if service.ID == a.ID && !reflect.DeepEqual(service.Requires, []string{x.ID}) {
			t.Fatalf("holder update lost: %+v", service)
		}
		if service.ID == b.ID && len(service.Requires) != 0 {
			t.Fatalf("rejected waiter update persisted: %+v", service)
		}
	}
}
