// evidence benchmarks the full TLS gateway and two disposable HTTP backends.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"gateforge/internal/experiment"
	"os"
	"runtime/debug"
	"time"
)

func main() {
	mode := flag.String("scenario", "baseline", "baseline, failure, latency, fairness or all")
	repeats := flag.Int("repeat", 1, "repetitions (1..10)")
	flag.Parse()
	if *repeats < 1 || *repeats > 10 {
		fmt.Fprintln(os.Stderr, "repeat must be 1..10")
		os.Exit(2)
	}
	modes := []string{*mode}
	if *mode == "all" {
		modes = []string{"baseline", "failure", "latency", "fairness"}
	}
	runs := []experiment.State{}
	for n := 0; n < *repeats; n++ {
		for _, mode := range modes {
			m := &experiment.Manager{}
			if err := m.Start(mode); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := m.Wait(ctx)
			cancel()
			m.Close()
			state := m.Snapshot()
			if err != nil || state.Error != "" {
				fmt.Fprintln(os.Stderr, "experiment failed")
				os.Exit(1)
			}
			runs = append(runs, state)
		}
	}
	revision := "unknown"
	modified := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				modified = setting.Value
			}
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(map[string]any{"schema_version": 1, "revision": revision, "modified": modified, "workload": "closed-loop TLS client -> gateway -> two loopback HTTP backends; 25 ms think time; full response bodies; latency and memory instrumentation enabled", "runs": runs}) != nil {
		os.Exit(1)
	}
}
