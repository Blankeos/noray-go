package relay

import (
	"errors"
	"sync"
)

// ErrBandwidth is returned when a limiter's time slice is full.
var ErrBandwidth = errors.New("Bandwidth limit reached!")

// BandwidthLimiter mirrors the original BandwidthLimiter: time is cut into
// slices of `interval` seconds and each slice may carry maxTraffic*interval
// bytes (maxTraffic is in bytes per second). The first packet of a new slice
// always passes and starts the count at its own size; a packet that would
// overflow the current slice is rejected and not counted.
type BandwidthLimiter struct {
	mu           sync.Mutex
	interval     float64 // seconds
	maxTraffic   float64 // bytes/sec
	traffic      float64
	lastInterval float64 // seconds
	now          func() float64
}

// NewBandwidthLimiter creates a limiter; interval <= 0 means 1 second, like
// the original's `options.interval ?? 1` default.
func NewBandwidthLimiter(maxTraffic, interval float64, now func() float64) *BandwidthLimiter {
	if interval <= 0 {
		interval = 1
	}
	if now == nil {
		now = Seconds
	}
	return &BandwidthLimiter{interval: interval, maxTraffic: maxTraffic, lastInterval: now(), now: now}
}

// Validate accounts size bytes, or returns ErrBandwidth if they don't fit.
func (l *BandwidthLimiter) Validate(size int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	if t > l.lastInterval+l.interval {
		l.lastInterval = t
		l.traffic = float64(size)
		return nil
	}
	if l.traffic+float64(size) > l.maxTraffic*l.interval {
		return ErrBandwidth
	}
	l.traffic += float64(size)
	return nil
}
