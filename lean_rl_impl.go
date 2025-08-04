package limitron

import (
	"sync/atomic"
	"time"
)

// leanRateLimiterImpl is a lightweight, allocation-free implementation of the LeanRateLimiter interface.
// It uses a fixed-size 64-bit state (passed by pointer) to track and enforce request rate limits.
//
// This struct is safe for use across many identities, as each limiter instance is stateless except for its configuration.
//
// Fields:
//   - maxreq:  Maximum number of requests allowed per configured interval (defines the burst size).
//   - rrpm:    Refill rate per millisecond. Calculated as maxreq / interval.Milliseconds().
//     Used internally to replenish tokens based on elapsed time.
//   - retries: Number of CAS (Compare-And-Swap) retries attempted during concurrent updates of *rl state.
//     A small integer (e.g., 4–8) balances correctness under contention with performance.
type leanRateLimiterImpl struct {
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

func (s leanRateLimiterImpl) CreateNewRl() uint64 {
	return packUint16AndUint48(s.maxreq, 0)
}

func (s leanRateLimiterImpl) Take1IfAllowed(rl *uint64) bool {
	return s.TakeNIfAllowed(rl, 1)
}

// TakeNIfAllowed attempts to atomically consume `requests` units from the limiter state `*rl`.
//
// It returns true if the request can be fulfilled under the current allowance; false otherwise.
// This method is non-blocking and retry-based (CAS loop).
//
// Parameters:
//   - rl: pointer to the 64-bit state (must be aligned and unique per identity)
//   - requests: number of tokens to consume (must be ≤ maxreq)
//
// Returns:
//   - true if the request is allowed and tokens were consumed
//   - false if the request exceeds the available allowance or retry attempts fail
//
// Edge cases:
//   - If `requests == 0`: always returns true (noop)
//   - If `requests > maxreq`: returns false immediately
//
// Internally uses atomic CAS to safely update the state under contention.
func (s leanRateLimiterImpl) TakeNIfAllowed(rl *uint64, requests uint16) bool {
	if requests == 0 {
		return true
	} else if requests > s.maxreq {
		return false
	}

	for i := 0; i < s.retries; i++ {
		rlval := atomic.LoadUint64(rl)
		// calculate new values for requests and timestamp
		newreq, ts := s.calcNewReq(rlval)

		if requests > newreq {
			return false
		}

		newreq -= requests
		newrlval := packUint16AndUint48(newreq, ts)
		if atomic.CompareAndSwapUint64(rl, rlval, newrlval) {
			return true
		}
	}

	return false
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
func (s leanRateLimiterImpl) calcNewReq(rl uint64) (newreq uint16, ts uint64) {
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
