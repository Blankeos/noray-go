// Package metrics is a tiny lock-free Prometheus text-format registry with
// the metric types Noray uses, formatted like prom-client 14 (no _total
// suffix is added to counters).
package metrics

import (
	"bufio"
	"io"
	"math"
	"runtime"
	"sort"
	"strconv"

	"sync"
	"sync/atomic"
)

type metric interface {
	write(w *bufio.Writer)
}

// Registry holds metrics in registration order.
type Registry struct {
	mu      sync.Mutex
	metrics []metric
}

// Default is the registry served on /metrics.
var Default = &Registry{}

func (r *Registry) add(m metric) {
	r.mu.Lock()
	r.metrics = append(r.metrics, m)
	r.mu.Unlock()
}

// Write renders every metric in the Prometheus text format.
func (r *Registry) Write(out io.Writer) error {
	r.mu.Lock()
	ms := append([]metric(nil), r.metrics...)
	r.mu.Unlock()
	w := bufio.NewWriter(out)
	for i, m := range ms {
		if i > 0 {
			w.WriteByte('\n')
		}
		m.write(w)
	}
	w.WriteByte('\n')
	return w.Flush()
}

func header(w *bufio.Writer, name, help, typ string) {
	w.WriteString("# HELP " + name + " " + help + "\n# TYPE " + name + " " + typ + "\n")
}

func fmtFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// atomicFloat is a float64 updated with CAS.
type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) Add(d float64) {
	for {
		old := f.bits.Load()
		if f.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}
func (f *atomicFloat) Set(v float64) { f.bits.Store(math.Float64bits(v)) }
func (f *atomicFloat) Get() float64  { return math.Float64frombits(f.bits.Load()) }

// Counter is a monotonically increasing value.
type Counter struct {
	name, help string
	v          atomic.Uint64
}

// NewCounter registers a counter.
func (r *Registry) NewCounter(name, help string) *Counter {
	c := &Counter{name: name, help: help}
	r.add(c)
	return c
}

func (c *Counter) Inc()          { c.v.Add(1) }
func (c *Counter) Add(n uint64)  { c.v.Add(n) }
func (c *Counter) Value() uint64 { return c.v.Load() }
func (c *Counter) write(w *bufio.Writer) {
	header(w, c.name, c.help, "counter")
	w.WriteString(c.name + " " + strconv.FormatUint(c.v.Load(), 10) + "\n")
}

// CounterVec is a counter with one label and a fixed set of label values.
type CounterVec struct {
	name, help, label string
	values            []string
	counters          []atomic.Uint64
}

// NewCounterVec registers a labelled counter with known label values.
func (r *Registry) NewCounterVec(name, help, label string, values ...string) *CounterVec {
	c := &CounterVec{name: name, help: help, label: label, values: values, counters: make([]atomic.Uint64, len(values))}
	r.add(c)
	return c
}

// Inc increments the counter for label value index i.
func (c *CounterVec) Inc(i int)          { c.counters[i].Add(1) }
func (c *CounterVec) Value(i int) uint64 { return c.counters[i].Load() }
func (c *CounterVec) write(w *bufio.Writer) {
	header(w, c.name, c.help, "counter")
	for i, v := range c.values {
		w.WriteString(c.name + "{" + c.label + "=\"" + v + "\"} " + strconv.FormatUint(c.counters[i].Load(), 10) + "\n")
	}
}

// Gauge is a value that goes up and down.
type Gauge struct {
	name, help string
	v          atomicFloat
	fn         func() float64
}

// NewGauge registers a gauge.
func (r *Registry) NewGauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	r.add(g)
	return g
}

// NewGaugeFunc registers a gauge read from fn at scrape time.
func (r *Registry) NewGaugeFunc(name, help string, fn func() float64) *Gauge {
	g := &Gauge{name: name, help: help, fn: fn}
	r.add(g)
	return g
}

func (g *Gauge) Inc()          { g.v.Add(1) }
func (g *Gauge) Dec()          { g.v.Add(-1) }
func (g *Gauge) Set(v float64) { g.v.Set(v) }
func (g *Gauge) Value() float64 {
	if g.fn != nil {
		return g.fn()
	}
	return g.v.Get()
}
func (g *Gauge) write(w *bufio.Writer) {
	header(w, g.name, g.help, "gauge")
	w.WriteString(g.name + " " + fmtFloat(g.Value()) + "\n")
}

// CounterFunc is a counter read from fn at scrape time.
type CounterFunc struct {
	name, help string
	fn         func() float64
}

// NewCounterFunc registers a counter read at scrape time.
func (r *Registry) NewCounterFunc(name, help string, fn func() float64) {
	r.add(&CounterFunc{name: name, help: help, fn: fn})
}
func (c *CounterFunc) write(w *bufio.Writer) {
	header(w, c.name, c.help, "counter")
	w.WriteString(c.name + " " + fmtFloat(c.fn()) + "\n")
}

// DefBuckets are prom-client's default histogram buckets.
var DefBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Histogram counts observations in cumulative buckets.
type Histogram struct {
	name, help string
	buckets    []float64
	counts     []atomic.Uint64 // per bucket (non-cumulative), last is +Inf
	count      atomic.Uint64
	sum        atomicFloat
}

// NewHistogram registers a histogram with the given upper bounds.
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	h := &Histogram{name: name, help: help, buckets: b, counts: make([]atomic.Uint64, len(b)+1)}
	r.add(h)
	return h
}

// Observe records one value. It does not allocate.
func (h *Histogram) Observe(v float64) {
	i := 0
	for i < len(h.buckets) && v > h.buckets[i] {
		i++
	}
	h.counts[i].Add(1)
	h.count.Add(1)
	h.sum.Add(v)
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 { return h.count.Load() }

func (h *Histogram) write(w *bufio.Writer) {
	header(w, h.name, h.help, "histogram")
	var cum uint64
	for i, b := range h.buckets {
		cum += h.counts[i].Load()
		w.WriteString(h.name + "_bucket{le=\"" + fmtFloat(b) + "\"} " + strconv.FormatUint(cum, 10) + "\n")
	}
	cum += h.counts[len(h.buckets)].Load()
	w.WriteString(h.name + "_bucket{le=\"+Inf\"} " + strconv.FormatUint(cum, 10) + "\n")
	w.WriteString(h.name + "_sum " + fmtFloat(h.sum.Get()) + "\n")
	w.WriteString(h.name + "_count " + strconv.FormatUint(h.count.Load(), 10) + "\n")
}

// RegisterRuntime adds process_* metrics (named like prom-client's defaults
// where possible) and a few go_* metrics in place of prom-client's nodejs_*.
func (r *Registry) RegisterRuntime() {
	registerProcess(r)
	r.NewGaugeFunc("go_goroutines", "Number of goroutines that currently exist.", func() float64 {
		return float64(runtime.NumGoroutine())
	})
	var mu sync.Mutex
	var ms runtime.MemStats
	read := func(f func(*runtime.MemStats) uint64) func() float64 {
		return func() float64 {
			mu.Lock()
			defer mu.Unlock()
			runtime.ReadMemStats(&ms)
			return float64(f(&ms))
		}
	}
	r.NewGaugeFunc("go_memstats_heap_alloc_bytes", "Number of heap bytes allocated and still in use.", read(func(m *runtime.MemStats) uint64 { return m.HeapAlloc }))
	r.NewGaugeFunc("go_memstats_stack_inuse_bytes", "Number of bytes in use by the stack allocator.", read(func(m *runtime.MemStats) uint64 { return m.StackInuse }))
	r.NewGaugeFunc("go_memstats_heap_inuse_bytes", "Number of heap bytes that are in use.", read(func(m *runtime.MemStats) uint64 { return m.HeapInuse }))
	r.NewGaugeFunc("go_memstats_sys_bytes", "Number of bytes obtained from system.", read(func(m *runtime.MemStats) uint64 { return m.Sys }))
	r.NewCounterFunc("go_gc_cycles_total", "Number of completed GC cycles.", read(func(m *runtime.MemStats) uint64 { return uint64(m.NumGC) }))
}
