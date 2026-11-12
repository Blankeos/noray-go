package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// report prints, per scenario and implementation, the median of every
// numeric field over repeated runs, plus min-max as a noise indicator.
func report(files []string) error {
	type key struct{ scenario, impl string }
	runs := map[key][]map[string]any{}
	var order []key
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var v map[string]any
			if json.Unmarshal(sc.Bytes(), &v) != nil {
				continue
			}
			if v["mode"] == "image" {
				continue
			}
			s, _ := v["scenario"].(string)
			if rate, ok := v["rate_hz"].(float64); ok && strings.Contains(s, "sweep") {
				s = fmt.Sprintf("%s@%.0fHz", s, rate)
			}
			k := key{s, fmt.Sprint(v["impl"])}
			if _, ok := runs[k]; !ok {
				order = append(order, k)
			}
			runs[k] = append(runs[k], v)
		}
		fh.Close()
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].scenario < order[j].scenario })
	fields := []string{"startup_ms", "cpu_pct", "mem_bytes", "register_s", "pps_offered", "pps_delivered", "loss", "p50_us", "p99_us", "max_us", "alive"}
	fmt.Println("| scenario | impl | runs | " + strings.Join(fields, " | ") + " |")
	fmt.Println("|" + strings.Repeat("---|", len(fields)+3))
	for _, k := range order {
		rs := runs[k]
		cells := []string{k.scenario, k.impl, fmt.Sprint(len(rs))}
		for _, f := range fields {
			var vals []float64
			alive := ""
			for _, r := range rs {
				switch x := r[f].(type) {
				case float64:
					vals = append(vals, x)
				case bool:
					if x {
						alive += "y"
					} else {
						alive += "n"
					}
				}
			}
			switch {
			case alive != "":
				cells = append(cells, alive)
			case len(vals) == 0:
				cells = append(cells, "")
			default:
				sort.Float64s(vals)
				m := median(vals)
				cell := fmtVal(f, m)
				if len(vals) > 1 && vals[0] != vals[len(vals)-1] {
					cell += " (" + fmtVal(f, vals[0]) + "–" + fmtVal(f, vals[len(vals)-1]) + ")"
				}
				cells = append(cells, cell)
			}
		}
		fmt.Println("| " + strings.Join(cells, " | ") + " |")
	}
	return nil
}

func fmtVal(field string, v float64) string {
	switch field {
	case "mem_bytes":
		return fmt.Sprintf("%.1fMiB", v/1048576)
	case "loss":
		return fmt.Sprintf("%.2f%%", v*100)
	case "cpu_pct":
		return fmt.Sprintf("%.1f%%", v)
	case "register_s":
		return fmt.Sprintf("%.2fs", v)
	case "startup_ms":
		return fmt.Sprintf("%.0fms", v)
	}
	return fmt.Sprintf("%.0f", v)
}
