package ratelimit

import (
	"sync"
	"time"
)

const (
	CredentialConcurrencyExceeded = "credential_concurrency_limit_exceeded"
	CredentialRateExceeded        = "credential_rate_limit_exceeded"
)

type CredentialCapacityDecision struct {
	Allowed    bool
	Reason     string
	RetryAfter time.Duration
}

type credentialDispatch struct {
	at     time.Time
	serial uint64
}

type credentialCapacityWindow struct {
	active int64
	values []credentialDispatch
	head   int
	length int
	count  int64
}

func (w *credentialCapacityWindow) expire(cutoff time.Time) {
	for w.length > 0 {
		entry := &w.values[w.head]
		if entry.at.After(cutoff) {
			break
		}
		if entry.serial != 0 {
			w.count--
		}
		*entry = credentialDispatch{}
		w.head = (w.head + 1) % len(w.values)
		w.length--
	}
	if w.length == 0 {
		w.head = 0
	}
}

func (w *credentialCapacityWindow) push(entry credentialDispatch) {
	if w.length == len(w.values) {
		values := make([]credentialDispatch, max(4, 2*len(w.values)))
		for i := range w.length {
			values[i] = w.values[(w.head+i)%len(w.values)]
		}
		w.values, w.head = values, 0
	}
	w.values[(w.head+w.length)%len(w.values)] = entry
	w.length++
	w.count++
}

// CredentialCapacity shares one admission window across all callers of a credential.
// Its zero value is ready to use; active counts survive policy changes.
type CredentialCapacity struct {
	mu          sync.Mutex
	windows     map[uint]*credentialCapacityWindow
	serial      uint64
	lastCleanup time.Time
	lastNow     time.Time
}

// CredentialLease owns one admitted dispatch, not a client connection. Release is idempotent.
// Transfer ownership when assigning a lease; never release an independent copy.
type CredentialLease struct {
	limiter      *CredentialCapacity
	credentialID uint
	serial       uint64
	released     bool
}

func (l *CredentialCapacity) TryAcquire(id uint, concurrent, rpm int64, now time.Time) (CredentialLease, CredentialCapacityDecision) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.lastNow) {
		now = l.lastNow
	} else {
		l.lastNow = now
	}
	if l.windows == nil {
		l.windows = make(map[uint]*credentialCapacityWindow)
	}
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for id, window := range l.windows {
			window.expire(now.Add(-time.Minute))
			if window.active == 0 && window.length == 0 {
				delete(l.windows, id)
			}
		}
		l.lastCleanup = now
	}
	w := l.windows[id]
	if w == nil {
		w = &credentialCapacityWindow{}
		l.windows[id] = w
	}
	w.expire(now.Add(-time.Minute))
	if concurrent > 0 && w.active >= concurrent {
		return CredentialLease{}, CredentialCapacityDecision{Reason: CredentialConcurrencyExceeded}
	}
	if rpm > 0 && w.count >= rpm {
		entry := w.values[(w.head+w.length-int(rpm))%len(w.values)]
		return CredentialLease{}, CredentialCapacityDecision{Reason: CredentialRateExceeded, RetryAfter: max(time.Second, ceilToSecond(entry.at.Add(time.Minute).Sub(now)))}
	}
	lease := CredentialLease{limiter: l, credentialID: id}
	if rpm > 0 {
		l.serial++
		if l.serial == 0 {
			l.serial++
		}
		lease.serial = l.serial
		w.push(credentialDispatch{at: now, serial: lease.serial})
	} else {
		w.values, w.head, w.length, w.count = nil, 0, 0, 0
	}
	w.active++
	return lease, CredentialCapacityDecision{Allowed: true}
}

func (lease *CredentialLease) Release() {
	if lease.limiter == nil {
		return
	}
	l := lease.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if lease.released {
		return
	}
	lease.released = true
	if w := l.windows[lease.credentialID]; w != nil {
		w.active--
	}
}

// RefundNotDispatched removes the RPM reservation only when execution proves nothing was sent.
func (lease *CredentialLease) RefundNotDispatched() {
	if lease.limiter == nil || lease.serial == 0 {
		return
	}
	l := lease.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.windows[lease.credentialID]; w != nil {
		for i := range w.length {
			entry := &w.values[(w.head+i)%len(w.values)]
			if entry.serial != lease.serial {
				continue
			}
			for j := i; j < w.length-1; j++ {
				w.values[(w.head+j)%len(w.values)] = w.values[(w.head+j+1)%len(w.values)]
			}
			w.values[(w.head+w.length-1)%len(w.values)] = credentialDispatch{}
			w.length--
			w.count--
			break
		}
	}
	lease.serial = 0
}
