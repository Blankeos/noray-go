// Command loadgen benchmarks a Noray server (Go or the original Node one)
// through the real protocol: TCP register-host, UDP pid registration and
// connect-relay, then UDP game-like traffic through the relays.
//
//	loadgen probe  -server noray-srv -container noray-srv   startup time
//	loadgen idle   -container noray-srv                      idle CPU / memory
//	loadgen hosts  -server noray-srv -n 5000                 N registered TCP hosts
//	loadgen relay  -server noray-srv -rooms 100 -players 6 -rate 30
//	loadgen report results/*.jsonl                           markdown summary
//
// Server CPU and memory are read from the Docker Engine API (mount
// /var/run/docker.sock and pass -container).
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var base = time.Now()

func mono() int64 { return int64(time.Since(base)) }

type common struct {
	server    string
	tcpPort   int
	regPort   int
	container string
	label     string
	scenario  string
}

func (c *common) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.server, "server", "noray-srv", "server host")
	fs.IntVar(&c.tcpPort, "tcp-port", 8890, "TCP command port")
	fs.IntVar(&c.regPort, "registrar-port", 8809, "UDP registrar port")
	fs.StringVar(&c.container, "container", "", "server container name for CPU/memory stats")
	fs.StringVar(&c.label, "label", "", "implementation label for the report (e.g. go, node)")
	fs.StringVar(&c.scenario, "scenario", "", "scenario name for the report")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen probe|idle|hosts|relay|report ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "probe":
		err = probe(os.Args[2:])
	case "idle":
		err = idle(os.Args[2:])
	case "hosts":
		err = hostsCmd(os.Args[2:])
	case "relay":
		err = relayCmd(os.Args[2:])
	case "report":
		err = report(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[loadgen %6.1fs] "+format+"\n", append([]any{time.Since(base).Seconds()}, args...)...)
}

// --- probe: startup time ---------------------------------------------------

func probe(args []string) error {
	var c common
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	c.flags(fs)
	timeout := fs.Duration("timeout", 60*time.Second, "give up after")
	fs.Parse(args)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.server, strconv.Itoa(c.tcpPort)), 200*time.Millisecond)
		if err == nil {
			ready := time.Now()
			conn.Close()
			res := map[string]any{"mode": "probe", "impl": c.label, "scenario": c.scenario, "ready_unix_ns": ready.UnixNano()}
			if c.container != "" {
				if st, err := inspectStarted(c.container); err == nil {
					res["startup_ms"] = float64(ready.Sub(st).Microseconds()) / 1000
				}
			}
			emit(res)
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("server never became ready")
}

// --- idle --------------------------------------------------------------------

func idle(args []string) error {
	var c common
	fs := flag.NewFlagSet("idle", flag.ExitOnError)
	c.flags(fs)
	settle := fs.Duration("settle", 10*time.Second, "wait before measuring")
	window := fs.Duration("window", 10*time.Second, "measurement window")
	fs.Parse(args)
	time.Sleep(*settle)
	cpu, mem, err := measure(c.container, *window)
	if err != nil {
		return err
	}
	emit(map[string]any{"mode": "idle", "impl": c.label, "scenario": c.scenario, "cpu_pct": cpu, "mem_bytes": mem})
	return nil
}

// measure samples the container over window: CPU% (100 = one core) and the
// median memory usage (docker stats' "MEM USAGE": usage - inactive_file).
func measure(container string, window time.Duration) (float64, float64, error) {
	if container == "" {
		time.Sleep(window)
		return 0, 0, nil
	}
	first, err := dockerStats(container)
	if err != nil {
		return 0, 0, err
	}
	var mems []float64
	end := time.Now().Add(window)
	for time.Now().Before(end) {
		time.Sleep(time.Second)
		if s, err := dockerStats(container); err == nil {
			mems = append(mems, s.mem)
		}
	}
	last, err := dockerStats(container)
	if err != nil {
		return 0, 0, err
	}
	mems = append(mems, last.mem)
	cpu := float64(last.cpuNs-first.cpuNs) / float64(last.at.Sub(first.at).Nanoseconds()) * 100
	return cpu, median(mems), nil
}

// --- hosts -------------------------------------------------------------------

func hostsCmd(args []string) error {
	var c common
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	c.flags(fs)
	n := fs.Int("n", 1000, "hosts to register")
	conc := fs.Int("concurrency", 64, "parallel registrations")
	settle := fs.Duration("settle", 5*time.Second, "wait before measuring memory")
	window := fs.Duration("window", 5*time.Second, "measurement window")
	fs.Parse(args)

	addr := net.JoinHostPort(c.server, strconv.Itoa(c.tcpPort))
	conns := make([]net.Conn, *n)
	var next, failed atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= *n {
					return
				}
				conn, _, _, err := register(addr)
				if err != nil {
					failed.Add(1)
					continue
				}
				conns[i] = conn
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	logf("registered %d hosts in %v (%d failed)", *n-int(failed.Load()), elapsed, failed.Load())
	time.Sleep(*settle)
	cpu, mem, err := measure(c.container, *window)
	if err != nil {
		return err
	}
	alive := serverAlive(addr)
	for _, conn := range conns {
		if conn != nil {
			conn.Close()
		}
	}
	emit(map[string]any{
		"mode": "hosts", "impl": c.label, "scenario": c.scenario, "n": *n, "failed": failed.Load(),
		"register_s": elapsed.Seconds(), "cpu_pct": cpu, "mem_bytes": mem, "alive": alive,
	})
	return nil
}

func register(addr string) (net.Conn, *bufio.Reader, [2]string, error) {
	var ids [2]string
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, nil, ids, err
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write([]byte("register-host\n")); err != nil {
		conn.Close()
		return nil, nil, ids, err
	}
	rd := bufio.NewReaderSize(conn, 512)
	for ids[0] == "" || ids[1] == "" {
		line, err := rd.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, ids, err
		}
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "set-oid "); ok {
			ids[0] = v
		} else if v, ok := strings.CutPrefix(line, "set-pid "); ok {
			ids[1] = v
		}
	}
	conn.SetDeadline(time.Time{})
	return conn, rd, ids, nil
}

func serverAlive(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// --- relay -------------------------------------------------------------------

const header = 16 // send time (8) + flow (4) + seq (4)

type player struct {
	tcp  net.Conn
	rd   *bufio.Reader
	udp  *net.UDPConn
	oid  string
	pid  string
	dsts []netip.AddrPort // where this player sends (relay ports)
	flow []uint32         // flow id per destination

	mu   sync.Mutex
	lat  []uint32 // received latencies (µs) inside the window
	recv atomic.Int64
}

type flowStat struct {
	sent atomic.Int64
	recv atomic.Int64
}

type relayRun struct {
	c        common
	srvIP    netip.Addr
	players  []*player
	flows    []flowStat
	rate     atomic.Int64 // Hz
	minSize  int
	maxSize  int
	winStart atomic.Int64
	winEnd   atomic.Int64
	mu       sync.Mutex
}

func relayCmd(args []string) error {
	var c common
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	c.flags(fs)
	rooms := fs.Int("rooms", 10, "rooms (1 host + players-1 clients each)")
	players := fs.Int("players", 6, "players per room")
	rate := fs.Int("rate", 30, "packets per second per direction per client (Hz)")
	sweep := fs.String("sweep", "", "comma separated rates (Hz) for a saturation sweep; stops when loss > -max-loss")
	maxLoss := fs.Float64("max-loss", 0.01, "sweep stop condition")
	minSize := fs.Int("min-size", 60, "min packet size (bytes)")
	maxSize := fs.Int("max-size", 200, "max packet size (bytes)")
	warmup := fs.Duration("warmup", 3*time.Second, "warmup before measuring")
	window := fs.Duration("window", 15*time.Second, "measurement window")
	conc := fs.Int("concurrency", 32, "parallel room setups")
	metricsPort := fs.Int("metrics-port", 0, "if set, read noray_relay_blocked_count from the server's /metrics")
	fs.Parse(args)
	if *minSize < header {
		*minSize = header
	}

	ips, err := net.LookupIP(c.server)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("resolve %s: %v", c.server, err)
	}
	ip, _ := netip.AddrFromSlice(ips[0].To4())
	r := &relayRun{c: c, srvIP: ip, minSize: *minSize, maxSize: *maxSize}

	setupStart := time.Now()
	if err := r.setup(*rooms, *players, *conc); err != nil {
		return err
	}
	setup := time.Since(setupStart)
	logf("set up %d rooms x %d players (%d relayed flows) in %v", *rooms, *players, len(r.flows), setup)

	rates := []int{*rate}
	if *sweep != "" {
		rates = nil
		for _, s := range strings.Split(*sweep, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return err
			}
			rates = append(rates, v)
		}
	}

	r.winStart.Store(1 << 62)
	r.winEnd.Store(1 << 62)
	r.rate.Store(int64(rates[0]))
	stop := make(chan struct{})
	var senders sync.WaitGroup
	for _, p := range r.players {
		go r.receive(p)
		senders.Add(1)
		go func(p *player) { defer senders.Done(); r.send(p, stop) }(p)
	}

	tcpAddr := net.JoinHostPort(c.server, strconv.Itoa(c.tcpPort))
	for _, hz := range rates {
		r.rate.Store(int64(hz))
		r.reset()
		time.Sleep(*warmup)
		ws := mono()
		r.winStart.Store(ws)
		r.winEnd.Store(ws + int64(*window))
		cpu, mem, merr := measure(c.container, *window)
		time.Sleep(time.Second) // drain in-flight packets
		res := r.result()
		res["mode"] = "relay"
		res["impl"] = c.label
		res["scenario"] = c.scenario
		res["rooms"] = *rooms
		res["players"] = *players
		res["rate_hz"] = hz
		res["size"] = fmt.Sprintf("%d-%d", *minSize, *maxSize)
		res["setup_s"] = setup.Seconds()
		res["cpu_pct"] = cpu
		res["mem_bytes"] = mem
		if merr != nil {
			res["stats_error"] = merr.Error()
		}
		res["window_s"] = window.Seconds()
		res["alive"] = serverAlive(tcpAddr)
		if *metricsPort > 0 {
			if b, ok := blockedPackets(c.server, *metricsPort); ok {
				res["blocked_total"] = b
			}
		}
		emit(res)
		logf("rate %d Hz: offered %.0f pps, delivered %.0f pps, loss %.2f%%, p50 %v us, p99 %v us, cpu %.0f%%, alive %v",
			hz, res["pps_offered"], res["pps_delivered"], res["loss"].(float64)*100, res["p50_us"], res["p99_us"], cpu, res["alive"])
		if !res["alive"].(bool) || (*sweep != "" && res["loss"].(float64) > *maxLoss) {
			break
		}
	}
	close(stop)
	senders.Wait()
	for _, p := range r.players {
		p.udp.Close()
		p.tcp.Close()
	}
	return nil
}

func (r *relayRun) newPlayer() (*player, error) {
	conn, rd, ids, err := register(net.JoinHostPort(r.c.server, strconv.Itoa(r.c.tcpPort)))
	if err != nil {
		return nil, fmt.Errorf("register-host: %w", err)
	}
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		conn.Close()
		return nil, err
	}
	udp.SetReadBuffer(4 << 20)
	udp.SetWriteBuffer(4 << 20)
	p := &player{tcp: conn, rd: rd, udp: udp, oid: ids[0], pid: ids[1]}
	reg := netip.AddrPortFrom(r.srvIP, uint16(r.c.regPort))
	buf := make([]byte, 256)
	for attempt := 0; attempt < 100; attempt++ {
		udp.WriteToUDPAddrPort([]byte(p.pid), reg)
		udp.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _, err := udp.ReadFromUDPAddrPort(buf)
		if err == nil {
			udp.SetReadDeadline(time.Time{})
			if string(buf[:n]) != "OK" {
				return nil, fmt.Errorf("registrar: %q", buf[:n])
			}
			return p, nil
		}
	}
	return nil, errors.New("registrar timed out")
}

func readLine(p *player, prefix string) (string, error) {
	p.tcp.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer p.tcp.SetReadDeadline(time.Time{})
	for {
		line, err := p.rd.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, prefix+" "); ok {
			return v, nil
		}
		if line == prefix {
			return "", fmt.Errorf("server replied with an error to %s", prefix)
		}
	}
}

func (r *relayRun) setup(rooms, players, conc int) error {
	var next atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, conc)
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if int(next.Add(1)) > rooms {
					return
				}
				if err := r.setupRoom(players); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func (r *relayRun) addFlow() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flows = append(r.flows, flowStat{})
	return uint32(len(r.flows) - 1)
}

func (r *relayRun) setupRoom(players int) error {
	host, err := r.newPlayer()
	if err != nil {
		return err
	}
	room := []*player{host}
	for i := 1; i < players; i++ {
		cl, err := r.newPlayer()
		if err != nil {
			return err
		}
		if _, err := cl.tcp.Write([]byte("connect-relay " + host.oid + "\n")); err != nil {
			return err
		}
		hostRelay, err := readLine(cl, "connect-relay")
		if err != nil {
			return err
		}
		clientRelay, err := readLine(host, "connect-relay")
		if err != nil {
			return err
		}
		hp, _ := strconv.Atoi(hostRelay)
		cp, _ := strconv.Atoi(clientRelay)
		cl.dsts = append(cl.dsts, netip.AddrPortFrom(r.srvIP, uint16(hp)))
		cl.flow = append(cl.flow, r.addFlow())
		host.dsts = append(host.dsts, netip.AddrPortFrom(r.srvIP, uint16(cp)))
		host.flow = append(host.flow, r.addFlow())
		room = append(room, cl)
	}
	r.mu.Lock()
	r.players = append(r.players, room...)
	r.mu.Unlock()
	return nil
}

func (r *relayRun) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.winStart.Store(1 << 62)
	r.winEnd.Store(1 << 62)
	for i := range r.flows {
		r.flows[i].sent.Store(0)
		r.flows[i].recv.Store(0)
	}
	for _, p := range r.players {
		p.recv.Store(0)
	}
}

// send paces packets on an absolute schedule (catching up after stalls).
func (r *relayRun) send(p *player, stop chan struct{}) {
	buf := make([]byte, r.maxSize)
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	var seq uint32
	rate := r.rate.Load()
	interval := time.Second / time.Duration(rate)
	next := time.Now().Add(time.Duration(rng.Int64N(int64(interval))))
	for {
		select {
		case <-stop:
			return
		default:
		}
		if cur := r.rate.Load(); cur != rate {
			rate = cur
			interval = time.Second / time.Duration(rate)
			next = time.Now()
		}
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		next = next.Add(interval)
		if behind := time.Since(next); behind > time.Second {
			next = time.Now() // don't burst after a long stall
		}
		for i, dst := range p.dsts {
			size := r.minSize
			if r.maxSize > r.minSize {
				size += rng.IntN(r.maxSize - r.minSize + 1)
			}
			now := mono()
			binary.LittleEndian.PutUint64(buf[0:], uint64(now))
			binary.LittleEndian.PutUint32(buf[8:], p.flow[i])
			binary.LittleEndian.PutUint32(buf[12:], seq)
			seq++
			if _, err := p.udp.WriteToUDPAddrPort(buf[:size], dst); err == nil {
				if now >= r.winStart.Load() && now < r.winEnd.Load() {
					r.flows[p.flow[i]].sent.Add(1)
				}
			}
		}
	}
}

func (r *relayRun) receive(p *player) {
	buf := make([]byte, 2048)
	for {
		n, _, err := p.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if n < header {
			continue
		}
		now := mono()
		sent := int64(binary.LittleEndian.Uint64(buf[0:]))
		flow := binary.LittleEndian.Uint32(buf[8:])
		if sent >= r.winStart.Load() && sent < r.winEnd.Load() && int(flow) < len(r.flows) {
			r.flows[flow].recv.Add(1)
			p.recv.Add(1)
			us := (now - sent) / 1000
			p.mu.Lock()
			p.lat = append(p.lat, uint32(min(us, 1<<31)))
			p.mu.Unlock()
		}
	}
}

func (r *relayRun) result() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var sent, recv int64
	for i := range r.flows {
		sent += r.flows[i].sent.Load()
		recv += min(r.flows[i].recv.Load(), r.flows[i].sent.Load())
	}
	var lat []uint32
	for _, p := range r.players {
		p.mu.Lock()
		lat = append(lat, p.lat...)
		p.lat = p.lat[:0]
		p.mu.Unlock()
	}
	slices.Sort(lat)
	pct := func(q float64) any {
		if len(lat) == 0 {
			return nil
		}
		return lat[min(len(lat)-1, int(q*float64(len(lat))))]
	}
	win := float64(r.winEnd.Load()-r.winStart.Load()) / 1e9
	loss := 0.0
	if sent > 0 {
		loss = 1 - float64(recv)/float64(sent)
	}
	var maxLat any
	if len(lat) > 0 {
		maxLat = lat[len(lat)-1]
	}
	return map[string]any{
		"flows": len(r.flows), "sent": sent, "received": recv, "loss": loss,
		"pps_offered": float64(sent) / win, "pps_delivered": float64(recv) / win,
		"p50_us": pct(0.5), "p99_us": pct(0.99), "max_us": maxLat,
	}
}

// --- helpers -------------------------------------------------------------------

// blockedPackets sums noray_relay_blocked_count (Go server only).
func blockedPackets(server string, port int) (float64, bool) {
	res, err := http.Get("http://" + net.JoinHostPort(server, strconv.Itoa(port)) + "/metrics")
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	total, found := 0.0, false
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "noray_relay_blocked_count{"); ok {
			if i := strings.LastIndexByte(rest, ' '); i >= 0 {
				v, _ := strconv.ParseFloat(rest[i+1:], 64)
				total += v
				found = true
			}
		}
	}
	return total, found
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
