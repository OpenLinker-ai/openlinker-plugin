package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/egressgateway"
)

func main() {
	logger := log.New(os.Stderr, "openlinker-egress ", log.LstdFlags)
	upstreamRaw, err := egressgateway.ResolveSecret(
		"OPENLINKER_EGRESS_UPSTREAM_PROXY",
	)
	if err != nil {
		logger.Fatal(err)
	}
	var upstream *url.URL
	if upstreamRaw != "" {
		upstream, err = egressgateway.ParseUpstreamProxy(upstreamRaw)
		if err != nil {
			logger.Fatal(err)
		}
	}
	handler, secureDNSHost, err := egressgateway.NewDefault(
		os.Getenv("OPENLINKER_EGRESS_DOH_URL"),
		upstream,
		logger,
	)
	if err != nil {
		logger.Fatal(err)
	}
	listen := strings.TrimSpace(os.Getenv("OPENLINKER_EGRESS_LISTEN"))
	if listen == "" {
		listen = "0.0.0.0:3128"
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Printf(
		"version=%s listen=%s upstream=%t secure_dns=%s",
		egressgateway.Version,
		listen,
		upstream != nil,
		secureDNSHost,
	)
	if err := server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}
