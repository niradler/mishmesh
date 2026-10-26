package ingress

import (
	"context"
	"sync"
)

type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	held chan struct{}
	refs int
}

func (l *keyedLocks) acquire(ctx context.Context, key string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*keyedLock{}
	}
	lock := l.locks[key]
	if lock == nil {
		lock = &keyedLock{held: make(chan struct{}, 1)}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()
	leave := func() {
		l.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	select {
	case lock.held <- struct{}{}:
		return func() {
			<-lock.held
			leave()
		}, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

func (l *keyedLocks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}
