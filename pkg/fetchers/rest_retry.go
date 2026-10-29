package fetchers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// statusError is a response outside 2xx. Its message is the one the
// fetcher has always given, and it wraps ErrHTTPRequestFailed.
type statusError struct {
	code int
	// retryAfter is the server's Retry-After, zero when it sent none.
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: status code %d", ErrHTTPRequestFailed, e.code)
}

func (e *statusError) Unwrap() error { return ErrHTTPRequestFailed }

// maxRetryAfter caps how long a page retry honours a server's
// Retry-After. A server may ask for hours; the run would rather fail
// and say why than sit silent that long.
const maxRetryAfter = 5 * time.Minute

// pageRetryable reports whether a failed page fetch can succeed if it is
// tried again. Transport errors, timeouts and rate limits (408, 425, 429)
// and server errors (5xx) can. Any other status (400, 401, 403, 404...)
// and a refused or malformed target come back the same on every try, and
// retrying them only delays the failure and adds load on the server.
func pageRetryable(err error) bool {
	if errors.Is(err, ErrSSRFBlocked) || errors.Is(err, ErrInvalidURL) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		switch {
		case se.code == http.StatusRequestTimeout, se.code == http.StatusTooEarly,
			se.code == http.StatusTooManyRequests, se.code >= 500:
			return true
		default:
			return false
		}
	}
	return true
}

// pageRetryWait is the pause before retry attempt (0-indexed): the
// configured backoff, or the server's Retry-After when that is longer.
func pageRetryWait(backoff string, attempt int, err error) time.Duration {
	wait := pageRetryDelay(backoff, attempt)
	var se *statusError
	if errors.As(err, &se) && se.retryAfter > wait {
		wait = min(se.retryAfter, maxRetryAfter)
	}
	return wait
}

// parseRetryAfter reads a Retry-After header: delay-seconds or an
// HTTP-date (RFC 9110 section 10.2.3). Anything else is zero.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
