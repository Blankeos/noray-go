package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blankeos/noray-go/internal/config"
	"github.com/blankeos/noray-go/internal/logger"
)

// In-process end-to-end test, mirroring noray's test/e2e suite.

func start(t *testing.T) *Server {
	cfg, err := config.FromEnv(func(k string) (string, bool) {
		return map[string]string{
			"NORAY_SOCKET_HOST": "127.0.0.1", "NORAY_SOCKET_PORT": "0",
			"NORAY_HTTP_HOST": "127.0.0.1", "NORAY_HTTP_PORT": "0",
			"NORAY_UDP_REGISTRAR_PORT": "0",
		}[k], strings.HasPrefix(k, "NORAY_SOCKET") || strings.HasPrefix(k, "NORAY_HTTP") || k == "NORAY_UDP_REGISTRAR_PORT"
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.UDPRelay.Ports = []int{0, 0, 0, 0}
	s := New(cfg, logger.Nop(), nil)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)
	return s
}

type party struct {
	t   *testing.T
	tcp net.Conn
	rd  *bufio.Reader
	udp *net.UDPConn
	oid string
	pid string
}

func (p *party) line() string {
	p.tcp.SetReadDeadline(time.Now().Add(2 * time.Second))
	l, err := p.rd.ReadString('\n')
	if err != nil {
		p.t.Fatalf("read: %v", err)
	}
	return strings.TrimSuffix(l, "\n")
}

func (p *party) send(s string) { p.tcp.Write([]byte(s)) }

func join(t *testing.T, s *Server) *party {
	c, err := net.Dial("tcp", s.TCPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p := &party{t: t, tcp: c, rd: bufio.NewReader(c)}
	p.send("register-host\n")
	p.oid = strings.TrimPrefix(p.line(), "set-oid ")
	p.pid = strings.TrimPrefix(p.line(), "set-pid ")
	if len(p.oid) != 21 || len(p.pid) != 128 {
		t.Fatalf("ids %q %q", p.oid, p.pid)
	}
	p.udp, _ = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	t.Cleanup(func() { p.udp.Close() })
	reg := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(s.Registrar.Addr().Port))
	p.udp.WriteToUDPAddrPort([]byte(p.pid), reg)
	if got := p.recv(); got != "OK" {
		t.Fatalf("registrar: %q", got)
	}
	return p
}

func (p *party) recv() string {
	p.udp.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := p.udp.ReadFromUDPAddrPort(buf)
	if err != nil {
		p.t.Fatalf("udp: %v", err)
	}
	return string(buf[:n])
}

func TestConnect(t *testing.T) {
	s := start(t)
	host, client := join(t, s), join(t, s)
	client.send("connect " + host.oid + "\n")
	toClient, toHost := client.line(), host.line()
	if toClient != "connect "+host.udp.LocalAddr().String() || toHost != "connect "+client.udp.LocalAddr().String() {
		t.Fatalf("%q %q", toClient, toHost)
	}
	client.send("connect nope\n")
	if l := client.line(); l != "connect" {
		t.Fatalf("error reply %q", l)
	}
	client.send("connect?q1 nope\n")
	if l := client.line(); l != "!q1" {
		t.Fatalf("error reply %q", l)
	}
}

func TestConnectRelay(t *testing.T) {
	s := start(t)
	host, client := join(t, s), join(t, s)
	client.send("connect-relay " + host.oid + "\n")
	hostRelay, _ := strconv.Atoi(strings.TrimPrefix(client.line(), "connect-relay "))
	clientRelay, _ := strconv.Atoi(strings.TrimPrefix(host.line(), "connect-relay "))
	if hostRelay == 0 || clientRelay == 0 || hostRelay == clientRelay {
		t.Fatal("relay ports")
	}
	to := func(port int) netip.AddrPort {
		return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))
	}
	client.udp.WriteToUDPAddrPort([]byte("Hello from client!"), to(hostRelay))
	if got := host.recv(); got != "Hello from client!" {
		t.Fatal(got)
	}
	host.udp.WriteToUDPAddrPort([]byte("Hello from host!"), to(clientRelay))
	if got := client.recv(); got != "Hello from host!" {
		t.Fatal(got)
	}

	res, err := http.Get("http://" + s.HTTPAddr().String() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"noray_active_hosts 2", "noray_relay_count 2", "noray_relay_size_count 2", "noray_remote_registrar_success 2"} {
		if !strings.Contains(string(body), want+"\n") {
			t.Errorf("metrics missing %q", want)
		}
	}
	if res, _ := http.Get("http://" + s.HTTPAddr().String() + "/"); res.StatusCode != 404 {
		t.Error("404")
	}
}

func TestDisconnectRemovesHosts(t *testing.T) {
	s := start(t)
	host := join(t, s)
	host.tcp.Close()
	deadline := time.Now().Add(2 * time.Second)
	for s.Hosts.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Hosts.Len() != 0 || s.activeHosts.Value() != 0 {
		t.Fatal("host not removed")
	}
}
