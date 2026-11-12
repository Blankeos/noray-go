package server

import (
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/blankeos/noray-go/internal/logger"
)

const writeTimeout = 5 * time.Second

var tcpBufPool = sync.Pool{New: func() any { b := make([]byte, 4096); return &b }}

// goroutineTCP serves every connection on its own goroutine, parked in the
// runtime netpoller while idle; the read buffer is borrowed from a pool only
// while data is drained. Portable; used where epoll is not available.
type goroutineTCP struct {
	s     *Server
	ln    net.Listener
	mu    sync.Mutex
	conns map[*conn]struct{}
}

type conn struct {
	sess session
	c    *net.TCPConn
	rc   syscall.RawConn
	wmu  sync.Mutex
}

func listenGoroutine(s *Server, addr string) (tcpFrontend, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	g := &goroutineTCP{s: s, ln: ln, conns: map[*conn]struct{}{}}
	s.wg.Add(1)
	go g.acceptLoop()
	return g, nil
}

func (g *goroutineTCP) Addr() *net.TCPAddr { return g.ln.Addr().(*net.TCPAddr) }

func (g *goroutineTCP) Close() {
	g.ln.Close()
	g.mu.Lock()
	for c := range g.conns {
		c.c.Close()
	}
	g.mu.Unlock()
}

func (g *goroutineTCP) acceptLoop() {
	s := g.s
	defer s.wg.Done()
	for {
		nc, err := g.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Error(logger.F{"err": err.Error()}, "Listen socket encountered an error!")
			time.Sleep(10 * time.Millisecond)
			continue
		}
		tc := nc.(*net.TCPConn)
		rc, err := tc.SyscallConn()
		if err != nil {
			tc.Close()
			continue
		}
		c := &conn{c: tc, rc: rc}
		c.sess = session{s: s, out: c, remote: tc.RemoteAddr().String()}
		g.mu.Lock()
		g.conns[c] = struct{}{}
		g.mu.Unlock()
		s.wg.Add(1)
		go g.serve(c)
	}
}

func (g *goroutineTCP) serve(c *conn) {
	defer g.s.wg.Done()
	defer func() {
		c.c.Close()
		g.mu.Lock()
		delete(g.conns, c)
		g.mu.Unlock()
		c.sess.closed()
	}()
	closed := false
	for !closed {
		err := c.rc.Read(func(fd uintptr) bool {
			bp := tcpBufPool.Get().(*[]byte)
			defer tcpBufPool.Put(bp)
			for {
				n, err := syscall.Read(int(fd), *bp)
				if err == syscall.EINTR {
					continue
				}
				if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
					return false
				}
				if err != nil || n <= 0 {
					closed = true
					return true
				}
				c.sess.ingest((*bp)[:n])
			}
		})
		if err != nil {
			return
		}
	}
}

func (c *conn) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := c.c.Write(b)
	return err
}
