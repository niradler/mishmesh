package connections

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

var (
	ErrNotConnected = errors.New("connection is not loaded")
	ErrHubStopped   = errors.New("connection hub is not running")
	ErrSyncFailed   = errors.New("connection sync failed")
	ErrDrainPending = errors.New("previous connection generation is still draining")
)

type HubOptions struct {
	Registry     *Registry
	Deps         Deps
	SyncTimeout  time.Duration
	DrainTimeout time.Duration
}

type Hub struct {
	opts           HubOptions
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	started        bool
	stop           chan struct{}
	admission      chan struct{}
	runs           map[string]*instanceRun
	statuses       map[string]Status
	statusRecords  map[string]store.Connection
	reloadFailures map[string]failedReload
	epochs         map[string]uint64
	all            map[*instanceRun]bool
}

type failedReload struct {
	expected store.Connection
	status   Status
}

type instanceRun struct {
	id         string
	record     store.Connection
	instance   Instance
	redactor   *redactor
	ctx        context.Context
	cancel     context.CancelFunc
	requests   chan syncRequest
	trigger    chan struct{}
	runDone    chan struct{}
	syncDone   chan struct{}
	closeDone  chan struct{}
	done       chan struct{}
	retireOnce sync.Once
}

type syncRequest struct {
	result   chan syncResult
	expected *store.Connection
}
type syncResult struct {
	status Status
	err    error
}

func NewHub(opts HubOptions) *Hub {
	if opts.Registry == nil {
		opts.Registry = NewRegistry()
	}
	if opts.Deps.Log == nil {
		opts.Deps.Log = slog.Default()
	}
	if opts.SyncTimeout <= 0 {
		opts.SyncTimeout = 30 * time.Second
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 5 * time.Second
	}
	return &Hub{opts: opts, stop: make(chan struct{}), admission: make(chan struct{}, 1), runs: make(map[string]*instanceRun), statuses: make(map[string]Status), statusRecords: make(map[string]store.Connection), reloadFailures: make(map[string]failedReload), epochs: make(map[string]uint64), all: make(map[*instanceRun]bool)}
}

func (h *Hub) Start(ctx context.Context) error {
	if err := h.enter(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	if h.started || h.closed {
		h.mu.Unlock()
		h.leave()
		return errors.New("connection hub already started or closed")
	}
	h.started = true
	h.ctx, h.cancel = context.WithCancel(ctx)
	base := h.ctx
	h.mu.Unlock()
	if h.opts.Deps.Data == nil {
		h.leave()
		h.Close()
		return errors.New("connection store is required")
	}
	records, err := h.opts.Deps.Data.ListAllConnections(base)
	h.leave()
	if err != nil {
		h.Close()
		return errors.New("list connections failed")
	}
	for _, record := range records {
		if record == nil {
			continue
		}
		if err := h.Reload(base, record.ID); err != nil {
			if base.Err() != nil {
				return base.Err()
			}
			h.opts.Deps.Log.Error("connection not loaded", "connection_id", record.ID, "error", err)
		}
	}
	return base.Err()
}

func (h *Hub) enter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.stop:
		return ErrHubStopped
	case h.admission <- struct{}{}:
		return nil
	}
}

func (h *Hub) leave() { <-h.admission }

func (h *Hub) Reload(ctx context.Context, connectionID string) error {
	return h.reload(ctx, connectionID, nil)
}

func (h *Hub) ReloadIfCurrent(ctx context.Context, expected store.Connection) error {
	return h.reload(ctx, expected.ID, &expected)
}

func (h *Hub) reload(ctx context.Context, connectionID string, expected *store.Connection) error {
	h.mu.RLock()
	epoch := h.epochs[connectionID]
	h.mu.RUnlock()
	if err := h.enter(ctx); err != nil {
		if expected != nil {
			h.recordAdmissionFailure(*expected, epoch, err)
		}
		return err
	}
	defer h.leave()
	h.mu.RLock()
	base := h.ctx
	stopped := h.closed || base == nil || base.Err() != nil
	pending := false
	for run, retired := range h.all {
		if run.id == connectionID && retired {
			pending = true
		}
	}
	h.mu.RUnlock()
	if stopped {
		return ErrHubStopped
	}
	loadCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(base, cancel)
	defer stopCancel()
	defer cancel()
	record, err := h.opts.Deps.Data.GetConnection(loadCtx, connectionID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.replace(connectionID, nil, downStatus("load connection failed"))
		return errors.New("load connection failed")
	}
	if errors.Is(err, store.ErrNotFound) || record == nil {
		h.mu.Lock()
		delete(h.statusRecords, connectionID)
		h.mu.Unlock()
		old := h.replace(connectionID, nil, downStatus(ErrNotConnected.Error()))
		h.waitClosed(loadCtx, old)
		return loadCtx.Err()
	}
	if expected != nil && !store.SameConnectionInput(*record, *expected) {
		return store.ErrConflict
	}
	if pending {
		h.bindStatusRecord(*record)
		h.mu.Lock()
		h.statuses[connectionID] = downStatus("previous connection generation is still draining; retry sync or restart the server")
		h.mu.Unlock()
		return ErrDrainPending
	}
	if record.ID != connectionID || record.ID == "" || record.OrgID == "" {
		return h.failLoad(connectionID, "invalid connection identity")
	}
	old := h.replace(connectionID, nil, Status{Health: "unknown"})
	h.bindStatusRecord(*record)
	if err := h.waitRetired(loadCtx, old); err != nil {
		h.mu.Lock()
		h.statuses[connectionID] = downStatus("connection reload could not drain the previous generation; retry sync or restart the server")
		h.mu.Unlock()
		return err
	}
	if old != nil {
		fresh, loadError := h.opts.Deps.Data.GetConnection(loadCtx, connectionID)
		if errors.Is(loadError, store.ErrNotFound) || loadError == nil && fresh == nil {
			return h.failLoad(connectionID, ErrNotConnected.Error())
		}
		if loadError != nil {
			return h.failLoad(connectionID, "load connection failed")
		}
		if fresh.ID != record.ID || fresh.OrgID != record.OrgID || fresh.Type != record.Type {
			return h.failLoad(connectionID, "connection identity changed during reload")
		}
		record = fresh
	}
	if expected != nil && !store.SameConnectionInput(*record, *expected) {
		return store.ErrConflict
	}
	h.bindStatusRecord(*record)
	entry, ok := h.opts.Registry.lookup(record.Type)
	if !ok {
		return h.failLoad(connectionID, "unknown connection type")
	}
	cfg, err := openConfig(*record, h.opts.Deps.Box, h.opts.Deps.HostCommands)
	if err != nil {
		return h.failLoad(connectionID, err.Error())
	}
	redactor := newRedactor(cfg, record.SecretsEnc, entry.descriptor.Fields)
	if err := entry.provider.Validate(cloneConfig(cfg)); err != nil {
		return h.failLoad(connectionID, "invalid connection configuration")
	}
	if err := loadCtx.Err(); err != nil {
		return err
	}
	deps := h.opts.Deps
	deps.Log = slog.New(redactingHandler{base: deps.Log.Handler(), redactor: redactor})
	instance, err := entry.provider.Open(cloneConfig(cfg), deps)
	if err != nil || isNil(instance) {
		if !isNil(instance) {
			h.retireUnstarted(connectionID, instance, redactor)
		}
		return h.failLoad(connectionID, "open connection failed")
	}
	runCtx, runCancel := context.WithCancel(base)
	run := &instanceRun{id: connectionID, record: *record, instance: instance, redactor: redactor, ctx: runCtx, cancel: runCancel, requests: make(chan syncRequest), trigger: make(chan struct{}, 1), runDone: make(chan struct{}), syncDone: make(chan struct{}), closeDone: make(chan struct{}), done: make(chan struct{})}
	initial := redactor.status(instance.Status())
	h.mu.Lock()
	if h.closed || base.Err() != nil || loadCtx.Err() != nil {
		h.all[run] = true
		h.mu.Unlock()
		close(run.runDone)
		close(run.syncDone)
		h.retire(run)
		return ErrHubStopped
	}
	if failure, exists := h.reloadFailures[connectionID]; exists && store.SameConnectionInput(failure.expected, run.record) {
		delete(h.reloadFailures, connectionID)
	}
	h.runs[connectionID] = run
	h.statuses[connectionID] = initial
	h.all[run] = false
	h.mu.Unlock()
	go func() { defer close(run.runDone); instance.Run(runCtx) }()
	go h.syncLoop(run)
	return nil
}

func (h *Hub) failLoad(id, message string) error {
	old := h.replace(id, nil, downStatus(message))
	h.waitClosed(context.Background(), old)
	return errors.New(message)
}

func downStatus(message string) Status {
	return WithHostDiagnostic(Status{Health: "down"}, HostDiagnostic(message))
}

func (h *Hub) recordAdmissionFailure(expected store.Connection, epoch uint64, cause error) {
	if h.opts.Deps.Data == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.DrainTimeout)
	defer cancel()
	current, err := h.opts.Deps.Data.GetConnection(ctx, expected.ID)
	if err != nil || current == nil || !store.SameConnectionInput(*current, expected) {
		return
	}
	current, err = h.opts.Deps.Data.GetConnection(ctx, expected.ID)
	if err != nil || current == nil || !store.SameConnectionInput(*current, expected) {
		return
	}
	diagnostic := HostReloadUnavailable
	if errors.Is(cause, context.Canceled) {
		diagnostic = HostReloadCanceled
	} else if errors.Is(cause, context.DeadlineExceeded) {
		diagnostic = HostReloadTimedOut
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	run := h.runs[expected.ID]
	if h.closed || h.epochs[expected.ID] != epoch || run != nil && store.SameConnectionInput(run.record, expected) {
		return
	}
	expected.Config = append([]byte(nil), expected.Config...)
	expected.SecretsEnc = append([]byte(nil), expected.SecretsEnc...)
	expected.State = nil
	h.reloadFailures[expected.ID] = failedReload{expected: expected, status: WithHostDiagnostic(Status{Health: "down"}, diagnostic)}
}

func (h *Hub) bindStatusRecord(record store.Connection) {
	record.Config = append([]byte(nil), record.Config...)
	record.SecretsEnc = append([]byte(nil), record.SecretsEnc...)
	record.State = nil
	h.mu.Lock()
	if !h.closed {
		h.statusRecords[record.ID] = record
	}
	h.mu.Unlock()
}

func (h *Hub) replace(id string, next *instanceRun, status Status) *instanceRun {
	h.mu.Lock()
	h.epochs[id]++
	delete(h.reloadFailures, id)
	old := h.runs[id]
	delete(h.runs, id)
	if next != nil {
		h.runs[id] = next
	}
	if !h.closed {
		h.statuses[id] = status
	}
	if old != nil {
		h.all[old] = true
	}
	h.mu.Unlock()
	if old != nil {
		h.retire(old)
	}
	return old
}

func (h *Hub) retireUnstarted(id string, instance Instance, redactor *redactor) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &instanceRun{id: id, instance: instance, redactor: redactor, ctx: ctx, cancel: cancel, runDone: make(chan struct{}), syncDone: make(chan struct{}), closeDone: make(chan struct{}), done: make(chan struct{})}
	close(run.runDone)
	close(run.syncDone)
	h.mu.Lock()
	h.all[run] = true
	h.mu.Unlock()
	h.retire(run)
	h.waitClosed(context.Background(), run)
}

func (h *Hub) retire(run *instanceRun) {
	run.retireOnce.Do(func() {
		run.cancel()
		go func() {
			deadline := time.NewTimer(h.opts.DrainTimeout)
			defer deadline.Stop()
			for _, done := range []<-chan struct{}{run.runDone, run.syncDone} {
				select {
				case <-done:
				case <-deadline.C:
					goto drained
				}
			}
		drained:
			run.instance.Close()
			close(run.closeDone)
			<-run.runDone
			<-run.syncDone
			h.mu.Lock()
			delete(h.all, run)
			close(run.done)
			h.mu.Unlock()
		}()
	})
}

func (h *Hub) waitClosed(ctx context.Context, run *instanceRun) {
	if run == nil {
		return
	}
	timer := time.NewTimer(2 * h.opts.DrainTimeout)
	defer timer.Stop()
	select {
	case <-run.closeDone:
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (h *Hub) waitRetired(ctx context.Context, run *instanceRun) error {
	if run == nil {
		return ctx.Err()
	}
	timer := time.NewTimer(2 * h.opts.DrainTimeout)
	defer timer.Stop()
	select {
	case <-run.done:
		return ctx.Err()
	case <-timer.C:
		return ErrDrainPending
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hub) syncLoop(run *instanceRun) {
	defer close(run.syncDone)
	for {
		if run.ctx.Err() != nil {
			return
		}
		var request syncRequest
		select {
		case <-run.ctx.Done():
			return
		case request = <-run.requests:
		case <-run.trigger:
		}
		if run.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(run.ctx, h.opts.SyncTimeout)
		if request.expected != nil {
			current, err := h.opts.Deps.Data.GetConnection(ctx, run.id)
			if err != nil || current == nil || !store.SameConnectionInput(*current, *request.expected) || !store.SameConnectionInput(run.record, *request.expected) {
				cancel()
				request.result <- syncResult{status: downStatus("connection changed; reload before retrying"), err: store.ErrConflict}
				continue
			}
		}
		status, err := run.instance.Sync(ctx)
		contextError := ctx.Err()
		cancel()
		status = run.redactor.status(status)
		if contextError != nil {
			err = contextError
		} else if err != nil {
			err = ErrSyncFailed
		}
		if err != nil {
			status.Health = "down"
			status = WithHostDiagnostic(status, HostSyncFailed)
		}
		status.LastSync = time.Now().UTC()
		h.mu.Lock()
		current := h.runs[run.id] == run && !h.closed && run.ctx.Err() == nil
		if current {
			h.statuses[run.id] = status
		}
		h.mu.Unlock()
		if !current {
			err = ErrNotConnected
			status = downStatus(err.Error())
		}
		if request.result != nil {
			request.result <- syncResult{status: status, err: err}
		}
	}
}

func (h *Hub) Sync(ctx context.Context, connectionID string) (Status, error) {
	return h.sync(ctx, connectionID, nil)
}

func (h *Hub) SyncIfCurrent(ctx context.Context, expected store.Connection) (Status, error) {
	return h.sync(ctx, expected.ID, &expected)
}

func (h *Hub) sync(ctx context.Context, connectionID string, expected *store.Connection) (Status, error) {
	if err := ctx.Err(); err != nil {
		return downStatus("connection sync canceled"), err
	}
	h.mu.RLock()
	run := h.runs[connectionID]
	h.mu.RUnlock()
	if run == nil {
		return h.Status(connectionID), ErrNotConnected
	}
	waitCtx, cancel := context.WithTimeout(ctx, h.opts.SyncTimeout)
	defer cancel()
	request := syncRequest{result: make(chan syncResult, 1), expected: expected}
	select {
	case <-waitCtx.Done():
		return downStatus("connection sync canceled"), waitCtx.Err()
	case <-run.ctx.Done():
		return downStatus(ErrNotConnected.Error()), ErrNotConnected
	case run.requests <- request:
	}
	select {
	case <-waitCtx.Done():
		return downStatus("connection sync canceled"), waitCtx.Err()
	case <-run.ctx.Done():
		return downStatus(ErrNotConnected.Error()), ErrNotConnected
	case result := <-request.result:
		return cloneSafeStatus(run.redactor, result.status), result.err
	}
}

func (h *Hub) Status(connectionID string) Status {
	h.mu.RLock()
	failure, failedReload := h.reloadFailures[connectionID]
	run := h.runs[connectionID]
	cached, exists := h.statuses[connectionID]
	h.mu.RUnlock()
	if failedReload {
		return cloneSafeStatus(&redactor{}, failure.status)
	}
	if run == nil {
		if exists {
			return cloneSafeStatus(&redactor{}, cached)
		}
		return downStatus(ErrNotConnected.Error())
	}
	status := run.redactor.status(run.instance.Status())
	h.mu.RLock()
	current := h.runs[connectionID] == run && !h.closed && run.ctx.Err() == nil
	cached = h.statuses[connectionID]
	h.mu.RUnlock()
	if !current {
		return downStatus(ErrNotConnected.Error())
	}
	if !cached.LastSync.IsZero() && !status.LastSync.After(cached.LastSync) {
		status.LastSync = cached.LastSync
		if cached.LastError != "" {
			status.LastError = cached.LastError
			status.hostDiagnostic = cached.hostDiagnostic
			status.Health = cached.Health
		}
	}
	return status
}

func (h *Hub) StatusIfCurrent(expected store.Connection) Status {
	h.mu.RLock()
	run := h.runs[expected.ID]
	failure, failed := h.reloadFailures[expected.ID]
	statusRecord, recorded := h.statusRecords[expected.ID]
	cached := h.statuses[expected.ID]
	loaded := run != nil && store.SameConnectionInput(run.record, expected)
	h.mu.RUnlock()
	if !loaded {
		if failed && store.SameConnectionInput(failure.expected, expected) {
			return cloneSafeStatus(&redactor{}, failure.status)
		}
		if recorded && store.SameConnectionInput(statusRecord, expected) && cached.hostDiagnostic != "" {
			return cloneSafeStatus(&redactor{}, cached)
		}
		return WithHostDiagnostic(Status{Health: "down"}, HostConfigurationPending)
	}
	status := h.Status(expected.ID)
	h.mu.RLock()
	run = h.runs[expected.ID]
	loaded = run != nil && store.SameConnectionInput(run.record, expected)
	h.mu.RUnlock()
	if !loaded {
		return WithHostDiagnostic(Status{Health: "down"}, HostConfigurationPending)
	}
	return status
}

func cloneSafeStatus(redactor *redactor, status Status) Status {
	return redactor.hostStatus(status)
}

func (h *Hub) Trigger(connectionID string) {
	h.mu.RLock()
	run := h.runs[connectionID]
	h.mu.RUnlock()
	if run == nil || run.ctx.Err() != nil {
		return
	}
	select {
	case run.trigger <- struct{}{}:
	default:
	}
}

func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.stop)
	cancel := h.cancel
	runs := make([]*instanceRun, 0, len(h.all))
	for run := range h.all {
		h.all[run] = true
		runs = append(runs, run)
	}
	h.runs = make(map[string]*instanceRun)
	h.statuses = make(map[string]Status)
	h.statusRecords = make(map[string]store.Connection)
	h.reloadFailures = make(map[string]failedReload)
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, run := range runs {
		h.retire(run)
	}
	deadline := time.NewTimer(2 * h.opts.DrainTimeout)
	defer deadline.Stop()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-deadline.C:
			return
		}
	}
}
