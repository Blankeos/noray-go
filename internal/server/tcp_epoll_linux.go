//go:build linux

package server

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/blankeos/noray-go/internal/logger"
)

// listenTCP serves commands from a single epoll loop on Linux, like Node's
// event loop: an idle connection costs a file descriptor and a few hundred
// bytes, with no goroutine or buffer of its own. NORAY_GO_TCP_MODE=goroutine
// selects the portable goroutine-per-connection server instead.
func listenTCP(s *Server, addr string) (tcpFrontend, error) {
	if os.Getenv("NORAY_GO_TCP_MODE") == "goroutine" {
		return listenGoroutine(s, addr)
	}
	ta, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}
	ip := ta.IP.To4()
	if ta.IP == nil || ta.IP.IsUnspecified() {
		ip = net.IPv4zero.To4()
	}
	if ip == nil { // IPv6 listen address
		return listenGoroutine(s, addr)
	}
	return listenEpoll(s, [4]byte(ip), ta.Port)
}

const (
	maxPending   = 64 << 10 // buffered output per connection before it is dropped
	readsPerWake = 16
)

type epollTCP struct {
	s            *Server
	epfd, lfd    int
	wakeR, wakeW int
	addr         *net.TCPAddr
	conns        map[int]*econn // loop goroutine only
	toClose      []*econn
	buf          []byte
	closeOnce    sync.Once
	done         chan struct{}
}

type econn struct {
	t       *epollTCP
	fd      int
	sess    session
	pending []byte
	out     bool // EPOLLOUT armed
	dead    bool
}

func listenEpoll(s *Server, ip [4]byte, port int) (*epollTCP, error) {
	lfd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*epollTCP, error) { syscall.Close(lfd); return nil, os.NewSyscallError("listen", err) }
	syscall.SetsockoptInt(lfd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(lfd, &syscall.SockaddrInet4{Port: port, Addr: ip}); err != nil {
		return fail(err)
	}
	if err := syscall.Listen(lfd, 4096); err != nil {
		return fail(err)
	}
	sa, err := syscall.Getsockname(lfd)
	if err != nil {
		return fail(err)
	}
	sa4 := sa.(*syscall.SockaddrInet4)
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return fail(err)
	}
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		syscall.Close(epfd)
		return fail(err)
	}
	t := &epollTCP{
		s: s, epfd: epfd, lfd: lfd, wakeR: p[0], wakeW: p[1],
		addr:  &net.TCPAddr{IP: net.IP(sa4.Addr[:]), Port: sa4.Port},
		conns: map[int]*econn{}, buf: make([]byte, 64<<10), done: make(chan struct{}),
	}
	t.ctl(syscall.EPOLL_CTL_ADD, lfd, syscall.EPOLLIN)
	t.ctl(syscall.EPOLL_CTL_ADD, t.wakeR, syscall.EPOLLIN)
	s.wg.Add(1)
	go t.loop()
	return t, nil
}

func (t *epollTCP) ctl(op, fd int, events uint32) error {
	ev := syscall.EpollEvent{Events: events, Fd: int32(fd)}
	return syscall.EpollCtl(t.epfd, op, fd, &ev)
}

func (t *epollTCP) Addr() *net.TCPAddr { return t.addr }

func (t *epollTCP) Close() {
	t.closeOnce.Do(func() {
		syscall.Write(t.wakeW, []byte{1})
		<-t.done
	})
}

func (t *epollTCP) loop() {
	defer t.s.wg.Done()
	defer close(t.done)
	events := make([]syscall.EpollEvent, 256)
	for {
		n, err := syscall.EpollWait(t.epfd, events, -1)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			t.s.log.Error(logger.F{"err": err.Error()}, "epoll_wait failed")
			return
		}
		for i := 0; i < n; i++ {
			fd, ev := int(events[i].Fd), events[i].Events
			switch fd {
			case t.wakeR:
				t.shutdown()
				return
			case t.lfd:
				t.accept()
			default:
				c := t.conns[fd]
				if c == nil || c.dead {
					continue
				}
				if ev&syscall.EPOLLOUT != 0 {
					c.flush()
				}
				if ev&(syscall.EPOLLIN|syscall.EPOLLHUP|syscall.EPOLLERR|syscall.EPOLLRDHUP) != 0 {
					c.read()
				}
			}
		}
		t.reap()
	}
}

func (t *epollTCP) accept() {
	for {
		nfd, sa, err := syscall.Accept4(t.lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		if err != nil {
			switch err {
			case syscall.EAGAIN, syscall.EINTR, syscall.ECONNABORTED:
				return
			case syscall.EMFILE, syscall.ENFILE:
				// Out of descriptors: stop polling the listener for a moment
				// instead of spinning on it (level-triggered).
				t.s.log.Error(logger.F{"err": err.Error()}, "Listen socket encountered an error!")
				t.ctl(syscall.EPOLL_CTL_DEL, t.lfd, 0)
				time.AfterFunc(100*time.Millisecond, func() { t.ctl(syscall.EPOLL_CTL_ADD, t.lfd, syscall.EPOLLIN) })
				return
			}
			t.s.log.Error(logger.F{"err": err.Error()}, "Listen socket encountered an error!")
			return
		}
		syscall.SetsockoptInt(nfd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
		remote := ""
		if sa4, ok := sa.(*syscall.SockaddrInet4); ok {
			remote = netip.AddrPortFrom(netip.AddrFrom4(sa4.Addr), uint16(sa4.Port)).String()
		}
		c := &econn{t: t, fd: nfd}
		c.sess = session{s: t.s, out: c, remote: remote}
		if err := t.ctl(syscall.EPOLL_CTL_ADD, nfd, syscall.EPOLLIN|syscall.EPOLLRDHUP); err != nil {
			syscall.Close(nfd)
			continue
		}
		t.conns[nfd] = c
	}
}

func (c *econn) read() {
	t := c.t
	for i := 0; i < readsPerWake && !c.dead; i++ {
		n, err := syscall.Read(c.fd, t.buf)
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EAGAIN {
			return
		}
		if err != nil || n == 0 {
			c.kill()
			return
		}
		c.sess.ingest(t.buf[:n])
	}
}

// write is called by session handlers, always on the loop goroutine.
func (c *econn) write(b []byte) error {
	if c.dead {
		return net.ErrClosed
	}
	if len(c.pending) > 0 {
		return c.queue(b)
	}
	for len(b) > 0 {
		n, err := syscall.Write(c.fd, b)
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EAGAIN {
			return c.queue(b)
		}
		if err != nil {
			c.kill()
			return err
		}
		b = b[n:]
	}
	return nil
}

func (c *econn) queue(b []byte) error {
	if len(c.pending)+len(b) > maxPending {
		c.kill()
		return errors.New("client is not reading")
	}
	c.pending = append(c.pending, b...)
	if !c.out {
		c.out = true
		c.t.ctl(syscall.EPOLL_CTL_MOD, c.fd, syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLOUT)
	}
	return nil
}

func (c *econn) flush() {
	for len(c.pending) > 0 {
		n, err := syscall.Write(c.fd, c.pending)
		if err == syscall.EINTR {
			continue
		}
		if err == syscall.EAGAIN {
			return
		}
		if err != nil {
			c.kill()
			return
		}
		c.pending = c.pending[n:]
	}
	c.pending = nil
	c.out = false
	c.t.ctl(syscall.EPOLL_CTL_MOD, c.fd, syscall.EPOLLIN|syscall.EPOLLRDHUP)
}

// kill marks the connection dead; it is closed at the end of the event batch
// so its descriptor number cannot be reused while events for it are pending.
func (c *econn) kill() {
	if c.dead {
		return
	}
	c.dead = true
	c.t.toClose = append(c.t.toClose, c)
}

func (t *epollTCP) reap() {
	for len(t.toClose) > 0 {
		batch := t.toClose
		t.toClose = nil
		for _, c := range batch {
			delete(t.conns, c.fd)
			syscall.Close(c.fd) // also removes it from the epoll set
			c.sess.closed()
		}
	}
}

func (t *epollTCP) shutdown() {
	for _, c := range t.conns {
		c.kill()
	}
	t.reap()
	syscall.Close(t.lfd)
	syscall.Close(t.epfd)
	syscall.Close(t.wakeR)
	syscall.Close(t.wakeW)
}
