// Package server wires Noray together: the TCP command server, the UDP
// remote registrar, the relays and the metrics endpoint.
package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/blankeos/noray-go/internal/config"
	"github.com/blankeos/noray-go/internal/hosts"
	"github.com/blankeos/noray-go/internal/ids"
	"github.com/blankeos/noray-go/internal/logger"
	"github.com/blankeos/noray-go/internal/metrics"
	"github.com/blankeos/noray-go/internal/relay"
)

// Server is a running Noray instance.
type Server struct {
	cfg *config.Config
	log *logger.Logger
	reg *metrics.Registry

	Hosts     *hosts.Repository
	Relays    *relay.Handler
	Pool      *relay.SocketPool
	Registrar *relay.Registrar

	activeHosts *metrics.Gauge
	genOID      ids.Generator
	genPID      ids.Generator

	tcp     tcpFrontend
	httpSrv *http.Server
	httpLn  net.Listener
	stop    chan struct{}
	wg      sync.WaitGroup
}

// tcpFrontend accepts and serves command connections.
type tcpFrontend interface {
	Addr() *net.TCPAddr
	Close()
}

// New creates a server; Start runs it.
func New(cfg *config.Config, log *logger.Logger, reg *metrics.Registry) *Server {
	if reg == nil {
		reg = &metrics.Registry{}
	}
	s := &Server{
		cfg:  cfg,
		log:  log,
		reg:  reg,
		stop: make(chan struct{}),
	}
	s.activeHosts = reg.NewGauge("noray_active_hosts", "Number of currently active hosts")
	s.Hosts = hosts.NewRepository()
	s.Pool = relay.NewSocketPool()
	s.Relays = relay.NewHandler(s.Pool, relay.NewMetrics(reg), log.Child("UDPRelayHandler"))
	s.Registrar = relay.NewRegistrar(s.Hosts, relay.NewRegistrarMetrics(reg), log.Child("UDPRemoteRegistrar"))
	if cfg.WordsOID.Enabled {
		s.genOID = ids.WordID(cfg.WordsOID.Length)
	} else {
		s.genOID = ids.NanoID(cfg.OID.Charset, cfg.OID.Length)
	}
	s.genPID = ids.NanoID(cfg.PID.Charset, cfg.PID.Length)
	return s
}

// Start brings everything up in the original's order: metrics HTTP, relay
// cleanup, registrar, relay ports, constraints, then the TCP server.
func (s *Server) Start() error {
	started := time.Now()
	s.log.Info(nil, "Starting Noray")
	c := s.cfg
	u := &c.UDPRelay

	// metrics
	mlog := s.log.Child("mod:metrics")
	mlog.Info(nil, "Starting HTTP server to serve metrics")
	ln, err := net.Listen("tcp", net.JoinHostPort(c.HTTP.Host, strconv.Itoa(c.HTTP.Port)))
	if err != nil {
		return fmt.Errorf("metrics listen: %w", err)
	}
	s.httpLn = ln
	s.httpSrv = &http.Server{Handler: http.HandlerFunc(s.serveMetrics), ReadHeaderTimeout: 10 * time.Second}
	go s.httpSrv.Serve(ln)
	mlog.Info(nil, "Serving metrics over HTTP on port %s:%d", c.HTTP.Host, c.HTTP.Port)

	// relays
	rlog := s.log.Child("mod:relay")
	rlog.Info(nil, "Starting periodic UDP relay cleanup job, running every %s", config.FormatDuration(u.CleanupInterval))
	s.wg.Add(1)
	go s.cleanupLoop()

	rlog.Info(nil, "Listening on port %d for UDP remote registrars", u.RegistrarPort)
	if err := s.Registrar.Listen("0.0.0.0", u.RegistrarPort); err != nil {
		return fmt.Errorf("registrar listen: %w", err)
	}

	rlog.Info(nil, "Binding %d ports for relaying", len(u.Ports))
	bound := 0
	for _, port := range u.Ports {
		if _, err := s.Pool.AllocatePort(port); err != nil {
			rlog.Warn(logger.F{"err": err.Error()}, "Failed to bind port %d, ignoring", port)
			continue
		}
		bound++
	}
	rlog.Info(nil, "Bound %d relay ports", bound)

	rlog.Info(nil, "Limiting relay bandwidth to %s/s and global bandwidth to %s/s",
		config.FormatByteSize(u.MaxIndividualTraffic), config.FormatByteSize(u.MaxGlobalTraffic))
	s.Relays.AddConstraint(&relay.IndividualBandwidth{Traffic: u.MaxIndividualTraffic, Interval: u.TrafficInterval})
	s.Relays.AddConstraint(relay.NewGlobalBandwidth(u.MaxGlobalTraffic, u.TrafficInterval, nil))
	rlog.Info(nil, "Blocking relay traffic after %s or %s",
		config.FormatDuration(u.MaxLifetimeDuration), config.FormatByteSize(u.MaxLifetimeTraffic))
	s.Relays.AddConstraint(&relay.Lifetime{Duration: u.MaxLifetimeDuration})
	s.Relays.AddConstraint(&relay.Traffic{Traffic: u.MaxLifetimeTraffic})
	rlog.Info(nil, "Applying dynamic relaying")
	s.Relays.EnableDynamicRelaying()

	// TCP
	s.log.Info(nil, "Starting TCP server")
	tl, err := listenTCP(s, net.JoinHostPort(c.Socket.Host, strconv.Itoa(c.Socket.Port)))
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}
	s.tcp = tl
	s.log.Info(nil, "Listening on %s:%d", c.Socket.Host, c.Socket.Port)
	s.log.Info(nil, "Started noray in %g ms", float64(time.Since(started).Microseconds())/1000)
	return nil
}

// TCPAddr is the bound command server address.
func (s *Server) TCPAddr() *net.TCPAddr { return s.tcp.Addr() }

// HTTPAddr is the bound metrics address.
func (s *Server) HTTPAddr() *net.TCPAddr { return s.httpLn.Addr().(*net.TCPAddr) }

func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	// Like the original: exactly "/metrics" (no query string), any method.
	if r.RequestURI != "/metrics" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var buf bytes.Buffer
	s.reg.Write(&buf)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Write(buf.Bytes())
}

func (s *Server) cleanupLoop() {
	defer s.wg.Done()
	d := time.Duration(s.cfg.UDPRelay.CleanupInterval * float64(time.Second))
	if d < time.Millisecond {
		d = time.Millisecond // setInterval clamps to 1 ms too
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if n := s.Relays.Cleanup(s.cfg.UDPRelay.Timeout); n > 0 {
				s.log.Child("mod:relay").Debug(nil, "Freed %d expired relays", n)
			}
		}
	}
}

// Shutdown stops accepting, closes every connection and socket, and waits.
func (s *Server) Shutdown() {
	s.log.Info(nil, "Shutting down")
	close(s.stop)
	if s.tcp != nil {
		s.tcp.Close()
	}
	if s.httpSrv != nil {
		s.log.Child("mod:metrics").Info(nil, "noray closing, shutting down HTTP server")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s.httpSrv.Shutdown(ctx)
		cancel()
	}
	rlog := s.log.Child("mod:relay")
	rlog.Info(nil, "Noray shutting down, cancelling UDP relay cleanup job")
	rlog.Info(nil, "Closing UDP remote registrar socket")
	s.Registrar.Close()
	rlog.Info(nil, "Closing socket pool")
	s.Pool.Clear()
	rlog.Info(nil, "Closing relay handler")
	s.Relays.Clear()
	s.wg.Wait()
}
