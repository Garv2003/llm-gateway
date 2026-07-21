package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAllowsUpToBurstThenDenies(t *testing.T) {
	l := NewLocal(1, 3)

	for i := 0; i < 3; i++ {
		allowed, _, err := l.Allow(context.Background(), "k")
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			t.Fatalf("request %d within burst was denied", i+1)
		}
	}

	allowed, retryAfter, err := l.Allow(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("request beyond burst should be denied")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter = %v, want > 0", retryAfter)
	}
}

func TestPerKeyIsolation(t *testing.T) {
	l := NewLocal(1, 1)

	if allowed, _, _ := l.Allow(context.Background(), "alice"); !allowed {
		t.Fatal("alice's first request should be allowed")
	}
	// alice is now exhausted, but bob has his own bucket.
	if allowed, _, _ := l.Allow(context.Background(), "alice"); allowed {
		t.Error("alice's second request should be denied")
	}
	if allowed, _, _ := l.Allow(context.Background(), "bob"); !allowed {
		t.Error("bob's first request must not be affected by alice")
	}
}

func TestRefillOverTime(t *testing.T) {
	l := NewLocal(2, 1) // 2 tokens/sec, burst 1

	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	if allowed, _, _ := l.Allow(context.Background(), "k"); !allowed {
		t.Fatal("first request should be allowed")
	}
	if allowed, _, _ := l.Allow(context.Background(), "k"); allowed {
		t.Fatal("immediate second request should be denied (bucket empty)")
	}

	// After 500ms at 2 rps, exactly one token has refilled.
	now = now.Add(500 * time.Millisecond)
	if allowed, _, _ := l.Allow(context.Background(), "k"); !allowed {
		t.Error("request after refill window should be allowed")
	}
}

func TestRefillCapsAtBurst(t *testing.T) {
	l := NewLocal(10, 2)

	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }

	// Idle far longer than needed to refill; tokens must not exceed burst.
	now = now.Add(time.Hour)
	for i := 0; i < 2; i++ {
		if allowed, _, _ := l.Allow(context.Background(), "k"); !allowed {
			t.Fatalf("request %d within burst after long idle was denied", i+1)
		}
	}
	if allowed, _, _ := l.Allow(context.Background(), "k"); allowed {
		t.Error("tokens should have been capped at burst, third request must deny")
	}
}

func TestConcurrentAllowIsRaceFree(t *testing.T) {
	l := NewLocal(1000, 100)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _, _ = l.Allow(context.Background(), "shared")
			}
		}()
	}
	wg.Wait()
}
