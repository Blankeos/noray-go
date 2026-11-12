package relay

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
)

// ErrNoFreePorts mirrors the original's assertion message.
var ErrNoFreePorts = errors.New("No more free ports!")

// maxDatagram is the largest UDP payload we can receive.
const maxDatagram = 65536

// PacketFunc handles one datagram received on a relay port. msg is only valid
// during the call.
type PacketFunc func(msg []byte, from netip.AddrPort, port int)

// Socket is one bound relay port.
type Socket struct {
	conn *net.UDPConn
	rc   syscall.RawConn
	port int
	// listening is set once the port has been handed to a relay. Before that,
	// datagrams are read and discarded, like a Node dgram socket without a
	// 'message' listener.
	listening atomic.Bool
	rx        recvState
}

// Port is the bound local port.
func (s *Socket) Port() int { return s.port }

// WriteTo sends a datagram from this socket. It does not allocate.
func (s *Socket) WriteTo(b []byte, to netip.AddrPort) error {
	_, err := s.conn.WriteToUDPAddrPort(b, to)
	return err
}

// SocketPool allocates and hands out UDP relay ports, like UDPSocketPool.
// Every socket has a goroutine parked in the runtime netpoller; a receive
// buffer is borrowed from a shared pool only while datagrams are being
// drained, so thousands of idle ports cost no buffer memory.
type SocketPool struct {
	mu      sync.Mutex
	sockets map[int]*Socket
	free    []int
	handler atomic.Pointer[PacketFunc]
	wg      sync.WaitGroup
}

var bufPool = sync.Pool{New: func() any { b := make([]byte, maxDatagram); return &b }}

// NewSocketPool creates an empty pool.
func NewSocketPool() *SocketPool {
	return &SocketPool{sockets: map[int]*Socket{}}
}

// SetHandler sets the function receiving datagrams from listening ports.
func (p *SocketPool) SetHandler(f PacketFunc) { p.handler.Store(&f) }

// AllocatePort binds 0.0.0.0:port (0 picks a free port) and adds it to the
// pool, returning the bound port.
func (p *SocketPool) AllocatePort(port int) (int, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return 0, err
	}
	rc, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return 0, err
	}
	s := &Socket{conn: conn, rc: rc, port: conn.LocalAddr().(*net.UDPAddr).Port}
	p.addSocket(s)
	p.wg.Add(1)
	go p.serve(s)
	return s.port, nil
}

func (p *SocketPool) addSocket(s *Socket) {
	p.mu.Lock()
	p.sockets[s.port] = s
	p.free = append(p.free, s.port)
	p.mu.Unlock()
}

func (p *SocketPool) serve(s *Socket) {
	defer p.wg.Done()
	for {
		err := s.rc.Read(func(fd uintptr) bool {
			var bp *[]byte
			for {
				if bp == nil {
					bp = bufPool.Get().(*[]byte)
				}
				n, from, err := recvFrom(int(fd), *bp, &s.rx)
				if err != nil {
					bufPool.Put(bp)
					if err == syscall.EINTR {
						bp = nil
						continue
					}
					// EAGAIN: park until readable. Other errors (e.g. ICMP
					// errors surfacing on the socket): return to retry.
					return err != syscall.EAGAIN && err != syscall.EWOULDBLOCK
				}
				if !s.listening.Load() {
					continue
				}
				if h := p.handler.Load(); h != nil {
					(*h)((*bp)[:n], from, s.port)
				}
			}
		})
		if err != nil {
			return // closed
		}
	}
}

// DeallocatePort closes the port's socket and forgets it.
func (p *SocketPool) DeallocatePort(port int) {
	p.mu.Lock()
	s := p.sockets[port]
	delete(p.sockets, port)
	for i, f := range p.free {
		if f == port {
			p.free = append(p.free[:i], p.free[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	if s != nil {
		s.conn.Close()
	}
}

// GetSocket returns the socket bound to port, or nil.
func (p *SocketPool) GetSocket(port int) *Socket {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sockets[port]
}

// GetPort takes a free port (the most recently freed or added one, like the
// original's Array.pop()).
func (p *SocketPool) GetPort() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.free) == 0 {
		return 0, ErrNoFreePorts
	}
	port := p.free[len(p.free)-1]
	p.free = p.free[:len(p.free)-1]
	return port, nil
}

// ReturnPort makes a port available again; unknown ports are ignored.
func (p *SocketPool) ReturnPort(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sockets[port]; ok {
		p.free = append(p.free, port)
	}
}

// HasFreePort reports whether GetPort would succeed.
func (p *SocketPool) HasFreePort() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free) > 0
}

// FreeCount is the number of free ports.
func (p *SocketPool) FreeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free)
}

// Ports lists the allocated ports.
func (p *SocketPool) Ports() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, 0, len(p.sockets))
	for port := range p.sockets {
		out = append(out, port)
	}
	return out
}

// Clear closes every socket and waits for the readers to stop.
func (p *SocketPool) Clear() {
	for _, port := range p.Ports() {
		p.DeallocatePort(port)
	}
	p.wg.Wait()
}
