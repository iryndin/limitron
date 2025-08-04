package limitron

import "time"

// LeanRateLimiter defines a minimal non-blocking, zero-allocation,
// lock-free rate-limiting interface that stores the entire per-key limiter state
// in a single uint64.
//
// This design is GC-friendly and ideal for very high-cardinality limits
// (e.g., per-IP, per-API key, per-user).
//
// Implementations should be safe for concurrent use by multiple goroutines
// operating on the *same* rl pointer (assuming 64-bit alignment; see notes).
//
// The rl argument holds the entire limiter state (encoded into a uint64 value).
// Call CreateNewRl() once per key to initialize a new state value.
type LeanRateLimiter interface {
	// Take1IfAllowed attempts to consume 1 unit (request/token).
	// Returns true and atomically updates *rl on success;
	// returns false if not allowed now (*rl is not updated in this case).
	Take1IfAllowed(rl *uint64) bool

	// TakeNIfAllowed attempts to consume 'requests' units atomically.
	// Returns true and updates *rl on success; returns false if the request
	// exceeds the current allowance or policy (e.g., burst) at this moment.
	// requests == 0 is treated as a no-op and should return true.
	TakeNIfAllowed(rl *uint64, requests uint16) bool

	// CreateNewRl creates a brand-new, zero-use limiter state.
	// Call this once per identity (user/IP/apiKey/etc) and store it;
	// pass a pointer to this uint64 into Take* calls.
	CreateNewRl() uint64
}

const UpdateRetries = 3

// CreateLeanRateLimiterRps returns a LeanRateLimiter that allows up to `rps` requests per second,
// with a burst capacity equal to `rps`. Internally, this is a shorthand for calling
// CreateLeanRateLimiter(rps, time.Second).
//
// Example:
//
//	limiter := CreateLeanRateLimiterRps(10) // 10 requests per second
//
// See: CreateLeanRateLimiter for general-purpose rate limiting over any interval.
func CreateLeanRateLimiterRps(rps uint16) LeanRateLimiter {
	return CreateLeanRateLimiter(rps, time.Second)
}

// CreateLeanRateLimiter returns a LeanRateLimiter that allows up to `req` requests per given `interval`.
// This allows for flexible configurations such as per-second, per-minute, or per-hour rate limiting.
//
// Parameters:
//   - req:     number of allowed requests per interval (burst == req)
//   - interval: time duration over which the `req` applies
//
// Examples:
//
//	limiter := CreateLeanRateLimiter(100, time.Minute) // 100 reqs per minute
//	limiter := CreateLeanRateLimiter(5, 2*time.Second) // 5 reqs per 2 seconds
//
// Internally uses a token bucket approximation with floating point precision.
// Concurrency-safe for shared use of rl pointers.
//
// Note: If you're rate limiting per second, use CreateLeanRateLimiterRps for simplicity.
func CreateLeanRateLimiter(req uint16, interval time.Duration) LeanRateLimiter {
	rl := leanRateLimiterImpl{
		maxreq:  req,
		rrpm:    float64(req) / float64(interval.Milliseconds()),
		retries: UpdateRetries,
	}
	return rl
}
