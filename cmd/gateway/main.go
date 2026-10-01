package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"gateforge/internal/security"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.LevelKey {
			a.Key = "severity"
		}
		return a
	}}))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	listen := flag.String("listen", "", "HTTP listen address (default 127.0.0.1:8080, or 0.0.0.0:$PORT when PORT is set)")
	upstream := flag.String("upstream", "http://127.0.0.1:9000", "Upstream HTTP(S) URL, optionally with a path prefix")
	configPath := flag.String("config", "", "JSON route configuration file (cannot be combined with -upstream)")
	tlsCert := flag.String("tls-cert", os.Getenv("GATEFORGE_TLS_CERT"), "TLS certificate PEM file")
	tlsKey := flag.String("tls-key", os.Getenv("GATEFORGE_TLS_KEY"), "TLS private key PEM file")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments; use -help for usage")
	}
	address, err := listenAddress(*listen, os.Getenv("PORT"))
	if err != nil {
		return err
	}

	upstreamSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "upstream" {
			upstreamSet = true
		}
	})
	var tlsConfig *tls.Config
	if *tlsCert != "" || *tlsKey != "" {
		tlsConfig, err = security.TLSConfig(*tlsCert, *tlsKey)
		if err != nil {
			return err
		}
	}
	handler, cleanup, err := runtimeHandler(*configPath, *upstream, upstreamSet, logger, tlsConfig != nil)
	if err != nil {
		return err
	}
	defer cleanup()
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		MaxHeaderBytes:    32 * 1024,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		TLSConfig:         tlsConfig,
	}
	if address := os.Getenv("GATEFORGE_HEALTH_LISTEN"); address != "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("GATEFORGE_HEALTH_LISTEN must use a loopback IP")
		}
		healthListener, err := net.Listen("tcp", address)
		if err != nil {
			return err
		}
		healthServer := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
				http.NotFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		})}
		defer healthServer.Close()
		go func() {
			if err := healthServer.Serve(healthListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("health listener stopped")
			}
		}()
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			serveErr <- server.ServeTLS(listener, "", "")
		} else {
			serveErr <- server.Serve(listener)
		}
	}()
	if *configPath != "" {
		logger.Info("gateway listening", "address", listener.Addr().String(), "config", *configPath)
	} else {
		logger.Info("gateway listening", "address", listener.Addr().String(), "upstream", *upstream)
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		// Restore the default signal behavior so a second interrupt exits immediately.
		stop()
	}
	logger.Info("gateway shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		server.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func listenAddress(explicit, port string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if port == "" {
		return "127.0.0.1:8080", nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", fmt.Errorf("PORT must be an integer between 1 and 65535")
	}
	return net.JoinHostPort("0.0.0.0", port), nil
}
