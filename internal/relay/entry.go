// Package relay implements Noray's UDP relays: the socket pool, the relay
// table and forwarding, the constraints, cleanup, dynamic relaying and the UDP
// remote registrar.
package relay

import (
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"
)

var base = time.Now()

// Nanos is a monotonic clock in nanoseconds (relative to process start).
func Nanos() int64 { return int64(time.Since(base)) }

// Seconds is Nanos in seconds.
func Seconds() float64 { return float64(Nanos()) / 1e9 }

// Entry is one row of the relay translation table: traffic arriving on Port is
// forwarded to whoever it is addressed to, and traffic from Address leaves
// through Port.
type Entry struct {
	// Port is the local relay port allocated for Address.
	Port int
	// Address is the remote (public) address this relay belongs to.
	Address netip.AddrPort

	created      atomic.Int64 // Nanos
	lastSent     atomic.Int64
	lastReceived atomic.Int64

	sock    *Socket
	limiter atomic.Pointer[BandwidthLimiter] // per-relay bandwidth constraint
	traffic atomic.Int64                     // lifetime traffic constraint
}

// NewEntry creates an entry (used by tests and the handler).
func NewEntry(addr netip.AddrPort, port int) *Entry {
	e := &Entry{Address: addr, Port: port}
	e.created.Store(Nanos())
	return e
}

// ID is the original's relay id: "<address>:<port>@<relay port>".
func (e *Entry) ID() string { return e.Address.String() + "@" + strconv.Itoa(e.Port) }

// Equals compares addresses only, like RelayEntry.equals.
func (e *Entry) Equals(o *Entry) bool { return e.Address == o.Address }

// Created, LastSent and LastReceived are Nanos timestamps.
func (e *Entry) Created() int64          { return e.created.Load() }
func (e *Entry) LastSent() int64         { return e.lastSent.Load() }
func (e *Entry) LastReceived() int64     { return e.lastReceived.Load() }
func (e *Entry) SetCreated(t int64)      { e.created.Store(t) }
func (e *Entry) SetLastSent(t int64)     { e.lastSent.Store(t) }
func (e *Entry) SetLastReceived(t int64) { e.lastReceived.Store(t) }
