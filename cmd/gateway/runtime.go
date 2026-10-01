package main

import (
	"context"
	"fmt"
	"gateforge/internal/analytics"
	"gateforge/internal/app"
	"gateforge/internal/config"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"gateforge/internal/shared"
	"gateforge/internal/storage"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func runtimeHandler(configPath, upstream string, upstreamSet bool, logger *slog.Logger, tlsEnabled bool) (*app.App, func(), error) {
	var c config.File
	var err error
	if configPath == "" {
		c.Routes = []config.Route{{Prefix: "/", Upstream: upstream}}
	} else {
		if upstreamSet {
			return nil, nil, fmt.Errorf("-config and -upstream cannot be used together")
		}
		c, err = config.Read(configPath)
		if err != nil {
			return nil, nil, err
		}
	}
	offloaded := os.Getenv("GATEFORGE_TLS_OFFLOADED") == "true"
	token := os.Getenv("GATEFORGE_ADMIN_TOKEN")
	if !tlsEnabled && !offloaded {
		if token != "" {
			return nil, nil, fmt.Errorf("administrator access requires TLS")
		}
		for _, r := range c.Routes {
			if r.Auth != "" && r.Auth != "public" {
				return nil, nil, fmt.Errorf("protected routes require TLS")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var db *storage.Postgres
	var store app.Store
	var keys security.KeyReader
	var redisStore *shared.Redis
	cleanup := func() {
		if redisStore != nil {
			redisStore.Close()
		}
		if db != nil {
			db.Close()
		}
	}
	snapshot := storage.Snapshot{Revision: 1, Routes: c.Routes}
	if url := os.Getenv("DATABASE_URL"); url != "" {
		db, err = storage.Open(ctx, url)
		if err != nil {
			return nil, nil, err
		}
		store = db
		keys = db
		if err = db.Migrate(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("database migration failed")
		}
	}
	auth, err := security.New(keys, c.JWT, offloaded)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	options := gateway.Options{Auth: auth, Metrics: analytics.New()}
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisStore, err = shared.Open(redisURL)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if err = redisStore.Ping(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("Redis is unavailable")
		}
		options.Redis = redisStore
	}
	// Validate before writing the initial configuration to durable storage.
	validationHandler, err := gateway.NewRoutesWithOptions(c.Routes, logger, options)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if closer, ok := validationHandler.(io.Closer); ok {
		closer.Close()
	}
	if db != nil {
		if err = db.Seed(ctx, c.Routes); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("could not seed configuration")
		}
		snapshot, err = db.Load(ctx)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("could not load configuration")
		}
	}
	if !tlsEnabled && !offloaded {
		for _, r := range snapshot.Routes {
			if r.Auth != "" && r.Auth != "public" {
				cleanup()
				return nil, nil, fmt.Errorf("stored protected routes require TLS")
			}
		}
	}
	a, err := app.New(snapshot, store, options, logger, token, offloaded)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	uiDir := os.Getenv("GATEFORGE_UI_DIR")
	if uiDir == "" {
		uiDir = "web/dist"
	}
	if _, err = os.Stat(uiDir + "/index.html"); err == nil {
		a.UI = http.StripPrefix("/admin", app.StaticUI(uiDir))
	}
	refreshCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(refreshCtx, 3*time.Second)
				err := a.Reload(ctx)
				cancel()
				if err != nil {
					logger.Error("configuration refresh failed; retaining last valid configuration")
				}
			}
		}
	}()
	return a, func() { stop(); <-done; a.Close(); cleanup() }, nil
}
