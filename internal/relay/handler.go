package relay

import (
	"net/netip"
	"sync"
	"time"

	"github.com/blankeos/noray-go/internal/logger"
	"github.com/blankeos/noray-go/internal/metrics"
)

// Metrics are the relay metrics, named like the original's prom-client ones.
type Metrics struct {
	Duration *metrics.Histogram  // noray_relay_duration (seconds)
	Size     *metrics.Histogram  // noray_relay_size (bytes, default buckets like the original)
	Drop     *metrics.Counter    // noray_relay_drop_count
	Count    *metrics.Gauge      // noray_relay_count
	Expired  *metrics.Counter    // noray_relay_expired
	Blocked  *metrics.CounterVec // noray_relay_blocked_count{reason} (new)
	SendErr  *metrics.Counter    // noray_relay_send_error_count (new)
}

// NewMetrics registers the relay metrics on r.
func NewMetrics(r *metrics.Registry) *Metrics {
	return &Metrics{
		Duration: r.NewHistogram("noray_relay_duration", "Time it takes to relay a packet", metrics.DefBuckets),
		Size:     r.NewHistogram("noray_relay_size", "Size of the packet being relayed", metrics.DefBuckets),
		Drop:     r.NewCounter("noray_relay_drop_count", "Number of relay packets dropped"),
		Count:    r.NewGauge("noray_relay_count", "Count of currently active relays"),
		Expired:  r.NewCounter("noray_relay_expired", "Count of expired relays"),
		Blocked: r.NewCounterVec("noray_relay_blocked_count",
			"Number of relay packets blocked by a relay constraint (the original crashed instead)",
			"reason", ReasonNames...),
		SendErr: r.NewCounter("noray_relay_send_error_count", "Number of relay packets that failed to send"),
	}
}

// Handler is the relay table plus forwarding logic (UDPRelayHandler).
//
// Traffic arriving on port P from sender S is forwarded when both S has a
// relay (its own port) and P belongs to a relay: it leaves through S's relay
// port towards P's owner. So a client always appears as noray:<client relay
// port> to the host and vice versa.
type Handler struct {
	mu     sync.RWMutex
	byAddr map[netip.AddrPort]*Entry
	byPort []*Entry
	table  []*Entry // creation order

	pool        *SocketPool
	constraints []Constraint
	dynamic     bool
	m           *Metrics
	log         *logger.Logger
	now         func() int64
}

// NewHandler creates a relay handler over pool and installs itself as the
// pool's packet handler.
func NewHandler(pool *SocketPool, m *Metrics, log *logger.Logger) *Handler {
	if m == nil {
		m = NewMetrics(&metrics.Registry{})
	}
	if log == nil {
		log = logger.Nop()
	}
	h := &Handler{
		byAddr: map[netip.AddrPort]*Entry{},
		byPort: make([]*Entry, 65536),
		pool:   pool,
		m:      m,
		log:    log,
		now:    Nanos,
	}
	pool.SetHandler(func(msg []byte, from netip.AddrPort, port int) { h.Relay(msg, from, port) })
	return h
}

// Pool returns the socket pool.
func (h *Handler) Pool() *SocketPool { return h.pool }

// AddConstraint appends a constraint; they run in insertion order and the
// first failure blocks the packet (later ones are not consulted).
func (h *Handler) AddConstraint(c Constraint) { h.constraints = append(h.constraints, c) }

// EnableDynamicRelaying turns on dynamic relay creation: an unknown sender
// that sends to an existing relay port gets a relay of its own, and the
// packet is forwarded.
func (h *Handler) EnableDynamicRelaying() { h.dynamic = true }

// CreateRelay returns the relay for addr, creating one on a free port if
// there is none. created is false if it already existed.
func (h *Handler) CreateRelay(addr netip.AddrPort) (e *Entry, created bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.byAddr[addr]; e != nil {
		return e, false, nil
	}
	port, err := h.pool.GetPort()
	if err != nil {
		return nil, false, err
	}
	sock := h.pool.GetSocket(port)
	e = &Entry{Address: addr, Port: port, sock: sock}
	now := h.now()
	e.created.Store(now)
	e.lastReceived.Store(now)
	h.byAddr[addr] = e
	h.byPort[port] = e
	h.table = append(h.table, e)
	if sock != nil {
		sock.listening.Store(true)
	}
	h.m.Count.Inc()
	return e, true, nil
}

// HasRelay reports whether addr has a relay (ports are not compared).
func (h *Handler) HasRelay(addr netip.AddrPort) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byAddr[addr] != nil
}

// FreeRelay removes the relay for e's address and returns its port to the
// pool. False if there was no such relay.
func (h *Handler) FreeRelay(e *Entry) bool { return h.free(e, false) }

func (h *Handler) free(e *Entry, exact bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.byAddr[e.Address]
	if cur == nil || (exact && cur != e) {
		return false
	}
	delete(h.byAddr, cur.Address)
	if h.byPort[cur.Port] == cur {
		h.byPort[cur.Port] = nil
	}
	for i, t := range h.table {
		if t == cur {
			h.table = append(h.table[:i], h.table[i+1:]...)
			break
		}
	}
	h.pool.ReturnPort(cur.Port)
	h.m.Count.Dec()
	return true
}

// Clear frees every relay.
func (h *Handler) Clear() {
	for _, e := range h.Table() {
		h.FreeRelay(e)
	}
	h.m.Count.Set(0)
}

// Table returns a copy of the relay table in creation order.
func (h *Handler) Table() []*Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]*Entry(nil), h.table...)
}

// Len is the number of relays.
func (h *Handler) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.table)
}

// Relay forwards msg from sender to the relay owning targetPort. It returns
// true if the packet was sent. It does not allocate.
func (h *Handler) Relay(msg []byte, sender netip.AddrPort, targetPort int) bool {
	start := time.Now()
	h.mu.RLock()
	src := h.byAddr[sender]
	var dst *Entry
	if targetPort >= 0 && targetPort < len(h.byPort) {
		dst = h.byPort[targetPort]
	}
	h.mu.RUnlock()

	if src == nil || dst == nil {
		h.m.Drop.Inc()
		h.m.Duration.Observe(time.Since(start).Seconds())
		if h.dynamic && src == nil && dst != nil {
			return h.dynamicRelay(msg, sender, targetPort, dst)
		}
		return false
	}
	if src.sock == nil {
		return false
	}

	now := h.now()
	for _, c := range h.constraints {
		if err := c.Check(src, dst, len(msg), now); err != nil {
			h.m.Blocked.Inc(c.Reason())
			if h.log.Enabled(logger.Trace) {
				h.log.Trace(logger.F{"relay": src.ID(), "err": err.Error()}, "Blocked relay packet")
			}
			return false
		}
	}

	if err := src.sock.WriteTo(msg, dst.Address); err != nil {
		h.m.SendErr.Inc()
		return false
	}
	src.lastReceived.Store(now)
	dst.lastSent.Store(now)

	h.m.Size.Observe(float64(len(msg)))
	h.m.Duration.Observe(time.Since(start).Seconds())
	return true
}

func (h *Handler) dynamicRelay(msg []byte, sender netip.AddrPort, targetPort int, dst *Entry) bool {
	h.log.Info(logger.F{"from": sender.String(), "to": dst.Address.String()}, "Creating dynamic relay")
	e, _, err := h.CreateRelay(sender)
	if err != nil {
		h.log.Warn(logger.F{"from": sender.String(), "err": err.Error()}, "Failed to create dynamic relay")
		return false
	}
	h.log.Info(logger.F{"relay": e.ID()}, "Relay created, sending %d packets", 1)
	return h.Relay(msg, sender, targetPort)
}

// Cleanup frees relays without traffic in either direction for timeout
// seconds (cleanupUdpRelayTable) and returns how many were freed.
func (h *Handler) Cleanup(timeout float64) int {
	cutoff := h.now() - int64(timeout*1e9)
	freed := 0
	for _, e := range h.Table() {
		last := max(e.LastSent(), e.LastReceived())
		if last <= cutoff {
			if h.free(e, true) {
				h.m.Expired.Inc()
				freed++
			}
		}
	}
	return freed
}
