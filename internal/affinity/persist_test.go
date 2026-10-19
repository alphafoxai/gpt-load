package affinity

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (clock *fakeClock) Now() time.Time          { return clock.now }
func (clock *fakeClock) Advance(d time.Duration) { clock.now = clock.now.Add(d) }

// memoryStore is an in-memory Store. When gate is set, Save blocks until gate
// is closed, which lets tests prove the request path never waits on the store.
type memoryStore struct {
	mu     sync.Mutex
	rows   map[Key]Binding
	saves  []int
	clears int
	gate   chan struct{}
}

func newMemoryStore() *memoryStore {
	return &memoryStore{rows: make(map[Key]Binding)}
}

func (store *memoryStore) LoadLive(_ context.Context, now time.Time, limit int) ([]Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var live []Binding
	for _, row := range store.rows {
		if row.ExpiresAt.After(now) {
			live = append(live, row)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].ExpiresAt.Equal(live[j].ExpiresAt) {
			return live[i].Key < live[j].Key
		}
		return live[i].ExpiresAt.After(live[j].ExpiresAt)
	})
	if len(live) > limit {
		live = live[:limit]
	}
	return live, nil
}

func (store *memoryStore) Save(ctx context.Context, bindings []Binding) error {
	if store.gate != nil {
		select {
		case <-store.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.saves = append(store.saves, len(bindings))
	for _, binding := range bindings {
		store.rows[binding.Key] = binding
	}
	return nil
}

func (store *memoryStore) DeleteExpired(_ context.Context, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for key, row := range store.rows {
		if !row.ExpiresAt.After(now) {
			delete(store.rows, key)
		}
	}
	return nil
}

func (store *memoryStore) Clear(context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.rows = make(map[Key]Binding)
	store.clears++
	return nil
}

func (store *memoryStore) count() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.rows)
}

func (store *memoryStore) clearCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.clears
}

func (store *memoryStore) savedBatches() []int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]int(nil), store.saves...)
}

func persistTestTarget(credentialID uint) Target {
	return Target{GroupID: 1, CredentialID: credentialID, IdentityGeneration: 100 + uint64(credentialID)}
}

func seedBinding(store *memoryStore, key Key, target Target, expiresAt time.Time) {
	store.rows[key] = Binding{Key: key, Target: target, ExpiresAt: expiresAt, UpdatedAt: expiresAt.Add(-time.Hour)}
}

func TestPersistedBindingSurvivesRestartWithSameTarget(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	key := Key("conversation")
	target := persistTestTarget(11)

	first := newCache(10, time.Hour, clock.Now)
	firstPersister := NewPersister(first, store)
	if !first.RecordSuccess(key, first.Lookup(key), target) {
		t.Fatal("RecordSuccess() = false, want insert")
	}
	if err := firstPersister.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	second := newCache(10, time.Hour, clock.Now)
	secondPersister := NewPersister(second, store)
	restored, err := secondPersister.Restore(context.Background(), 7, 10, time.Hour)
	if err != nil || restored != 1 {
		t.Fatalf("Restore() = %d, %v; want 1, nil", restored, err)
	}
	got := second.Lookup(key)
	if !got.Found() || got.Target != target {
		t.Fatalf("Lookup() after restart = %#v, want target %#v", got, target)
	}
	// Restored entries carry fresh versions, so a compare-and-swap from the
	// observation taken after restore still updates the mapping.
	fallback := persistTestTarget(12)
	if !second.RecordSuccess(key, got, fallback) {
		t.Fatal("RecordSuccess() on a restored binding = false, want compare-and-swap success")
	}
	if updated := second.Lookup(key); updated.Target != fallback {
		t.Fatalf("Lookup() after fallback = %#v, want %#v", updated, fallback)
	}
}

func TestRestoreClampsStoredExpiryToLiveTTL(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	now := clock.Now()
	seedBinding(store, "far", persistTestTarget(1), now.Add(3*time.Hour))
	seedBinding(store, "near", persistTestTarget(2), now.Add(10*time.Minute))

	cache := newCache(10, time.Hour, clock.Now)
	restored, err := NewPersister(cache, store).Restore(context.Background(), 1, 10, time.Hour)
	if err != nil || restored != 2 {
		t.Fatalf("Restore() = %d, %v; want 2, nil", restored, err)
	}

	clock.Advance(10 * time.Minute)
	if got := cache.Lookup("near"); got.Found() {
		t.Fatal("binding stored to expire in 10 minutes survived its stored expiry")
	}
	clock.Advance(49 * time.Minute)
	if got := cache.Lookup("far"); !got.Found() {
		t.Fatal("binding inside the live TTL was dropped before the clamped expiry")
	}
	clock.Advance(2 * time.Minute)
	if got := cache.Lookup("far"); got.Found() {
		t.Fatal("binding stored for 3h outlived the live 1h TTL")
	}
}

func TestRestoreSkipsExpiredRowsAndKeepsLatestExpiryWithinCapacity(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	now := clock.Now()
	seedBinding(store, "expired", persistTestTarget(1), now.Add(-time.Minute))
	seedBinding(store, "early", persistTestTarget(2), now.Add(30*time.Minute))
	seedBinding(store, "late", persistTestTarget(3), now.Add(50*time.Minute))
	seedBinding(store, "middle", persistTestTarget(4), now.Add(40*time.Minute))

	cache := newCache(2, time.Hour, clock.Now)
	restored, err := NewPersister(cache, store).Restore(context.Background(), 1, 2, time.Hour)
	if err != nil || restored != 2 {
		t.Fatalf("Restore() = %d, %v; want 2, nil", restored, err)
	}
	// Presence is read from the map: Lookup refreshes recency and would change the order under test.
	present := func(key Key) bool {
		_, ok := cache.entries[key]
		return ok
	}
	if present("expired") || present("early") {
		t.Fatal("restored a binding that is expired or outside capacity")
	}
	if !present("late") || !present("middle") {
		t.Fatal("the two latest-expiring live bindings were not restored")
	}
	// The restored recency order puts the earliest-expiring binding at the cold end.
	cache.RecordSuccess("newest", Observation{}, persistTestTarget(5))
	if present("middle") || !present("late") {
		t.Fatal("capacity eviction did not remove the earliest-expiring restored binding first")
	}
}

func TestConfigureKeepsRestoredBindingsWhenPolicyUnchanged(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	seedBinding(store, "conversation", persistTestTarget(1), clock.Now().Add(time.Hour))

	cache := newCache(10, time.Hour, clock.Now)
	persister := NewPersister(cache, store)
	if restored, err := persister.Restore(context.Background(), 5, 10, time.Hour); err != nil || restored != 1 {
		t.Fatalf("Restore() = %d, %v; want 1, nil", restored, err)
	}
	if !cache.Configure(6, 10, time.Hour) {
		t.Fatal("first Configure() with the unchanged policy = false")
	}
	if got := cache.Lookup("conversation"); !got.Found() {
		t.Fatal("first Configure() with the unchanged policy wiped the restored binding")
	}
	persister.flushAll(context.Background(), true)
	if store.count() != 1 || store.clearCount() != 0 {
		t.Fatalf("unchanged policy touched the store: rows=%d clears=%d", store.count(), store.clearCount())
	}
}

func TestConfigurePolicyChangeClearsPersistedRows(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	seedBinding(store, "one", persistTestTarget(1), clock.Now().Add(time.Hour))
	seedBinding(store, "two", persistTestTarget(2), clock.Now().Add(time.Hour))

	cache := newCache(10, time.Hour, clock.Now)
	persister := NewPersister(cache, store)
	if restored, err := persister.Restore(context.Background(), 5, 10, time.Hour); err != nil || restored != 2 {
		t.Fatalf("Restore() = %d, %v; want 2, nil", restored, err)
	}
	if !cache.Configure(6, 10, 30*time.Minute) {
		t.Fatal("Configure() with a new TTL = false")
	}
	if cache.Lookup("one").Found() {
		t.Fatal("a TTL change kept an in-memory binding")
	}
	persister.flushAll(context.Background(), true)
	if store.count() != 0 || store.clearCount() != 1 {
		t.Fatalf("TTL change left persisted rows: rows=%d clears=%d", store.count(), store.clearCount())
	}
	restarted := newCache(10, time.Hour, clock.Now)
	if restored, err := NewPersister(restarted, store).Restore(context.Background(), 7, 10, time.Hour); err != nil || restored != 0 {
		t.Fatalf("Restore() after a policy change = %d, %v; want 0, nil", restored, err)
	}
}

func TestRecordSuccessNeverWaitsForStoreAndWritesInBatches(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	store.gate = make(chan struct{})
	cache := newCache(1000, time.Hour, clock.Now)
	persister := NewPersister(cache, store)
	persister.batchSize = 100
	persister.flushInterval = time.Hour
	persister.Start()

	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		for index := range 500 {
			key := Key(fmt.Sprintf("conversation-%03d", index))
			cache.RecordSuccess(key, cache.Lookup(key), persistTestTarget(1))
		}
	}()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordSuccess waited on a blocked store")
	}
	if got := cache.entryCount(); got != 500 {
		t.Fatalf("cache entries = %d, want 500", got)
	}

	close(store.gate)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := persister.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	batches := store.savedBatches()
	total := 0
	for _, size := range batches {
		if size > 100 {
			t.Fatalf("batch of %d exceeds the batch cap", size)
		}
		total += size
	}
	if total != 500 || len(batches) != 5 {
		t.Fatalf("saved batches = %v, want five batches totalling 500", batches)
	}
	if store.count() != 500 {
		t.Fatalf("persisted rows = %d, want 500", store.count())
	}
}

func TestPersisterCountsOverflowAndOversizedKeyDrops(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)}
	store := newMemoryStore()
	cache := newCache(100, time.Hour, clock.Now)
	persister := NewPersister(cache, store)
	persister.queueCapacity = 5

	// Checked first, while the queue still has room, so this drop is the key-length guard.
	tooLong := Key(strings.Repeat("k", MaxKeyLength+1))
	cache.RecordSuccess(tooLong, Observation{}, persistTestTarget(1))
	if got := persister.Dropped(); got != 1 {
		t.Fatalf("Dropped() after an oversized key = %d, want 1", got)
	}

	for index := range 8 {
		key := Key(fmt.Sprintf("conversation-%d", index))
		if !cache.RecordSuccess(key, cache.Lookup(key), persistTestTarget(1)) {
			t.Fatalf("RecordSuccess(%q) = false; overflow must not affect the in-memory cache", key)
		}
	}
	if got := cache.entryCount(); got != 9 {
		t.Fatalf("cache entries = %d, want 9", got)
	}
	if got := persister.Dropped(); got != 4 {
		t.Fatalf("Dropped() after overflow = %d, want 4 (1 oversized key + 3 overflow)", got)
	}

	if err := persister.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if store.count() != 5 {
		t.Fatalf("persisted rows = %d, want the 5 queued bindings", store.count())
	}
}
