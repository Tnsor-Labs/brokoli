package engine

import (
	"log"
	"sync"
)

// fallbackLogf is where a degradation is announced. A variable so a test
// can capture the line and prove the announcement happens (#529).
var fallbackLogf = log.Printf

// announcedFallbacks holds the keys already logged, so a fallback taken on
// every run is announced once per process rather than flooding the log.
var announcedFallbacks sync.Map

// announceFallbackOnce logs a degradation the first time key is seen. A
// fallback whose result is still correct but weaker must say so, or a
// mistake keeps working and nobody looks (#529). The key should name what
// is missing, such as the store type, so each distinct cause is announced.
func announceFallbackOnce(key, format string, args ...interface{}) {
	if _, seen := announcedFallbacks.LoadOrStore(key, struct{}{}); seen {
		return
	}
	fallbackLogf(format, args...)
}
