package relay

import "errors"

// Constraint errors, with the original assertion messages.
var (
	ErrLifetime = errors.New("Relay has hit lifetime duration limit!")
	ErrTraffic  = errors.New("Relay has hit lifetime traffic limit!")
)

// Block reasons, also the label values of noray_relay_blocked_count.
const (
	ReasonIndividualBandwidth = iota
	ReasonGlobalBandwidth
	ReasonLifetimeDuration
	ReasonLifetimeTraffic
)

// ReasonNames are the label values for the reasons above.
var ReasonNames = []string{"individual_bandwidth", "global_bandwidth", "lifetime_duration", "lifetime_traffic"}

// Constraint inspects every packet before it is transmitted from the source
// relay to the target relay (the original's 'transmit' listeners). A non-nil
// error blocks the packet. In the original, that error was thrown out of the
// UDP message handler and killed the process; here the packet is dropped.
type Constraint interface {
	Check(source, target *Entry, size int, now int64) error
	Reason() int
}

// IndividualBandwidth limits each relay's outgoing traffic (keyed by the
// source relay) to traffic bytes/sec, measured in interval-second slices.
type IndividualBandwidth struct {
	Traffic, Interval float64
	Now               func() float64
}

func (c *IndividualBandwidth) Reason() int { return ReasonIndividualBandwidth }
func (c *IndividualBandwidth) Check(source, _ *Entry, size int, _ int64) error {
	l := source.limiter.Load()
	if l == nil {
		l = NewBandwidthLimiter(c.Traffic, c.Interval, c.Now)
		if !source.limiter.CompareAndSwap(nil, l) {
			l = source.limiter.Load()
		}
	}
	return l.Validate(size)
}

// GlobalBandwidth limits the sum of all relayed traffic.
type GlobalBandwidth struct{ limiter *BandwidthLimiter }

// NewGlobalBandwidth creates the global limiter.
func NewGlobalBandwidth(traffic, interval float64, now func() float64) *GlobalBandwidth {
	return &GlobalBandwidth{limiter: NewBandwidthLimiter(traffic, interval, now)}
}

func (c *GlobalBandwidth) Reason() int { return ReasonGlobalBandwidth }
func (c *GlobalBandwidth) Check(_, _ *Entry, size int, _ int64) error {
	return c.limiter.Validate(size)
}

// Lifetime blocks traffic from relays older than Duration seconds.
type Lifetime struct{ Duration float64 }

func (c *Lifetime) Reason() int { return ReasonLifetimeDuration }
func (c *Lifetime) Check(source, _ *Entry, _ int, now int64) error {
	if float64(now-source.Created())/1e9 < c.Duration {
		return nil
	}
	return ErrLifetime
}

// Traffic blocks relays after they sent Traffic bytes in total. Like the
// original, the counter grows before the check (also for blocked packets).
type Traffic struct{ Traffic float64 }

func (c *Traffic) Reason() int { return ReasonLifetimeTraffic }
func (c *Traffic) Check(source, _ *Entry, size int, _ int64) error {
	if float64(source.traffic.Add(int64(size))) < c.Traffic {
		return nil
	}
	return ErrTraffic
}
