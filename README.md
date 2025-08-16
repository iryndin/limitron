# Limitron

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![GoDoc](https://pkg.go.dev/badge/github.com/iryndin/limitron.svg)](https://pkg.go.dev/github.com/iryndin/limitron)
[![Go Report Card](https://goreportcard.com/badge/github.com/iryndin/limitron)](https://goreportcard.com/report/github.com/iryndin/limitron)

**Limitron** is a lean, lock-free, zero-allocation, garbage-collector-friendly rate limiter designed for ultra-high cardinality use cases (e.g., per-IP, per-user, per-API key).

It encodes the entire limiter state into a single `uint64` and is designed for extreme performance, minimal memory overhead, and safe concurrent access in Go applications.

## 1. Features

- **Zero allocations** per operation
- **GC-friendly**: Stores all state in a plain `uint64`
- **Lock-free**, non-blocking design with atomic CAS retries
- **Concurrency-safe** for shared use by multiple goroutines
- Customizable rate limits per time interval
- Designed for **high cardinality** scenarios (millions of limiters)
- Suitable for **per-identity** limits: IPs, users, API keys, etc.

## 2. Installation

```bash
go get github.com/iryndin/limitron
```

## 3. Concept

**Limitron** uses a token bucket-like algorithm encoded into a single `uint64`, split as follows:
 - High 16 bits: number of available tokens (requests). 16 bits allow to store number up to `65536` - that is a maximum number of requests/tokens stored by rate limiter state. 
 - Low 48 bits: timestamp of last update in Unix milliseconds. Millisecond precision packed in 48 bites allows time interval up to `8925 years`

```
64 bits: [ 16-bit tokens ][ 48-bit timestamp in ms ]
```

Limiter state is updated atomically using CAS operations. 
Refill logic is based on elapsed time since the last update and configured refill rate.

## 4. Usage

### 4.1. Import

```go
import "github.com/iryndin/limitron"
```

### 4.2. Example: Per-Second Rate Limit

```go
limiter := limitron.BuildRateLimiterRps(10) // 10 requests per second
state := limiter.New()

if waitMillis, taken := limiter.Take1(state); taken {
    // Allowed – process request
} else {
    // Denied – rate limit exceeded
    time.Sleep(time.Duration(waitMillis) * time.Millisecond)
}
```


### 4.3. Example: Custom Interval Rate Limit

```go
limiter := limitron.BuildRateLimiter(100, time.Minute) // 100 reqs per minute
state := limiter.New()

_, taken := limiter.TakeN(state, 5) // Try to take 5 requests at once
if taken {
    // Allowed
} else {
    // Rate limit hit
}
```

### 4.4. Example: Free and Paid plan rate limiting

```go
package limitronexample

import (
    "github.com/iryndin/limitron"
    "net/http"
    "strconv"
    "time"
)

var freeRateLimiter = limitron.BuildRateLimiter(1, 5 * time.Second)  // 1 request in 5 seconds - limit for FREE users
var paidRateLimiter = limitron.BuildRateLimiterRps(10)               // 10 rps - limit for PAID users

var userRateLimitMap = make(map[string]*uint64, 10)

func apiHandler(w http.ResponseWriter, r *http.Request) {
    apiToken := r.Header.Get("Authorization")
    user, err := getUserByApiToken(apiToken)
    if err != nil {
        w.WriteHeader(http.StatusUnauthorized)
        return
    }

    rateLimiter := &freeRateLimiter
    if user.IsPaidUser {
        rateLimiter = &paidRateLimiter
    }

    rl, ok := userRateLimitMap[apiToken]

    if !ok {
        rl := rateLimiter.New()
        userRateLimitMap[apiToken] = rl
    }

    if waitMillis, taken := rateLimiter.Take1(rl); taken {
        // hande API call normally
    } else {
        waitSeconds := time.Duration(waitMillis) + 500*time.Millisecond
        if waitSeconds < time.Second {
            waitSeconds = time.Second
        }
        w.Header().Set("Retry-After", strconv.Itoa(int(waitSeconds.Seconds())))
        http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
        return
    }
}
```

## 5. Internals

**Limitron** represents rate limiter state using a compact 64-bit integer:

```
64 bits: [ 16-bit tokens ][ 48-bit timestamp in ms ]
```

Refill logic:
* At each call, it calculates tokens based on now - last_timestamp and a precomputed tokens/ms rate.
* Capped by a burst size (maxreq).

CAS loop with configurable retries ensures safe concurrent mutation of shared limiter state.


## 6. Best Practices

* Store `rl *uint64` values in maps keyed by user/IP/key.
* Use separate `RateLimiter` instances for each configuration (they are stateless). E.g. create one instance of `RateLimiter` for free plan users, and another `RateLimiter` instance for paid plan users with higher rate.
* Use of `rl *uint64` values makes sense only by reference (pointer)