package mirasimstatus

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheFailureBackoffPreservesOldDataAndRecovers(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	var calls int
	fail := false
	reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if fail {
			return responseWith(503, ""), nil
		}
		return responseWith(200, fixture(t, now)), nil
	})})
	reader.now = func() time.Time { return now }
	first := reader.Snapshot(t.Context())
	if first.Stale || first.Data == nil || calls != 1 {
		t.Fatalf("first=%+v calls=%d", first, calls)
	}
	first.Data.Cohorts[0].Agents[0].Summary.Cells[0] = nil
	*first.FetchedAt = time.Time{}
	now = now.Add(59 * time.Second)
	cached := reader.Snapshot(t.Context())
	if calls != 1 || cached.Data.Cohorts[0].Agents[0].Summary.Cells[0] == nil || cached.FetchedAt.IsZero() {
		t.Fatalf("cache alias or premature fetch: %+v calls=%d", cached, calls)
	}
	lastFetched := *cached.FetchedAt
	now = now.Add(time.Second)
	fail = true
	failed := reader.Snapshot(t.Context())
	if calls != 2 || !failed.Stale || failed.Data == nil || failed.Error == "" || !failed.FetchedAt.Equal(lastFetched) || !failed.CheckedAt.Equal(now) {
		t.Fatalf("failure = %+v calls=%d", failed, calls)
	}
	now = now.Add(29 * time.Second)
	_ = reader.Snapshot(t.Context())
	if calls != 2 {
		t.Fatal("failure backoff did not cache")
	}
	now = now.Add(time.Second)
	_ = reader.Snapshot(t.Context())
	if calls != 3 {
		t.Fatal("first retry must happen after 30 seconds")
	}
	now = now.Add(59 * time.Second)
	_ = reader.Snapshot(t.Context())
	if calls != 3 {
		t.Fatal("consecutive failure should back off for 60 seconds")
	}
	now = now.Add(time.Second)
	fail = false
	recovered := reader.Snapshot(t.Context())
	if calls != 4 || recovered.Stale || recovered.Error != "" || !recovered.FetchedAt.Equal(now) {
		t.Fatalf("recovery = %+v calls=%d", recovered, calls)
	}
	if reader.failures != 0 {
		t.Fatal("success should reset failure backoff")
	}
}

func TestSourceAgeIncludesDataThroughAndAgesInsideCache(t *testing.T) {
	t.Parallel()
	for _, sourceAge := range []time.Duration{4*time.Minute + 30*time.Second, 6 * time.Minute} {
		t.Run(sourceAge.String(), func(t *testing.T) {
			now := time.Now().UTC()
			generated := now
			payload := strings.Replace(fixture(t, generated), generated.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(-sourceAge).Format(time.RFC3339Nano), 1)
			reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return responseWith(200, payload), nil })})
			reader.now = func() time.Time { return now }
			got := reader.Snapshot(t.Context())
			if got.Stale != (sourceAge > maxSourceAge) {
				t.Fatalf("unexpected initial stale=%v", got.Stale)
			}
			now = now.Add(31 * time.Second)
			got = reader.Snapshot(t.Context())
			if !got.Stale || got.Data == nil || !strings.Contains(got.Error, "older than 5 minutes") {
				t.Fatalf("age snapshot = %+v", got)
			}
		})
	}
}

func TestConcurrentSnapshotsShareOneFetch(t *testing.T) {
	t.Parallel()
	payload := fixture(t, time.Now().UTC())
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return responseWith(200, payload), nil
	})})
	var wg sync.WaitGroup
	results := make(chan Snapshot, 32)
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- reader.Snapshot(t.Context()) }()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("concurrent fetches = %d", calls.Load())
	}
	for got := range results {
		if got.Stale || got.Data == nil {
			t.Fatalf("concurrent result = %+v", got)
		}
	}
}

func TestWaitingCallerCanCancelWithoutCancelingSharedFetch(t *testing.T) {
	t.Parallel()
	payload := fixture(t, time.Now().UTC())
	started := make(chan struct{})
	release := make(chan struct{})
	reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return responseWith(200, payload), nil
	})})
	owner := make(chan Snapshot, 1)
	go func() { owner <- reader.Snapshot(t.Context()) }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	got := reader.Snapshot(ctx)
	if !got.Stale || !strings.Contains(got.Error, "deadline exceeded") {
		t.Fatalf("canceled waiter = %+v", got)
	}
	close(release)
	if got := <-owner; got.Stale || got.Data == nil {
		t.Fatalf("owner = %+v", got)
	}
}

func TestCanceledOwnerDoesNotPoisonCache(t *testing.T) {
	t.Parallel()
	payload := fixture(t, time.Now().UTC())
	started := make(chan struct{})
	var calls atomic.Int32
	reader := New(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return responseWith(200, payload), nil
	})})
	ctx, cancel := context.WithCancel(t.Context())
	owner := make(chan Snapshot, 1)
	go func() { owner <- reader.Snapshot(ctx) }()
	<-started
	cancel()
	if got := <-owner; !got.Stale || !strings.Contains(got.Error, "canceled") {
		t.Fatalf("owner = %+v", got)
	}
	if got := reader.Snapshot(t.Context()); got.Stale || got.Data == nil || calls.Load() != 2 {
		t.Fatalf("retry = %+v calls=%d", got, calls.Load())
	}
}

func TestTransportSecretsAreNotExposedAndFirstFailureCached(t *testing.T) {
	t.Parallel()
	var calls int
	reader := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("proxy https://user:private-password@example.test failed")
	})})
	for range 2 {
		got := reader.Snapshot(t.Context())
		if !got.Stale || got.Data != nil || got.FetchedAt != nil || got.Error == "" || strings.Contains(got.Error, "private-password") {
			t.Fatalf("first failure = %+v", got)
		}
	}
	if calls != 1 {
		t.Fatal("first failure was not cached")
	}
}
