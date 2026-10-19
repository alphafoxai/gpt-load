package affinity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/platform/utils"
)

const (
	defaultFlushInterval   = time.Second
	defaultBatchSize       = 200
	defaultQueueCapacity   = 16_384
	defaultCleanupInterval = 10 * time.Minute
)

// Binding is one persisted soft-affinity mapping. ExpiresAt is absolute; a
// restored binding keeps it, clamped to the live TTL.
type Binding struct {
	Key       Key
	Target    Target
	ExpiresAt time.Time
	UpdatedAt time.Time
}

// Store is the persistence seam for bindings. Implementations run only on the
// persister's background goroutine, never on the request path.
type Store interface {
	// LoadLive returns at most limit bindings whose expiry is after now, latest expiry first.
	LoadLive(ctx context.Context, now time.Time, limit int) ([]Binding, error)
	// Save inserts or replaces bindings by key; a later entry wins for a repeated key.
	Save(ctx context.Context, bindings []Binding) error
	// DeleteExpired removes bindings whose expiry is at or before now.
	DeleteExpired(ctx context.Context, now time.Time) error
	// Clear removes every binding.
	Clear(ctx context.Context) error
}

// Persister writes cache changes behind the request path and restores them at
// startup. Lookup and RecordSuccess only enqueue; a single background goroutine
// performs all store I/O in batches.
type Persister struct {
	cache *Cache
	store Store
	now   func() time.Time

	flushInterval   time.Duration
	batchSize       int
	queueCapacity   int
	cleanupInterval time.Duration

	mu           sync.Mutex
	queue        []Binding
	clearPending bool
	closed       bool
	started      bool
	wake         chan struct{}
	stop         chan struct{}
	stopOnce     sync.Once
	done         chan struct{}
	cancel       context.CancelFunc

	dropped     atomic.Uint64
	dropLogger  *utils.RateLimitedEventCounter
	errorLogger *utils.RateLimitedEventCounter
}

// NewPersister attaches write-behind persistence to cache. Restore must run
// before the cache serves requests; Start then launches the writer.
func NewPersister(cache *Cache, store Store) *Persister {
	persister := &Persister{
		cache:           cache,
		store:           store,
		now:             cache.now,
		flushInterval:   defaultFlushInterval,
		batchSize:       defaultBatchSize,
		queueCapacity:   defaultQueueCapacity,
		cleanupInterval: defaultCleanupInterval,
		wake:            make(chan struct{}, 1),
		stop:            make(chan struct{}),
		dropLogger:      utils.NewRateLimitedEventCounter(time.Minute, time.Now),
		errorLogger:     utils.NewRateLimitedEventCounter(time.Minute, time.Now),
	}
	cache.mu.Lock()
	cache.persister = persister
	cache.mu.Unlock()
	return persister
}

// Restore installs the live policy into an unused cache and loads the persisted
// bindings still live under it, most recently expiring first, up to capacity.
// It returns how many bindings are now in memory. Restored bindings are not
// rewritten; they already exist in the store.
func (persister *Persister) Restore(ctx context.Context, revision uint64, capacity int, ttl time.Duration) (int, error) {
	if revision == 0 || capacity <= 0 || ttl <= 0 {
		return 0, errors.New("affinity restore requires a live policy")
	}
	now := persister.now()
	rows, loadErr := persister.store.LoadLive(ctx, now, capacity)
	if loadErr != nil {
		loadErr = fmt.Errorf("load affinity bindings: %w", loadErr)
	}

	cache := persister.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.revision != 0 || len(cache.entries) != 0 {
		return 0, errors.New("affinity restore requires an unused cache")
	}
	// Installed directly rather than through Configure: the store already holds
	// these rows, so this must not trigger the clear that a real policy change does.
	cache.revision = revision
	cache.capacity = capacity
	cache.ttl = ttl
	restored := 0
	for _, row := range rows {
		if cache.restoreLocked(row, now) {
			restored++
		}
	}
	return restored, loadErr
}

// Start launches the background writer. It does nothing after Stop.
func (persister *Persister) Start() {
	persister.mu.Lock()
	defer persister.mu.Unlock()
	if persister.started || persister.closed {
		return
	}
	persister.started = true
	ctx, cancel := context.WithCancel(context.Background())
	persister.cancel = cancel
	persister.done = make(chan struct{})
	go persister.run(ctx, persister.done)
}

// Stop rejects later changes and flushes queued writes until ctx ends. Writes
// still pending at the deadline are counted as dropped and reported as an error.
func (persister *Persister) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	persister.mu.Lock()
	persister.closed = true
	started, done, cancel := persister.started, persister.done, persister.cancel
	persister.mu.Unlock()

	if started {
		persister.stopOnce.Do(func() { close(persister.stop) })
		select {
		case <-done:
		case <-ctx.Done():
		}
		cancel()
	} else {
		persister.flushAll(ctx, true)
	}

	persister.mu.Lock()
	pending, clearPending := len(persister.queue), persister.clearPending
	persister.queue, persister.clearPending = nil, false
	persister.mu.Unlock()
	if pending > 0 {
		persister.drop(pending)
	}
	if pending > 0 || clearPending {
		return fmt.Errorf("affinity bindings not flushed before shutdown: %d queued writes, clear pending: %t", pending, clearPending)
	}
	return nil
}

// Dropped reports how many binding writes were dropped instead of persisted:
// queue overflow, keys too long for the schema, a closed persister, or store failures.
func (persister *Persister) Dropped() uint64 {
	return persister.dropped.Load()
}

// record queues one cache change. It is called with cache.mu held, so queue
// order matches epoch and version order. It never waits on the store.
func (persister *Persister) record(binding Binding) {
	persister.mu.Lock()
	if persister.closed || len(persister.queue) >= persister.queueCapacity || len(binding.Key) > MaxKeyLength {
		persister.mu.Unlock()
		persister.drop(1)
		return
	}
	persister.queue = append(persister.queue, binding)
	full := len(persister.queue) >= persister.batchSize
	persister.mu.Unlock()
	if full {
		persister.signal()
	}
}

// clear discards queued writes and schedules deletion of every persisted
// binding. Called with cache.mu held when a real policy change clears the cache.
func (persister *Persister) clear() {
	persister.mu.Lock()
	if persister.closed {
		persister.mu.Unlock()
		return
	}
	persister.queue = nil
	persister.clearPending = true
	persister.mu.Unlock()
	persister.signal()
}

func (persister *Persister) signal() {
	select {
	case persister.wake <- struct{}{}:
	default:
	}
}

func (persister *Persister) drop(count int) {
	total := persister.dropped.Add(uint64(count))
	if _, logNow := persister.dropLogger.Observe(); logNow {
		logrus.WithField("event", "affinity.binding_persist_dropped").
			WithField("dropped_total", total).
			Warn("affinity binding writes were dropped; those sessions may lose affinity after a restart")
	}
}

func (persister *Persister) fail(operation string, err error, dropped int) {
	if dropped > 0 {
		persister.drop(dropped)
	}
	if _, logNow := persister.errorLogger.Observe(); logNow {
		logrus.WithError(err).
			WithField("event", "affinity.binding_persist_failed").
			WithField("operation", operation).
			Warn("affinity binding persistence failed")
	}
}

func (persister *Persister) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(persister.flushInterval)
	defer ticker.Stop()
	var nextCleanup time.Time
	for {
		force := false
		select {
		case <-ticker.C:
			// Ticks drain partial batches so a quiet cache still persists promptly.
			force = true
		case <-persister.wake:
		case <-persister.stop:
			persister.flushAll(ctx, true)
			return
		}
		persister.flushAll(ctx, force)
		if now := persister.now(); !now.Before(nextCleanup) {
			if err := persister.store.DeleteExpired(ctx, now); err != nil {
				persister.fail("cleanup", err, 0)
			}
			nextCleanup = now.Add(persister.cleanupInterval)
		}
	}
}

// flushAll writes queued changes until ctx ends or nothing eligible remains.
// Without force only full batches are written; a partial batch waits for the
// next tick or shutdown. A pending clear always runs before later changes.
func (persister *Persister) flushAll(ctx context.Context, force bool) {
	for ctx.Err() == nil {
		batch, clearStore := persister.take(force)
		if clearStore {
			if err := persister.store.Clear(ctx); err != nil {
				persister.fail("clear", err, 0)
			}
			continue
		}
		if len(batch) == 0 {
			return
		}
		if err := persister.store.Save(ctx, batch); err != nil {
			persister.fail("save", err, len(batch))
		}
	}
}

// take returns the next batch, or reports that the store must be cleared first.
func (persister *Persister) take(force bool) ([]Binding, bool) {
	persister.mu.Lock()
	defer persister.mu.Unlock()
	if persister.clearPending {
		persister.clearPending = false
		return nil, true
	}
	if len(persister.queue) == 0 || (!force && len(persister.queue) < persister.batchSize) {
		return nil, false
	}
	count := min(len(persister.queue), persister.batchSize)
	batch := append([]Binding(nil), persister.queue[:count]...)
	persister.queue = persister.queue[count:]
	if len(persister.queue) == 0 {
		persister.queue = nil
	}
	return batch, false
}

// journalLocked queues the current mapping for write-behind. Callers hold cache.mu.
func (cache *Cache) journalLocked(key Key, target Target, expiresAt, now time.Time) {
	if cache.persister == nil {
		return
	}
	cache.persister.record(Binding{Key: key, Target: target, ExpiresAt: expiresAt, UpdatedAt: now})
}

// restoreLocked appends one persisted binding at the cold end of the recency
// list, so rows must arrive latest expiry first. The expiry is clamped to the
// live TTL, and the entry gets a fresh version so compare-and-swap writes keep working.
func (cache *Cache) restoreLocked(binding Binding, now time.Time) bool {
	if !binding.Key.Valid() || !binding.Target.Valid() || len(cache.entries) >= cache.capacity {
		return false
	}
	if _, exists := cache.entries[binding.Key]; exists {
		return false
	}
	expiresAt := binding.ExpiresAt
	if limit := now.Add(cache.ttl); expiresAt.After(limit) {
		expiresAt = limit
	}
	if !expiresAt.After(now) {
		return false
	}
	cache.nextVersion++
	entry := &cacheEntry{
		key: binding.Key, target: binding.Target, version: cache.nextVersion,
		expiresAt: expiresAt,
	}
	cache.entries[binding.Key] = cache.recent.PushBack(entry)
	return true
}
