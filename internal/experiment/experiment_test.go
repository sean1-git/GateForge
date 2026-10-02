package experiment

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExperimentsUseRealRequestsAndRecover(t *testing.T) {
	for _, mode := range []string{"failure", "latency", "fairness"} {
		t.Run(mode, func(t *testing.T) {
			m := &Manager{}
			defer m.Close()
			if err := m.Start(mode); err != nil {
				t.Fatal(err)
			}
			if m.Start("baseline") == nil {
				t.Fatal("parallel experiment admitted")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := m.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			s := m.Snapshot()
			if s.Error != "" || s.Running || len(s.Summary) == 0 || s.Concurrency.Active != 0 || s.Concurrency.Peak > 4 {
				t.Fatal("experiment failed", s.Error)
			}
			quiet, timeout, recovery, retries := 0, 0, 0, 0
			for _, r := range s.Summary {
				if r.Phase == "tenant protected" && r.Tenant == "quiet" {
					quiet += r.Success
					if r.Errors+r.Rejected > 0 {
						t.Fatal("quiet tenant rejected with reserved capacity")
					}
				}
				timeout += r.Timeouts
				retries += r.Retries
				if r.Phase == "recovery" {
					recovery += r.Success
				}
			}
			if mode == "fairness" && quiet == 0 {
				t.Fatal("fairness not demonstrated")
			}
			if mode == "latency" && timeout == 0 {
				t.Fatal("no actual timeout")
			}
			if mode != "fairness" && recovery == 0 {
				t.Fatal("no recovery")
			}
			if mode == "failure" && retries == 0 {
				t.Fatal("no actual retry recorded")
			}
			data, _ := json.Marshal(s)
			if strings.Contains(string(data), "127.0.0.1") || strings.Contains(string(data), "gf_") {
				t.Fatal("fixture topology or credentials exposed")
			}
		})
	}
}
