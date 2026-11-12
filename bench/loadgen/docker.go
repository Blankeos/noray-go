package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

var dockerClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", "/var/run/docker.sock")
	}},
}

type stats struct {
	at    time.Time
	cpuNs uint64
	mem   float64
}

// dockerStats reads one stats sample from the Docker Engine API.
func dockerStats(container string) (stats, error) {
	res, err := dockerClient.Get("http://docker/containers/" + container + "/stats?stream=false&one-shot=true")
	if err != nil {
		return stats{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return stats{}, fmt.Errorf("docker stats: %s", res.Status)
	}
	var v struct {
		Read     time.Time `json:"read"`
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return stats{}, err
	}
	if v.CPUStats.CPUUsage.TotalUsage == 0 {
		return stats{}, fmt.Errorf("container %s not running", container)
	}
	mem := float64(v.MemoryStats.Usage)
	if inactive, ok := v.MemoryStats.Stats["inactive_file"]; ok && float64(inactive) < mem {
		mem -= float64(inactive) // what `docker stats` shows as MEM USAGE
	}
	return stats{at: v.Read, cpuNs: v.CPUStats.CPUUsage.TotalUsage, mem: mem}, nil
}

// inspectStarted returns the container's State.StartedAt.
func inspectStarted(container string) (time.Time, error) {
	res, err := dockerClient.Get("http://docker/containers/" + container + "/json")
	if err != nil {
		return time.Time{}, err
	}
	defer res.Body.Close()
	var v struct {
		State struct {
			StartedAt time.Time `json:"StartedAt"`
		} `json:"State"`
	}
	err = json.NewDecoder(res.Body).Decode(&v)
	return v.State.StartedAt, err
}
