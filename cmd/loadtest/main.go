// loadtest is a bounded GET-only load generator for services you own.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	url := flag.String("url", "https://localhost:8443/catalog/items", "target URL")
	seconds := flag.Int("seconds", 10, "duration, 1..300 seconds")
	rate := flag.Int("rate", 10, "requests per second, 1..1000")
	workers := flag.Int("workers", 10, "concurrent workers, 1..100")
	ca := flag.String("ca", "", "optional CA PEM file")
	flag.Parse()
	if *seconds < 1 || *seconds > 300 || *rate < 1 || *rate > 1000 || *workers < 1 || *workers > 100 {
		return fmt.Errorf("invalid duration, rate, or worker limit")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = *workers
	if *ca != "" {
		data, err := os.ReadFile(*ca)
		if err != nil {
			return err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return fmt.Errorf("invalid CA file")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer transport.CloseIdleConnections()
	jobs := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	latencies := []float64{}
	successLatencies := []float64{}
	statuses := map[int]int{}
	failed := 0
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				start := time.Now()
				req, err := http.NewRequest("GET", *url, nil)
				if err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
					continue
				}
				if key := os.Getenv("GATEFORGE_LOAD_API_KEY"); key != "" {
					req.Header.Set("X-API-Key", key)
				}
				resp, err := client.Do(req)
				status := 0
				if err == nil {
					status = resp.StatusCode
					_, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				elapsed := time.Since(start).Seconds() * 1000
				mu.Lock()
				if err != nil {
					failed++
				}
				if status > 0 {
					statuses[status]++
				}
				latencies = append(latencies, elapsed)
				if err == nil && status >= 200 && status < 300 {
					successLatencies = append(successLatencies, elapsed)
				}
				mu.Unlock()
			}
		}()
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*seconds)*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second / time.Duration(*rate))
	scheduled, dropped := 0, 0
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			scheduled++
			select {
			case jobs <- struct{}{}:
			default:
				dropped++
			}
		}
	}
	close(jobs)
	wg.Wait()
	sort.Float64s(latencies)
	sort.Float64s(successLatencies)
	p95 := 0.0
	if len(latencies) > 0 {
		p95 = latencies[(len(latencies)-1)*95/100]
	}
	percentile := func(p int) float64 {
		if len(successLatencies) == 0 {
			return 0
		}
		return successLatencies[(len(successLatencies)-1)*p/100]
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"requests": len(latencies), "transport_errors": failed, "statuses": statuses, "p95_ms": p95, "success_p50_ms": percentile(50), "success_p95_ms": percentile(95), "success_p99_ms": percentile(99), "target_rps": *rate, "scheduled": scheduled, "dropped_at_generator": dropped, "successful_rps": float64(len(successLatencies)) / time.Since(start).Seconds(), "elapsed_seconds": time.Since(start).Seconds()})
}
