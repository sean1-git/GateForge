package main

import (
	"fmt"
	"gateforge/internal/config"
	"log/slog"
	"net/http"

	"gateforge/internal/gateway"
)

func configuredHandler(configPath, upstream string, upstreamSet bool, logger *slog.Logger) (http.Handler, error) {
	if configPath == "" {
		return gateway.New(upstream, logger)
	}
	if upstreamSet {
		return nil, fmt.Errorf("-config and -upstream cannot be used together")
	}
	c, err := config.Read(configPath)
	if err != nil {
		return nil, err
	}
	return gateway.NewRoutes(c.Routes, logger)
}
