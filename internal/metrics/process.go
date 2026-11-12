package metrics

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var startTime = float64(time.Now().UnixNano()) / 1e9

func cpuTimes() (user, system float64) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime), tv(ru.Stime)
}

// procStatm returns (virtual, resident) bytes from /proc/self/statm (Linux).
func procStatm() (float64, float64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, 0, false
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return 0, 0, false
	}
	page := float64(os.Getpagesize())
	vsz, _ := strconv.ParseFloat(f[0], 64)
	rss, _ := strconv.ParseFloat(f[1], 64)
	return vsz * page, rss * page, true
}

func registerProcess(r *Registry) {
	r.NewCounterFunc("process_cpu_user_seconds_total", "Total user CPU time spent in seconds.", func() float64 {
		u, _ := cpuTimes()
		return u
	})
	r.NewCounterFunc("process_cpu_system_seconds_total", "Total system CPU time spent in seconds.", func() float64 {
		_, s := cpuTimes()
		return s
	})
	r.NewCounterFunc("process_cpu_seconds_total", "Total user and system CPU time spent in seconds.", func() float64 {
		u, s := cpuTimes()
		return u + s
	})
	r.NewGaugeFunc("process_start_time_seconds", "Start time of the process since unix epoch in seconds.", func() float64 {
		return float64(int64(startTime))
	})
	if _, _, ok := procStatm(); ok {
		r.NewGaugeFunc("process_resident_memory_bytes", "Resident memory size in bytes.", func() float64 {
			_, rss, _ := procStatm()
			return rss
		})
		r.NewGaugeFunc("process_virtual_memory_bytes", "Virtual memory size in bytes.", func() float64 {
			v, _, _ := procStatm()
			return v
		})
	}
	if _, err := os.ReadDir("/proc/self/fd"); err == nil {
		r.NewGaugeFunc("process_open_fds", "Number of open file descriptors.", func() float64 {
			e, _ := os.ReadDir("/proc/self/fd")
			return float64(len(e))
		})
		var lim syscall.Rlimit
		if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) == nil {
			r.NewGaugeFunc("process_max_fds", "Maximum number of open file descriptors.", func() float64 {
				return float64(lim.Cur)
			})
		}
	}
}
