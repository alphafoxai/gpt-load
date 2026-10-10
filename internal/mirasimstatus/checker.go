package mirasimstatus

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const (
	successTTL        = 60 * time.Second
	initialFailureTTL = 30 * time.Second
	maxFailureTTL     = 60 * time.Second
	maxSourceAge      = 5 * time.Minute
)

type fetcher interface {
	fetch(context.Context) (*Data, error)
}

// Checker coalesces concurrent refreshes and retains the last good document.
// No background poller is started: only calls to Snapshot trigger a fetch.
type Checker struct {
	client    fetcher
	mu        sync.Mutex
	inflight  chan struct{}
	cached    Snapshot
	expiresAt time.Time
	failures  int
	now       func() time.Time
}

func newChecker(client fetcher) *Checker {
	return &Checker{client: client, now: time.Now}
}

// Snapshot returns a detached copy of the cache, refreshing synchronously when
// needed. Waiters may cancel independently. A caller cancellation never poisons
// the shared cache or consumes failure backoff; other waiters can retry.
func (c *Checker) Snapshot(ctx context.Context) Snapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.client == nil {
		return Snapshot{SourceURL: SourceURL, CheckedAt: time.Now().UTC(), Stale: true, Error: "Mirasim status: reader is unavailable"}
	}
	for {
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			result := c.snapshotLocked()
			result.Stale = true
			result.Error = "Mirasim status: " + err.Error()
			c.mu.Unlock()
			return result
		}
		if c.now().Before(c.expiresAt) {
			result := c.snapshotLocked()
			c.mu.Unlock()
			return result
		}
		if c.inflight != nil {
			done := c.inflight
			c.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
			}
			continue
		}
		c.inflight = make(chan struct{})
		c.mu.Unlock()

		data, err := c.client.fetch(ctx)
		c.mu.Lock()
		now := c.now().UTC()
		if ctx.Err() == nil {
			c.cached.SourceURL = SourceURL
			c.cached.CheckedAt = now
			if err != nil {
				c.failures++
				delay := initialFailureTTL
				if c.failures > 1 {
					delay = maxFailureTTL
				}
				c.expiresAt = now.Add(delay)
				c.cached.Error = err.Error()
				c.cached.Stale = true
			} else {
				c.failures = 0
				c.expiresAt = now.Add(successTTL)
				c.cached = Snapshot{SourceURL: SourceURL, CheckedAt: now, FetchedAt: &now, Data: data}
			}
		}
		close(c.inflight)
		c.inflight = nil
		result := c.snapshotLocked()
		if ctx.Err() != nil {
			result.Stale = true
			result.Error = "Mirasim status: " + ctx.Err().Error()
		}
		c.mu.Unlock()
		return result
	}
}

func (c *Checker) snapshotLocked() Snapshot {
	result := c.cached
	result.SourceURL = SourceURL
	if result.CheckedAt.IsZero() {
		result.CheckedAt = c.now().UTC()
	}
	if result.Data == nil {
		result.Stale = true
	} else {
		now := c.now()
		if now.Sub(result.Data.GeneratedAt) > maxSourceAge || now.Sub(result.Data.DataThrough) > maxSourceAge {
			result.Stale = true
			if result.Error == "" {
				result.Error = "Mirasim status: source data is older than 5 minutes"
			}
		}
		// The schema consists only of JSON-native values and validated timestamps.
		// Cloning prevents callers from mutating cached slices or metric pointers.
		encoded, _ := json.Marshal(result.Data)
		var data Data
		_ = json.Unmarshal(encoded, &data)
		result.Data = &data
	}
	if result.FetchedAt != nil {
		fetchedAt := *result.FetchedAt
		result.FetchedAt = &fetchedAt
	}
	return result
}
