package relay

import (
	"errors"
	"net"
	"net/netip"

	"github.com/blankeos/noray-go/internal/logger"
	"github.com/blankeos/noray-go/internal/metrics"
)

// ErrUnknownPID is the reply for an unregistered pid.
var ErrUnknownPID = errors.New("Unknown host pid!")

// RemoteRegistry is what the registrar needs from the host repository:
// record addr as the public address of the host with pid, unless one is
// already recorded (repeat=true). ok is false for an unknown pid.
type RemoteRegistry interface {
	RegisterRemote(pid string, addr netip.AddrPort) (repeat, ok bool)
}

// RegistrarMetrics are the registrar counters.
type RegistrarMetrics struct {
	Success, Fail, Repeat *metrics.Counter
}

// NewRegistrarMetrics registers the counters with the original names.
func NewRegistrarMetrics(r *metrics.Registry) *RegistrarMetrics {
	return &RegistrarMetrics{
		Success: r.NewCounter("noray_remote_registrar_success", "Number of successful remote address registrations"),
		Fail:    r.NewCounter("noray_remote_registrar_fail", "Number of failed remote address registrations"),
		Repeat:  r.NewCounter("noray_remote_registrar_repeat", "Number of redundant remote address registrations"),
	}
}

// Registrar is the UDP remote registrar: a datagram containing a pid
// registers the sender's address as that host's public address and is
// answered with "OK" (also when already registered, so clients can just
// repeat the request until they get a reply), or with the error message.
type Registrar struct {
	conn  *net.UDPConn
	hosts RemoteRegistry
	m     *RegistrarMetrics
	log   *logger.Logger
	done  chan struct{}
}

// NewRegistrar creates a registrar.
func NewRegistrar(hosts RemoteRegistry, m *RegistrarMetrics, log *logger.Logger) *Registrar {
	if m == nil {
		m = NewRegistrarMetrics(&metrics.Registry{})
	}
	if log == nil {
		log = logger.Nop()
	}
	return &Registrar{hosts: hosts, m: m, log: log, done: make(chan struct{})}
}

// Listen binds address:port (udp4) and serves in a goroutine.
func (r *Registrar) Listen(address string, port int) error {
	ip := net.ParseIP(address)
	if ip == nil {
		ip = net.IPv4zero
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		return err
	}
	r.conn = conn
	r.log.Info(nil, "Listening on %s", conn.LocalAddr().String())
	go r.serve()
	return nil
}

// Addr is the bound address.
func (r *Registrar) Addr() *net.UDPAddr { return r.conn.LocalAddr().(*net.UDPAddr) }

func (r *Registrar) serve() {
	defer close(r.done)
	buf := make([]byte, maxDatagram)
	for {
		n, from, err := r.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		r.Handle(buf[:n], from)
	}
}

// Handle processes one request and sends the reply.
func (r *Registrar) Handle(msg []byte, from netip.AddrPort) {
	pid := string(msg)
	if r.log.Enabled(logger.Debug) {
		r.log.Debug(logger.F{"pid": pid, "rinfo": from.String()}, "Received UDP relay request")
	}
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	repeat, ok := r.hosts.RegisterRemote(pid, from)
	reply := "OK"
	switch {
	case !ok:
		reply = ErrUnknownPID.Error()
		r.m.Fail.Inc()
	case repeat:
		r.m.Repeat.Inc()
	default:
		r.m.Success.Inc()
	}
	if _, err := r.conn.WriteToUDPAddrPort([]byte(reply), from); err != nil && ok {
		r.m.Fail.Inc()
	}
}

// Close stops the registrar.
func (r *Registrar) Close() {
	if r.conn != nil {
		r.conn.Close()
		<-r.done
	}
}
