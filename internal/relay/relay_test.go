package relay

import (
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// Ported from noray test/spec/relay/*.test.mjs.

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

// --- BandwidthLimiter (bandwidth.limiter.test.mjs) ---

func TestLimiterPass(t *testing.T) {
	l := NewBandwidthLimiter(16, 0, nil)
	l.Validate(8)
	if err := l.Validate(8); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterPassAfterInterval(t *testing.T) {
	l := NewBandwidthLimiter(160, 0.1, nil)
	l.Validate(16)
	time.Sleep(150 * time.Millisecond)
	if err := l.Validate(8); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterThrow(t *testing.T) {
	l := NewBandwidthLimiter(16, 0, nil)
	l.Validate(12)
	if err := l.Validate(8); err != ErrBandwidth {
		t.Fatal("should reject")
	}
}

func TestLimiterSliceSemantics(t *testing.T) {
	now := 0.0
	clock := func() float64 { return now }
	// 128kb/s in 100ms slices = 13107.2 bytes per slice
	l := NewBandwidthLimiter(131072, 0.1, clock)
	if l.Validate(13107) != nil || l.Validate(1) != ErrBandwidth {
		t.Fatal("slice limit")
	}
	now = 0.1 // not past the slice yet (strictly greater is required)
	if l.Validate(1) != ErrBandwidth {
		t.Fatal("still the same slice")
	}
	now = 0.2
	// first packet of a new slice always passes, even if oversized
	if l.Validate(50000) != nil || l.Validate(1) != ErrBandwidth {
		t.Fatal("new slice")
	}
}

// --- constraints.test.mjs ---

func pair() (*Entry, *Entry) {
	return NewEntry(ap("37.89.0.5:32467"), 10001), NewEntry(ap("57.13.0.9:45357"), 10002)
}

func TestIndividualBandwidth(t *testing.T) {
	a, b := pair()
	c := &IndividualBandwidth{Traffic: 16, Interval: 1}
	if c.Check(a, b, 16, Nanos()) != nil {
		t.Fatal("should pass")
	}
	a, b = pair()
	if c.Check(a, b, 32, Nanos()) == nil {
		t.Fatal("should throw on too much data")
	}
	// limits are per source relay
	if c.Check(b, a, 16, Nanos()) != nil {
		t.Fatal("other relay has its own budget")
	}
}

func TestGlobalBandwidth(t *testing.T) {
	a, b := pair()
	c := NewGlobalBandwidth(16, 1, nil)
	if c.Check(a, b, 4, 0) != nil || c.Check(b, a, 4, 0) != nil {
		t.Fatal("should pass")
	}
	c = NewGlobalBandwidth(16, 1, nil)
	if c.Check(a, b, 12, 0) != nil || c.Check(b, a, 12, 0) == nil {
		t.Fatal("should throw")
	}
}

func TestLifetime(t *testing.T) {
	a, b := pair()
	c := &Lifetime{Duration: 4}
	if c.Check(a, b, 4, Nanos()) != nil {
		t.Fatal("should pass")
	}
	a.SetCreated(Nanos() - int64(16*time.Second))
	if c.Check(a, b, 4, Nanos()) != ErrLifetime {
		t.Fatal("should throw")
	}
}

func TestTraffic(t *testing.T) {
	a, b := pair()
	c := &Traffic{Traffic: 16}
	if c.Check(a, b, 4, 0) != nil || c.Check(b, a, 4, 0) != nil {
		t.Fatal("should pass")
	}
	a, b = pair()
	if c.Check(a, b, 12, 0) != nil || c.Check(b, a, 12, 0) != nil || c.Check(b, a, 12, 0) != ErrTraffic {
		t.Fatal("should throw")
	}
}

// --- relay.entry.test.mjs ---

func TestEntryEquals(t *testing.T) {
	cases := []struct {
		a, b *Entry
		want bool
	}{
		{NewEntry(ap("10.0.0.1:1000"), 2000), NewEntry(ap("10.0.0.1:1000"), 2000), true},
		{NewEntry(ap("10.0.0.1:1000"), 2000), NewEntry(ap("10.0.0.1:1000"), 2010), true},
		{NewEntry(ap("10.0.0.1:1000"), 2000), NewEntry(ap("10.0.0.2:1000"), 2000), false},
		{NewEntry(ap("10.0.0.1:1000"), 2000), NewEntry(ap("10.0.0.1:1020"), 2000), false},
	}
	for i, c := range cases {
		if c.a.Equals(c.b) != c.want {
			t.Errorf("case %d", i)
		}
	}
	if id := NewEntry(ap("10.0.0.1:1000"), 2000).ID(); id != "10.0.0.1:1000@2000" {
		t.Error(id)
	}
}

// --- udp.socket.pool.test.mjs ---

func TestPoolAllocate(t *testing.T) {
	p := NewSocketPool()
	defer p.Clear()
	port, err := p.AllocatePort(0)
	if err != nil || port == 0 {
		t.Fatal(err)
	}
	if got := p.Ports(); len(got) != 1 || got[0] != port || p.GetSocket(port) == nil {
		t.Fatal("should save port")
	}
	if _, err := p.AllocatePort(port); err == nil {
		t.Fatal("binding a taken port should fail")
	}
}

func TestPoolDeallocate(t *testing.T) {
	p := NewSocketPool()
	port, _ := p.AllocatePort(0)
	p.DeallocatePort(port + 1) // should ignore unknown port
	if len(p.Ports()) != 1 {
		t.Fatal("unknown port removed something")
	}
	p.DeallocatePort(port)
	if len(p.Ports()) != 0 || p.HasFreePort() {
		t.Fatal("should close")
	}
	// the port is really released
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	p.Clear()
}

func TestPoolGetReturnPort(t *testing.T) {
	p := NewSocketPool()
	defer p.Clear()
	if _, err := p.GetPort(); err != ErrNoFreePorts || p.HasFreePort() {
		t.Fatal("should throw if none available")
	}
	expected, _ := p.AllocatePort(0)
	got, err := p.GetPort()
	if err != nil || got != expected || p.HasFreePort() {
		t.Fatal("should return allocated")
	}
	p.ReturnPort(got)
	if !p.HasFreePort() {
		t.Fatal("should make port available")
	}
	p.ReturnPort(65575) // should ignore unknown
	if p.FreeCount() != 1 {
		t.Fatal("unknown port returned")
	}
}

func TestPoolLIFO(t *testing.T) {
	p := NewSocketPool()
	defer p.Clear()
	a, _ := p.AllocatePort(0)
	b, _ := p.AllocatePort(0)
	if got, _ := p.GetPort(); got != b {
		t.Fatal("pop takes the last added port")
	}
	if got, _ := p.GetPort(); got != a {
		t.Fatal("then the previous one")
	}
}

// --- udp.relay.handler.test.mjs ---

func newHandler(t *testing.T, ports int) *Handler {
	p := NewSocketPool()
	for i := 0; i < ports; i++ {
		if _, err := p.AllocatePort(0); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(p.Clear)
	return NewHandler(p, nil, nil)
}

func TestCreateRelay(t *testing.T) {
	h := newHandler(t, 1)
	e, created, err := h.CreateRelay(ap("88.57.0.107:32279"))
	if err != nil || !created || e.Port == 0 {
		t.Fatal("should create relay")
	}
	if tbl := h.Table(); len(tbl) != 1 || tbl[0] != e {
		t.Fatal("table")
	}
	again, created, _ := h.CreateRelay(ap("88.57.0.107:32279"))
	if created || again != e || h.Len() != 1 {
		t.Fatal("should ignore if relay exists")
	}
	if _, _, err := h.CreateRelay(ap("88.57.0.108:1")); err != ErrNoFreePorts {
		t.Fatal("pool exhausted")
	}
	if h.m.Count.Value() != 1 {
		t.Fatal("gauge")
	}
}

func TestFreeRelay(t *testing.T) {
	h := newHandler(t, 1)
	e, _, _ := h.CreateRelay(ap("88.57.0.107:32279"))
	if h.FreeRelay(NewEntry(ap("89.45.0.109:32279"), 0)) || h.Len() != 1 {
		t.Fatal("should ignore unknown")
	}
	if !h.FreeRelay(e) || h.Len() != 0 || !h.Pool().HasFreePort() {
		t.Fatal("should free relay and return the port")
	}
	if h.m.Count.Value() != 0 {
		t.Fatal("gauge")
	}
}

func TestHasRelay(t *testing.T) {
	h := newHandler(t, 2)
	h.CreateRelay(ap("88.57.0.107:32279"))
	if h.HasRelay(ap("88.57.0.107:49152")) {
		t.Fatal("should not have relay")
	}
	if !h.HasRelay(ap("88.57.0.107:32279")) {
		t.Fatal("should have relay")
	}
}

func udp(t *testing.T) *net.UDPConn {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func addrOf(c *net.UDPConn) netip.AddrPort { return c.LocalAddr().(*net.UDPAddr).AddrPort() }

func recv(t *testing.T, c *net.UDPConn) (string, netip.AddrPort) {
	c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 2048)
	n, from, err := c.ReadFromUDPAddrPort(buf)
	if err != nil {
		return "", netip.AddrPort{}
	}
	return string(buf[:n]), from
}

func TestRelay(t *testing.T) {
	h := newHandler(t, 2)
	sender, receiver := udp(t), udp(t)
	sRelay, _, _ := h.CreateRelay(addrOf(sender))
	rRelay, _, _ := h.CreateRelay(addrOf(receiver))

	// sending to the receiver's relay port arrives from the sender's relay port
	sender.WriteToUDPAddrPort([]byte("Hello!"), netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(rRelay.Port)))
	msg, from := recv(t, receiver)
	if msg != "Hello!" || int(from.Port()) != sRelay.Port {
		t.Fatalf("got %q from %v", msg, from)
	}
	if rRelay.LastSent() == 0 || sRelay.LastReceived() == 0 {
		t.Fatal("timings")
	}
	if h.m.Size.Count() != 1 {
		t.Fatal("size histogram")
	}
}

func TestRelayIgnoreUnknownAddress(t *testing.T) {
	h := newHandler(t, 1)
	e, _, _ := h.CreateRelay(ap("88.57.0.17:32279"))
	if h.Relay([]byte("Hello!"), ap("88.59.62.107:65227"), e.Port) {
		t.Fatal("relay succeeded?")
	}
	if h.m.Drop.Value() != 1 {
		t.Fatal("drop not counted")
	}
}

func TestRelayBlockedByConstraintDoesNotCrash(t *testing.T) {
	h := newHandler(t, 2)
	h.AddConstraint(&IndividualBandwidth{Traffic: 10, Interval: 1})
	sender, receiver := udp(t), udp(t)
	h.CreateRelay(addrOf(sender))
	r, _, _ := h.CreateRelay(addrOf(receiver))
	if h.Relay(make([]byte, 100), addrOf(sender), r.Port) {
		t.Fatal("should be blocked")
	}
	if h.m.Blocked.Value(ReasonIndividualBandwidth) != 1 {
		t.Fatal("blocked not counted")
	}
}

func TestRelayHotPathAllocations(t *testing.T) {
	h := newHandler(t, 2)
	h.AddConstraint(&IndividualBandwidth{Traffic: 1 << 40, Interval: 0.1})
	h.AddConstraint(NewGlobalBandwidth(1<<40, 0.1, nil))
	h.AddConstraint(&Lifetime{Duration: 3600})
	h.AddConstraint(&Traffic{Traffic: 1 << 50})
	sender, receiver := udp(t), udp(t)
	h.CreateRelay(addrOf(sender))
	r, _, _ := h.CreateRelay(addrOf(receiver))
	msg := make([]byte, 120)
	from := addrOf(sender)
	h.Relay(msg, from, r.Port)
	allocs := testing.AllocsPerRun(200, func() { h.Relay(msg, from, r.Port) })
	if allocs != 0 {
		t.Fatalf("Relay allocates %v per packet", allocs)
	}
}

// --- udp.relay.cleanup.test.mjs ---

func TestCleanup(t *testing.T) {
	h := newHandler(t, 4)
	origin := Nanos()
	expired := origin - int64(3*time.Second)
	mk := func(a string, recv, sent int64) *Entry {
		e, _, _ := h.CreateRelay(ap(a))
		e.SetLastReceived(recv)
		e.SetLastSent(sent)
		return e
	}
	fresh := mk("88.57.0.2:32276", origin, origin)
	oldSend := mk("88.57.0.3:32276", origin, expired)
	oldRecv := mk("88.57.0.5:32276", expired, origin)
	gone := mk("88.57.0.4:32276", expired, expired)
	if n := h.Cleanup(2); n != 1 {
		t.Fatalf("freed %d", n)
	}
	for _, e := range []*Entry{fresh, oldSend, oldRecv} {
		if !h.HasRelay(e.Address) {
			t.Fatal("active relay was freed")
		}
	}
	if h.HasRelay(gone.Address) || h.m.Expired.Value() != 1 {
		t.Fatal("expired relay was not freed")
	}
}

// --- dynamic.relaying.test.mjs ---

func TestDynamicRelaying(t *testing.T) {
	h := newHandler(t, 3)
	h.EnableDynamicRelaying()
	host, stranger := udp(t), udp(t)
	hostRelay, _, _ := h.CreateRelay(addrOf(host))

	for _, m := range []string{"hello", "world", "use", "noray"} {
		h.Relay([]byte(m), addrOf(stranger), hostRelay.Port)
		got, from := recv(t, host)
		if got != m {
			t.Fatalf("message %q was not sent", m)
		}
		if int(from.Port()) == hostRelay.Port || from.Port() == 0 {
			t.Fatal("should arrive from the new relay")
		}
	}
	if h.Len() != 2 || !h.HasRelay(addrOf(stranger)) {
		t.Fatal("relay was not created exactly once")
	}
	// the original leaked a pool port per dynamic relay; this does not
	if h.Pool().FreeCount() != 1 {
		t.Fatalf("free ports %d", h.Pool().FreeCount())
	}
}

func TestDynamicRelayingIgnores(t *testing.T) {
	h := newHandler(t, 2)
	h.EnableDynamicRelaying()
	known := udp(t)
	h.CreateRelay(addrOf(known))
	// known sender, unknown target
	if h.Relay([]byte("x"), addrOf(known), 1) || h.Len() != 1 {
		t.Fatal("should ignore known sender")
	}
	// unknown sender, unknown target
	if h.Relay([]byte("x"), ap("87.54.0.16:16752"), 1) || h.Len() != 1 {
		t.Fatal("should ignore unknown target")
	}
}

// --- udp.remote.registrar.test.mjs ---

type fakeHosts struct {
	mu   sync.Mutex
	pids map[string]*netip.AddrPort
}

func (f *fakeHosts) get(pid string) netip.AddrPort {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.pids[pid]
}

func (f *fakeHosts) RegisterRemote(pid string, addr netip.AddrPort) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.pids[pid]
	if !ok {
		return false, false
	}
	if cur.IsValid() {
		return true, true
	}
	*cur = addr
	return false, true
}

func TestRegistrar(t *testing.T) {
	addr := netip.AddrPort{}
	hosts := &fakeHosts{pids: map[string]*netip.AddrPort{"p0001": &addr}}
	r := NewRegistrar(hosts, nil, nil)
	if err := r.Listen("127.0.0.1", 0); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := udp(t)
	to := r.Addr().AddrPort()

	c.WriteToUDPAddrPort([]byte("p0001"), to)
	if msg, _ := recv(t, c); msg != "OK" || hosts.get("p0001") != addrOf(c) {
		t.Fatalf("should succeed: %q %v", msg, addr)
	}
	other := udp(t)
	other.WriteToUDPAddrPort([]byte("p0001"), to)
	if msg, _ := recv(t, other); msg != "OK" || hosts.get("p0001") != addrOf(c) {
		t.Fatal("repeat should reply OK and keep the first address")
	}
	c.WriteToUDPAddrPort([]byte("nope"), to)
	if msg, _ := recv(t, c); msg != "Unknown host pid!" {
		t.Fatalf("should fail on unknown pid: %q", msg)
	}
	if r.m.Success.Value() != 1 || r.m.Repeat.Value() != 1 || r.m.Fail.Value() != 1 {
		t.Fatal("counters")
	}
}
