// Command noray is a Go drop-in replacement for the Noray server
// (github.com/foxssake/noray): NAT punch-through orchestration and UDP relay.
package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/blankeos/noray-go/internal/config"
	"github.com/blankeos/noray-go/internal/logger"
	"github.com/blankeos/noray-go/internal/metrics"
	"github.com/blankeos/noray-go/internal/server"
)

func main() {
	config.LoadDotenv(".env")
	log := logger.New(os.Stdout, "noray", logger.ParseLevel(config.LogLevel(os.LookupEnv)))
	cfg, err := config.FromEnv(os.LookupEnv)
	if err != nil {
		log.Fatal(logger.F{"err": err.Error()}, "Invalid configuration")
		os.Exit(1)
	}
	log.Info(logger.F{"config": cfg}, "Loaded application config")

	metrics.Default.RegisterRuntime()
	srv := server.New(cfg, log, metrics.Default)
	if err := srv.Start(); err != nil {
		log.Fatal(logger.F{"err": err.Error()}, "Failed to start")
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	srv.Shutdown()
}
