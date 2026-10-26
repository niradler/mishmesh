package agent

import (
	"context"
	"fmt"
)

type rpcTable[T any] struct {
	waiting map[uint64]chan T
}

func newRPCTable[T any]() rpcTable[T] {
	return rpcTable[T]{waiting: map[uint64]chan T{}}
}

func (t *rpcTable[T]) register(id uint64) chan T {
	ch := make(chan T, 1)
	t.waiting[id] = ch
	return ch
}

func (t *rpcTable[T]) complete(id uint64, value T) {
	ch, ok := t.waiting[id]
	if !ok {
		return
	}
	delete(t.waiting, id)
	ch <- value
}

func (t *rpcTable[T]) closeAll() {
	for id, ch := range t.waiting {
		close(ch)
		delete(t.waiting, id)
	}
}

func rpcDeliver[T any](d *remoteDecider, gen uint64, table *rpcTable[T], id uint64, value T) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	table.complete(id, value)
}

func rpcForget[T any](d *remoteDecider, gen uint64, table *rpcTable[T], id uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen == gen {
		delete(table.waiting, id)
	}
}

func rpcAwait[T any](ctx context.Context, d *remoteDecider, gen uint64, table *rpcTable[T], id uint64, ch <-chan T, what string) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	select {
	case value, ok := <-ch:
		if !ok {
			var zero T
			return zero, errDecisionLost
		}
		return value, nil
	case <-ctx.Done():
		rpcForget(d, gen, table, id)
		var zero T
		return zero, fmt.Errorf("%s: %w", what, ctx.Err())
	}
}
