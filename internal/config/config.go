package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Route struct {
	Prefix       string     `json:"prefix"`
	Upstream     string     `json:"upstream,omitempty"`
	Upstreams    []string   `json:"upstreams,omitempty"`
	TimeoutMS    int        `json:"timeout_ms,omitempty"`
	MaxBodyBytes int64      `json:"max_body_bytes,omitempty"`
	Retries      int        `json:"retries,omitempty"`
	Health       *Health    `json:"health,omitempty"`
	Auth         string     `json:"auth,omitempty"`
	Scope        string     `json:"scope,omitempty"`
	RateLimit    *RateLimit `json:"rate_limit,omitempty"`
	Cache        *Cache     `json:"cache,omitempty"`
}

type Health struct {
	Path            string `json:"path"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutMS       int    `json:"timeout_ms"`
}

type RateLimit struct {
	Requests      int `json:"requests"`
	WindowSeconds int `json:"window_seconds"`
}
type Cache struct {
	TTLSeconds   int `json:"ttl_seconds"`
	MaxBodyBytes int `json:"max_body_bytes"`
}

type JWT struct {
	Issuer        string `json:"issuer"`
	Audience      string `json:"audience"`
	PublicKeyFile string `json:"public_key_file"`
}

type File struct {
	Routes []Route `json:"routes"`
	JWT    JWT     `json:"jwt,omitempty"`
}

func Read(path string) (File, error) {
	var result File
	f, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&result); err != nil {
		return result, fmt.Errorf("decode config: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("config must contain exactly one JSON object")
	}
	if len(result.Routes) == 0 {
		return result, fmt.Errorf("at least one route is required")
	}
	return result, nil
}
