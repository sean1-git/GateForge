package main

import (
	"context"
	"fmt"
	"gateforge/internal/adminauth"
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
	"net/mail"
	"os"
	"strings"
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
	if db != nil {
		instance, idErr := adminauth.Random()
		if idErr != nil {
			cleanup()
			return nil, nil, idErr
		}
		options.Metrics.EnablePersistence(db, instance)
	}
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
	mode := os.Getenv("GATEFORGE_ADMIN_AUTH")
	if mode != "" && mode != "token" && mode != "google" && mode != "token_session" {
		a.Close()
		cleanup()
		return nil, nil, fmt.Errorf("GATEFORGE_ADMIN_AUTH must be token, token_session or google")
	}
	if mode == "token_session" {
		if db == nil || redisStore == nil || (!tlsEnabled && !offloaded) {
			a.Close()
			cleanup()
			return nil, nil, fmt.Errorf("administrator sessions require PostgreSQL, Redis and HTTPS")
		}
		sessions := &adminauth.Sessions{Store: db, Origin: os.Getenv("GATEFORGE_PUBLIC_URL")}
		login, loginErr := adminauth.NewTokenLogin(sessions, token, logger)
		if loginErr != nil {
			a.Close()
			cleanup()
			return nil, nil, loginErr
		}
		a.AdminSessions = sessions
		a.AdminLogin = redisStore.WrapGlobal(config.Route{Prefix: "/admin/auth/token", RateLimit: &config.RateLimit{Requests: 10, WindowSeconds: 60}}, login)
	}
	if mode == "google" {
		if db == nil || redisStore == nil || (!tlsEnabled && !offloaded) {
			a.Close()
			cleanup()
			return nil, nil, fmt.Errorf("Google administrator sign-in requires PostgreSQL, Redis and HTTPS")
		}
		allowed := map[string]bool{}
		for _, entry := range strings.Split(os.Getenv("GATEFORGE_ADMIN_EMAILS"), ",") {
			email := strings.ToLower(strings.TrimSpace(entry))
			address, parseErr := mail.ParseAddress(email)
			if parseErr != nil || address.Address != email || len(email) > 254 {
				a.Close()
				cleanup()
				return nil, nil, fmt.Errorf("GATEFORGE_ADMIN_EMAILS must contain explicit email addresses")
			}
			allowed[email] = true
		}
		if len(allowed) > 25 {
			a.Close()
			cleanup()
			return nil, nil, fmt.Errorf("administrator allowlist exceeds 25 accounts")
		}
		sessions := &adminauth.Sessions{Store: db, Origin: os.Getenv("GATEFORGE_PUBLIC_URL"), AllowedEmails: allowed}
		login, loginErr := adminauth.NewGoogle(ctx, sessions, db, os.Getenv("GATEFORGE_GOOGLE_CLIENT_ID"), os.Getenv("GATEFORGE_GOOGLE_CLIENT_SECRET"), logger)
		if loginErr != nil {
			a.Close()
			cleanup()
			return nil, nil, loginErr
		}
		a.AdminSessions = sessions
		// Bound unauthenticated login writes and code exchanges across instances.
		a.AdminLogin = redisStore.Wrap(config.Route{Prefix: "/admin/auth", RateLimit: &config.RateLimit{Requests: 30, WindowSeconds: 60}}, login)
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
	return a, func() {
		stop()
		<-done
		a.Close()
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer flushCancel()
		if options.Metrics.Flush(flushCtx) != nil {
			logger.Error("final metrics persistence failed")
		}
		cleanup()
	}, nil
}
