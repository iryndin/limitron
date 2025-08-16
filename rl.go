package limitron

import (
	"math"
	"sync/atomic"
	"time"
)

const UpdateRetries = 5

// RateLimiter defines a minimal non-blocking, zero-allocation,
// lock-free rate limiter that stores the entire per-key limiter state
// in a single uint64.
//
// This design is GC-friendly and ideal for very high-cardinality limits
// (e.g., per-IP, per-API key, per-user).
//
// Implementations should be safe for concurrent use by multiple goroutines
// operating on the *same* rl pointer (assuming 64-bit alignment; see notes).
//
// The rl argument holds the entire limiter state (encoded into an uint64 value).
// Call New() once per key to initialize a new state value.
//
// Fields:
//   - maxreq:  Maximum number of requests allowed per configured interval (defines the burst size).
//   - rrpm:    Refill rate per millisecond. Calculated as maxreq / interval.Milliseconds().
//     Used internally to replenish tokens based on elapsed time.
//   - retries: Number of CAS (Compare-And-Swap) retries attempted during concurrent updates of *rl state.
//     A small integer (e.g., 4–8) balances correctness under contention with performance.
type RateLimiter struct {
	// maxreq defines the burst size — the maximum number of requests allowed
	// within the specified interval window. This value is packed into the limiter state.
	maxreq uint16

	// rrpm stands for "request refill rate per millisecond" and defines how quickly the limiter
	// should refill allowance over time. This is used to calculate the current
	// available tokens when a request is made.
	rrpm float64

	// retries controls the number of atomic CAS (Compare-And-Swap) attempts made
	// when updating the shared limiter state concurrently. It helps ensure
	// correctness under contention without indefinite spinning.
	retries int
}

// BuildRateLimiterRps returns a RateLimiter that allows up to `rps` requests per second,
// with a burst capacity equal to `rps`. Internally, this is a shorthand for calling
// BuildRateLimiter(rps, time.Second).
//
// Example:
//
//	limiter := BuildRateLimiterRps(10) // 10 requests per second
//
// See: BuildRateLimiter for general-purpose rate limiting over any interval.
func BuildRateLimiterRps(rps uint16) RateLimiter {
	return BuildRateLimiter(rps, time.Second)
}

// BuildRateLimiter returns a RateLimiter that allows up to `req` requests per given `interval`.
// This allows for flexible configurations such as per-second, per-minute, or per-hour rate limiting.
//
// Parameters:
//   - req:     number of allowed requests per interval (burst == req)
//   - interval: time duration over which the `req` applies
//
// Examples:
//
//	limiter1 := BuildRateLimiter(100, time.Minute) // 100 reqs per minute
//	limiter2 := BuildRateLimiterBuildRateLimiter(5, 2*time.Second) // 5 reqs per 2 seconds
//
// Internally uses a token bucket approximation with floating point precision.
// Concurrency-safe for shared use of rl pointers.
//
// Note: If you're rate limiting per second, use BuildRateLimiterRps for simplicity.
func BuildRateLimiter(req uint16, interval time.Duration) RateLimiter {
	return BuildRateLimiterFull(req, interval, UpdateRetries)
}

func BuildRateLimiterFull(req uint16, interval time.Duration, retries int) RateLimiter {
	return RateLimiter{
		maxreq:  req,
		rrpm:    float64(req) / float64(interval.Milliseconds()),
		retries: retries,
	}
}

// New creates a brand-new, zero-use limiter state.
// Call this once per identity (user/IP/apiKey/etc) and store it;
// pass a pointer to this uint64 into Take* calls.
func (s RateLimiter) New() *uint64 {
	rl := packUint16AndUint48(s.maxreq, 0)
	return &rl
}

// Take1 attempts to consume 1 unit (request/token).
// Returns true and atomically updates *rl on success;
// returns false if not allowed now (*rl is not updated in this case).
// Internally, this is a shorthand for calling TakeN(rps, time.Second).
func (s RateLimiter) Take1(rl *uint64) (int64, bool) {
	return s.TakeN(rl, 1)
}

// TakeN attempts to atomically consume `requests` units from the limiter state `*rl`.
//
// It returns number of milliseconds to wait and a boolean flag if the request can be fulfilled under the current allowance.
// For instance, it will return `0`, `true` if the request can be fulfilled.
// In other case, it could return `200`, `false` which means
// that request cannot be fulfilled right now, and you have to wait at least `200 milliseconds` before repeating the request.
// This method is non-blocking and retry-based (CAS loop).
//
// Parameters:
//   - rl: pointer to the 64-bit state (must be aligned and unique per identity)
//   - requests: number of tokens to consume (must be ≤ maxreq)
//
// Returns:
//   - 0,true if the request is allowed and tokens were consumed
//   - N, false if the request exceeds the available allowance or retry attempts fail. N is number of millis to wait before the next retry
//
// Edge cases:
//   - If `requests == 0`: always returns (0, true) - noop
//   - If `requests > maxreq`: returns (math.MaxInt64, false) immediately
//
// Internally uses atomic CAS to safely update the state under contention.
func (s RateLimiter) TakeN(rl *uint64, requests uint16) (int64, bool) {
	if requests == 0 {
		return 0, true
	} else if requests > s.maxreq {
		return math.MaxInt64, false
	}

	for i := 0; i < s.retries; i++ {
		// Atomically get current value of rl
		// (remember: the other clients might use this rl at the same time, hence we need atomic call)
		rlval := atomic.LoadUint64(rl)
		// calculate new values for requests and timestamp
		// with respect to time that passes since the last access timestamp
		// (last access timestamp is encoded in rlval - in its lower 48 bits)
		newreq, ts := s.calcNewRequests(rlval)

		// requested tokens are greater than currently available number of tokens
		if requests > newreq {
			waitMillis := 1 + int64(float64(requests-newreq)/s.rrpm)
			return waitMillis, false
		}

		newreq -= requests
		newrlval := packUint16AndUint48(newreq, ts)

		// if the value hasn't changed since we read it in line 134
		// then we are good to go.
		// Otherwise, let's repeat the entire loop again
		if atomic.CompareAndSwapUint64(rl, rlval, newrlval) {
			return 0, true
		}
	}

	// If we are here, this means that "Retries" times CAS operation (atomic.CompareAndSwapUint64)
	// returned false. So, we hadn't to wait, and failed to update rl
	// only because concurrent modifications occurred.
	// So it is safe to assume that waitMillis could be 1 millisecond to have minimal wait
	return 1, false
}

// calcNewReq computes the updated number of available requests (tokens) based on
// the time elapsed since the last recorded timestamp in the limiter state.
//
// Parameters:
//   - rl: the encoded 64-bit limiter state (current tokens and last timestamp)
//
// Returns:
//   - newreq: the refilled token count (capped at maxreq)
//   - ts:     the current timestamp in Unix milliseconds (used for the next state update)
//
// This function performs refill logic using a token bucket approximation:
//   - Tokens are replenished over time at a fixed rate (rrpm).
//   - The number of tokens is capped at maxreq (burst size).
func (s RateLimiter) calcNewRequests(rl uint64) (newreq uint16, ts uint64) {
	// req - current requests
	// lastTs - last access timestamp in unix millis
	req, lastTs := unpackUint16Uint48(rl)
	ts = uint64(time.Now().UnixMilli())
	// refillReq - refilled requests since last access timestamp
	refillReq := uint64(s.rrpm * float64(ts-lastTs))
	// new requests (uncapped)
	uncappedReq := uint64(req) + refillReq

	newreq = s.maxreq
	if uncappedReq < uint64(newreq) {
		newreq = uint16(uncappedReq)
	}

	return
}
