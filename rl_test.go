package limitron

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildRateLimiterRps(t *testing.T) {
	rps := uint16(10)
	s := BuildRateLimiterRps(rps)
	if s.maxreq != rps {
		t.Fatalf("maxreq = %d, want %d", s.maxreq, rps)
	}
	// rrpm should be rps / 1000ms
	want := float64(rps) / 1000.0
	if (s.rrpm-want) > 1e-9 || (want-s.rrpm) > 1e-9 {
		t.Fatalf("rrpm = %f, want %f", s.rrpm, want)
	}
	if s.retries != UpdateRetries {
		t.Fatalf("retries = %d, want %d", s.retries, UpdateRetries)
	}
}

func TestBuildRateLimiter(t *testing.T) {
	req := uint16(5)
	interval := 2 * time.Second
	s := BuildRateLimiter(req, interval)
	if s.maxreq != req {
		t.Fatalf("maxreq = %d, want %d", s.maxreq, req)
	}
	want := float64(req) / float64(interval.Milliseconds())
	if (s.rrpm-want) > 1e-12 || (want-s.rrpm) > 1e-12 {
		t.Fatalf("rrpm = %f, want %f", s.rrpm, want)
	}
}

func TestNewInitialState(t *testing.T) {
	s := BuildRateLimiterRps(7)
	rl := s.New()
	if rl == nil {
		t.Fatal("New() returned nil")
	}
	req, ts := unpackUint16Uint48(atomic.LoadUint64(rl))
	if req != s.maxreq {
		t.Fatalf("initial req = %d, want %d", req, s.maxreq)
	}
	if ts != 0 {
		t.Fatalf("initial ts = %d, want 0", ts)
	}
}

func TestTakeN_AllowWithinBurstAndRemaining(t *testing.T) {
	s := BuildRateLimiterRps(5) // burst=5
	rl := s.New()

	wait, ok := s.TakeN(rl, 3)
	if !ok || wait != 0 {
		t.Fatalf("TakeN(3) => wait=%d ok=%v, want 0,true", wait, ok)
	}
	req, _ := unpackUint16Uint48(atomic.LoadUint64(rl))
	if req != 2 {
		t.Fatalf("remaining tokens = %d, want 2", req)
	}
}

func TestTakeN_ExceedMaxImmediateFail(t *testing.T) {
	s := BuildRateLimiterRps(4)
	rl := s.New()

	wait, ok := s.TakeN(rl, s.maxreq+1)
	if ok {
		t.Fatalf("TakeN(maxreq+1) should fail")
	}
	if wait != math.MaxInt64 {
		t.Fatalf("wait=%d, want MaxInt64", wait)
	}
}

func TestTakeN_ZeroRequestsIsNoop(t *testing.T) {
	s := BuildRateLimiterRps(9)
	rl := s.New()

	before := atomic.LoadUint64(rl)
	wait, ok := s.TakeN(rl, 0)
	after := atomic.LoadUint64(rl)

	if !ok || wait != 0 {
		t.Fatalf("TakeN(0) => wait=%d ok=%v, want 0,true", wait, ok)
	}
	if before != after {
		t.Fatalf("state changed for zero request: before=%d after=%d", before, after)
	}
}

func TestTake1_RefillAfterWait(t *testing.T) {
	// Configure a small bucket with clear per-ms refill.
	// 10 req / second => rrpm = 0.01 tokens/ms
	s := BuildRateLimiterRps(10)
	rl := s.New()

	// Deplete tokens (burst = 10)
	for i := 0; i < int(s.maxreq); i++ {
		if _, ok := s.Take1(rl); !ok {
			t.Fatalf("unexpected failure while depleting at i=%d", i)
		}
	}

	// Next immediate take should fail with a finite wait
	wait, ok := s.Take1(rl)
	if ok {
		t.Fatalf("expected immediate refusal after depleting burst")
	}
	if wait <= 0 {
		t.Fatalf("expected positive wait, got %d", wait)
	}

	// Sleep just a bit more than suggested wait to ensure at least 1 token refilled.
	time.Sleep(time.Duration(wait+5) * time.Millisecond)

	// Now it should succeed
	wait2, ok2 := s.Take1(rl)
	if !ok2 || wait2 != 0 {
		t.Fatalf("expected success after sleeping wait, got wait=%d ok=%v", wait2, ok2)
	}
}

func TestRefillCapsAtMax(t *testing.T) {
	s := BuildRateLimiter(uint16(3), time.Second) // 3 tokens per second
	rl := s.New()

	// Spend all tokens
	for i := 0; i < int(s.maxreq); i++ {
		if _, ok := s.Take1(rl); !ok {
			t.Fatalf("unexpected failure while depleting at i=%d", i)
		}
	}

	// Wait long enough that > max tokens would accrue if uncapped
	time.Sleep(1500 * time.Millisecond)

	// Take exactly maxreq in one go; should succeed since cap refilled to 3
	if wait, ok := s.TakeN(rl, s.maxreq); !ok || wait != 0 {
		t.Fatalf("expected full cap available, got wait=%d ok=%v", wait, ok)
	}

	// Bucket should now be empty (0 remaining)
	req, _ := unpackUint16Uint48(atomic.LoadUint64(rl))
	if req != 0 {
		t.Fatalf("remaining tokens = %d, want 0", req)
	}
}

func TestConcurrentCASContention(t *testing.T) {
	// Make interval relatively long so refill doesn't interfere during the short test duration.
	s := BuildRateLimiter(uint16(50), 10*time.Second) // 50 tokens, very slow refill
	rl := s.New()

	var success int64
	var wg sync.WaitGroup
	workers := 100

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if _, ok := s.Take1(rl); ok {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	got := atomic.LoadInt64(&success)
	// We should never be able to consume more than the burst size.
	if got > int64(s.maxreq) {
		t.Fatalf("successes=%d exceed burst=%d", got, s.maxreq)
	}
	// And typically we should hit the cap closely (allow some slack due to races).
	if got < int64(s.maxreq)-3 {
		t.Fatalf("too few successes under contention: got=%d, want at least %d", got, int64(s.maxreq)-3)
	}
}

func TestWaitMillisReasonableWhenInsufficientTokens(t *testing.T) {
	// 20 req/s => rrpm = 0.02 tokens/ms
	s := BuildRateLimiterRps(20)
	rl := s.New()

	// Spend 19; leave 1 token
	for i := 0; i < 19; i++ {
		if _, ok := s.Take1(rl); !ok {
			t.Fatalf("unexpected depletion failure at i=%d", i)
		}
	}

	// Ask for 5; we have only 1, need 4 more.
	wait, ok := s.TakeN(rl, 5)
	if ok {
		t.Fatalf("expected refusal when asking for 5 with only 1 available")
	}
	// rrpm=0.02 => ~200ms for 4 tokens; function returns 1 + missing/rrpm
	if wait < 180 || wait > 260 {
		t.Fatalf("wait=%dms, expected roughly ~200ms (±20%%)", wait)
	}
}
