package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestCredentialCapacityAtomicConcurrencyAndRPM(t *testing.T) {
	var limiter CredentialCapacity
	now := time.Unix(1000, 0)
	first, decision := limiter.TryAcquire(7, 1, 2, now)
	if !decision.Allowed {
		t.Fatal(decision)
	}
	if _, blocked := limiter.TryAcquire(7, 1, 2, now); blocked.Reason != CredentialConcurrencyExceeded {
		t.Fatal(blocked)
	}
	first.Release()
	first.Release()
	second, decision := limiter.TryAcquire(7, 1, 2, now)
	if !decision.Allowed {
		t.Fatalf("concurrency rejection consumed RPM: %+v", decision)
	}
	second.Release()
	if _, blocked := limiter.TryAcquire(7, 1, 2, now.Add(time.Minute-time.Nanosecond)); blocked.Reason != CredentialRateExceeded || blocked.RetryAfter != time.Second {
		t.Fatal(blocked)
	}
	third, decision := limiter.TryAcquire(7, 1, 2, now.Add(time.Minute))
	if !decision.Allowed {
		t.Fatalf("exact window boundary: %+v", decision)
	}
	third.Release()
}

func TestCredentialCapacityConcurrentCallersShareCredential(t *testing.T) {
	var limiter CredentialCapacity
	var workers sync.WaitGroup
	start := make(chan struct{})
	release := make(chan struct{})
	admissions := make(chan bool, 32)
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			lease, decision := limiter.TryAcquire(1, 3, 32, time.Unix(1000, 0))
			admissions <- decision.Allowed
			<-release
			lease.Release()
		}()
	}
	close(start)
	allowed := 0
	for range 32 {
		if <-admissions {
			allowed++
		}
	}
	close(release)
	workers.Wait()
	if allowed != 3 {
		t.Fatalf("admitted %d, want 3", allowed)
	}
	lease, decision := limiter.TryAcquire(2, 3, 1, time.Unix(1000, 0))
	if !decision.Allowed {
		t.Fatalf("other credential shared the cap: %+v", decision)
	}
	lease.Release()
}

func TestCredentialCapacityRefundAndBoundedWindow(t *testing.T) {
	var limiter CredentialCapacity
	now := time.Unix(1000, 0)
	for range 1000 {
		lease, decision := limiter.TryAcquire(1, 1, 1, now)
		if !decision.Allowed {
			t.Fatal(decision)
		}
		lease.RefundNotDispatched()
		lease.RefundNotDispatched()
		lease.Release()
	}
	if w := limiter.windows[1]; w.length != 0 || len(w.values) > 4 {
		t.Fatalf("refunded window grew: %+v", w)
	}
	lease, decision := limiter.TryAcquire(1, 1, 9007199254740991, now)
	if !decision.Allowed || len(limiter.windows[1].values) > 4 {
		t.Fatal("allocated configured rather than used capacity")
	}
	lease.Release()
	other, _ := limiter.TryAcquire(2, 1, 1, now.Add(time.Minute))
	other.Release()
	if _, exists := limiter.windows[1]; exists {
		t.Fatal("idle expired credential was retained")
	}
}

func TestCredentialCapacityPolicyReductionAndRingReuse(t *testing.T) {
	var limiter CredentialCapacity
	now := time.Unix(1000, 0)
	for i := range 4 {
		lease, decision := limiter.TryAcquire(1, 0, 4, now.Add(time.Duration(i)*time.Second))
		if !decision.Allowed {
			t.Fatal(decision)
		}
		lease.Release()
	}
	if _, decision := limiter.TryAcquire(1, 0, 2, now.Add(60*time.Second)); decision.Allowed || decision.RetryAfter != 2*time.Second {
		t.Fatalf("lowered policy retry: %+v", decision)
	}
	lease, decision := limiter.TryAcquire(1, 0, 2, now.Add(62*time.Second))
	if !decision.Allowed {
		t.Fatal(decision)
	}
	lease.Release()
	if len(limiter.windows[1].values) != 4 {
		t.Fatal("sliding window reallocated")
	}
}

func TestCredentialCapacityBackwardClockDoesNotMoveWindow(t *testing.T) {
	var limiter CredentialCapacity
	now := time.Unix(1000, 0)
	first, _ := limiter.TryAcquire(1, 1, 2, now)
	first.Release()
	second, decision := limiter.TryAcquire(1, 1, 2, now.Add(-time.Hour))
	if !decision.Allowed {
		t.Fatal(decision)
	}
	second.Release()
	if _, blocked := limiter.TryAcquire(1, 1, 2, now.Add(59*time.Second)); blocked.Allowed || blocked.RetryAfter != time.Second {
		t.Fatalf("backward clock reordered timestamps: %+v", blocked)
	}
	third, decision := limiter.TryAcquire(1, 1, 2, now.Add(time.Minute))
	if !decision.Allowed {
		t.Fatal(decision)
	}
	third.Release()
}
