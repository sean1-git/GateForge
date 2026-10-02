// Package experiment runs bounded experiments using disposable loopback servers.
// It cannot accept a destination URL or operate on the application's route pools.
package experiment

import (
	"context"
	"errors"
	"gateforge/internal/config"
	"gateforge/internal/explain"
	"gateforge/internal/gateway"
	"gateforge/internal/security"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Result struct {
	Retries int     `json:"retries"`
	Phase   string  `json:"phase"`
	Tenant  string  `json:"tenant"`
	Status  int     `json:"status"`
	MS      float64 `json:"ms"`
}
type Summary struct {
	Retries  int     `json:"retries"`
	Phase    string  `json:"phase"`
	Tenant   string  `json:"tenant"`
	Requests int     `json:"requests"`
	Success  int     `json:"success"`
	Rejected int     `json:"rejected"`
	Errors   int     `json:"errors"`
	Timeouts int     `json:"timeouts"`
	P50      float64 `json:"p50_ms"`
	P95      float64 `json:"p95_ms"`
	P99      float64 `json:"p99_ms"`
	Max      float64 `json:"max_ms"`
	RPS      float64 `json:"rps"`
}
type State struct {
	Mode           string                    `json:"mode"`
	Phase          string                    `json:"phase"`
	Running        bool                      `json:"running"`
	Error          string                    `json:"error,omitempty"`
	Started        time.Time                 `json:"started"`
	DurationMS     float64                   `json:"duration_ms"`
	GoVersion      string                    `json:"go_version"`
	OS             string                    `json:"os"`
	Arch           string                    `json:"arch"`
	CPUs           int                       `json:"cpus"`
	Procs          int                       `json:"gomaxprocs"`
	HeapBefore     uint64                    `json:"heap_before_bytes"`
	HeapPeak       uint64                    `json:"heap_peak_bytes"`
	HeapAfter      uint64                    `json:"heap_after_bytes"`
	Allocated      uint64                    `json:"allocated_bytes"`
	GoroutinesPeak int                       `json:"goroutines_peak"`
	Concurrency    security.ConcurrencyStats `json:"concurrency"`
	Backends       []gateway.BackendStatus   `json:"backends"`
	Summary        []Summary                 `json:"summary"`
	Recent         []Result                  `json:"recent"`
	Requests       []explain.Record          `json:"requests"`
}
type Manager struct {
	saved     []explain.Record
	seen      map[string]bool
	mu        sync.Mutex
	state     State
	rows      []Result
	phases    map[string]time.Time
	durations map[string]time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
	traces    *explain.Store
	limiter   *security.Concurrency
	handler   http.Handler
	allocated uint64
}

func (m *Manager) Start(mode string) error {
	if mode != "baseline" && mode != "failure" && mode != "latency" && mode != "fairness" {
		return errors.New("choose baseline, failure, latency or fairness")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Running {
		return errors.New("an experiment is already running")
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	m.state = State{Mode: mode, Phase: "starting", Running: true, Started: time.Now().UTC(), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Procs: runtime.GOMAXPROCS(0), HeapBefore: memory.HeapAlloc, HeapPeak: memory.HeapAlloc}
	m.allocated = memory.TotalAlloc
	m.rows = nil
	m.saved = nil
	m.seen = map[string]bool{}
	m.phases = map[string]time.Time{}
	m.durations = map[string]time.Duration{}
	m.traces = nil
	m.limiter = nil
	m.handler = nil
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.run(ctx, mode)
	return nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
func (m *Manager) Wait(ctx context.Context) error {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done == nil {
		return errors.New("no experiment")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}
func (m *Manager) phase(value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if previous, ok := m.phases[m.state.Phase]; ok {
		m.durations[m.state.Phase] += now.Sub(previous)
		delete(m.phases, m.state.Phase)
	}
	m.state.Phase = value
	m.phases[value] = now
}
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	if state.Started.IsZero() {
		return state
	}
	if state.Running {
		state.DurationMS = float64(time.Since(state.Started).Microseconds()) / 1000
	}
	state.Summary = []Summary{}
	state.Recent = []Result{}
	state.Requests = []explain.Record{}
	groups := map[string][]Result{}
	for _, row := range m.rows {
		groups[row.Phase+"\x00"+row.Tenant] = append(groups[row.Phase+"\x00"+row.Tenant], row)
	}
	names := []string{}
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rows := groups[name]
		s := Summary{Phase: rows[0].Phase, Tenant: rows[0].Tenant, Requests: len(rows)}
		times := []float64{}
		for _, r := range rows {
			s.Retries += r.Retries
			times = append(times, r.MS)
			switch {
			case r.Status >= 200 && r.Status < 300:
				s.Success++
			case r.Status == 429:
				s.Rejected++
			default:
				s.Errors++
			}
			if r.Status == 504 {
				s.Timeouts++
			}
		}
		sort.Float64s(times)
		percentile := func(p float64) float64 {
			index := int(float64(len(times))*p+0.999999) - 1
			if index < 0 {
				index = 0
			}
			return times[index]
		}
		s.P50 = percentile(.5)
		s.P95 = percentile(.95)
		s.P99 = percentile(.99)
		s.Max = times[len(times)-1]
		elapsed := m.durations[s.Phase]
		if start, ok := m.phases[s.Phase]; ok {
			elapsed += time.Since(start)
		}
		if elapsed > 0 {
			s.RPS = float64(s.Requests) / elapsed.Seconds()
		}
		state.Summary = append(state.Summary, s)
	}
	start := len(m.rows) - 20
	if start < 0 {
		start = 0
	}
	state.Recent = append(state.Recent, m.rows[start:]...)
	if m.traces != nil {
		state.Requests = append(state.Requests, m.saved...)
	}
	if m.limiter != nil {
		state.Concurrency = m.limiter.Snapshot()
	}
	if h, ok := m.handler.(interface {
		Backends() []gateway.BackendStatus
	}); ok {
		state.Backends = h.Backends()
	}
	return state
}

type backend struct {
	delay   atomic.Int64
	server  *httptest.Server
	address string
}

func (b *backend) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		timer := time.NewTimer(time.Duration(b.delay.Load()) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"status":"ok"}`)
}
func (b *backend) start() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if b.address != "" {
		if err == nil {
			listener.Close()
		}
		listener, err = net.Listen("tcp", b.address)
	}
	if err != nil {
		return err
	}
	b.address = listener.Addr().String()
	b.server = &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(b.serve)}}
	b.server.Start()
	return nil
}

type keys map[string]security.Key

func (k keys) LookupKey(_ context.Context, hash string) (security.Key, error) {
	v, ok := k[hash]
	if !ok {
		return v, security.ErrInvalidKey
	}
	return v, nil
}

type fixture struct {
	first, second *backend
	gateway       *httptest.Server
	handler       http.Handler
	limiter       *security.Concurrency
	traces        *explain.Store
	keys          [2]string
}

func newFixture(perTenant, timeoutMS int) (*fixture, error) {
	f := &fixture{first: &backend{}, second: &backend{}, limiter: security.NewConcurrency(4, perTenant), traces: &explain.Store{}}
	if err := f.first.start(); err != nil {
		return nil, err
	}
	if err := f.second.start(); err != nil {
		f.first.server.Close()
		return nil, err
	}
	reader := keys{}
	for i, tenant := range []string{"busy", "quiet"} {
		raw, err := security.GenerateKey()
		if err != nil {
			f.close()
			return nil, err
		}
		f.keys[i] = raw
		reader[security.Hash(raw)] = security.Key{ID: tenant, TenantID: tenant, Prefixes: []string{"/work"}, ExpiresAt: time.Now().Add(time.Hour)}
	}
	route := config.Route{Prefix: "/work", Upstreams: []string{f.first.server.URL, f.second.server.URL}, Auth: "api_key", TimeoutMS: timeoutMS, Retries: 1, Health: &config.Health{Path: "/healthz", IntervalSeconds: 1, TimeoutMS: 100}}
	handler, err := gateway.NewRoutesWithOptions([]config.Route{route}, slog.New(slog.NewTextHandler(io.Discard, nil)), gateway.Options{Auth: &security.Authenticator{Keys: reader}, Concurrency: f.limiter})
	if err != nil {
		f.close()
		return nil, err
	}
	f.handler = handler
	f.gateway = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.traces.Serve(w, r, 1, handler) }))
	deadline := time.Now().Add(time.Second)
	for !handler.(interface{ Ready() bool }).Ready() {
		if time.Now().After(deadline) {
			f.close()
			return nil, errors.New("demo backends did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f, nil
}
func (f *fixture) close() {
	if f.gateway != nil {
		f.gateway.Close()
	}
	if f.handler != nil {
		f.handler.(io.Closer).Close()
	}
	if f.first.server != nil {
		f.first.server.Close()
	}
	if f.second.server != nil {
		f.second.server.Close()
	}
}
func (m *Manager) attach(f *fixture) {
	m.mu.Lock()
	m.traces = f.traces
	m.limiter = f.limiter
	m.handler = f.handler
	m.mu.Unlock()
}
func (m *Manager) request(ctx context.Context, f *fixture, tenant int) {
	start := time.Now()
	m.mu.Lock()
	phase := m.state.Phase
	m.mu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", f.gateway.URL+"/work", nil)
	req.Header.Set("X-API-Key", f.keys[tenant])
	response, err := f.gateway.Client().Do(req)
	status := 0
	requestID := ""
	if err == nil {
		requestID = response.Header.Get(explain.Header)
		status = response.StatusCode
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if readErr != nil {
			status = 0
		}
	}
	row := Result{Phase: phase, Tenant: []string{"busy", "quiet"}[tenant], Status: status, MS: float64(time.Since(start).Microseconds()) / 1000}
	trace, found := f.traces.Find(requestID)
	for _, event := range trace.Events {
		if event.Stage == "retry" && event.Outcome == "retrying" {
			row.Retries++
		}
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	m.mu.Lock()
	category := phase + "/" + row.Tenant + "/normal"
	if row.Retries > 0 {
		category = phase + "/retry"
	}
	if status == 504 {
		category = phase + "/timeout"
	}
	if status == 429 || status == 503 {
		category = phase + "/rejected"
	}
	if found && !m.seen[category] && len(m.saved) < 36 {
		m.saved = append(m.saved, trace)
		m.seen[category] = true
	}
	if len(m.rows) < 2000 {
		m.rows = append(m.rows, row)
	}
	if memory.HeapAlloc > m.state.HeapPeak {
		m.state.HeapPeak = memory.HeapAlloc
	}
	g := runtime.NumGoroutine()
	if g > m.state.GoroutinesPeak {
		m.state.GoroutinesPeak = g
	}
	m.mu.Unlock()
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (m *Manager) traffic(ctx context.Context, f *fixture, workers int, d time.Duration) {
	run, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tenant := 0
			if i == workers-1 {
				tenant = 1
			}
			for run.Err() == nil {
				m.request(ctx, f, tenant)
				if !pause(run, 25*time.Millisecond) {
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
func (m *Manager) run(ctx context.Context, mode string) {
	defer func() {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		m.mu.Lock()
		if recover() != nil {
			m.state.Error = "isolated experiment failed"
		}
		if ctx.Err() != nil {
			m.state.Error = "experiment cancelled or timed out"
		}
		if started, ok := m.phases[m.state.Phase]; ok {
			m.durations[m.state.Phase] += time.Since(started)
			delete(m.phases, m.state.Phase)
		}
		m.state.DurationMS = float64(time.Since(m.state.Started).Microseconds()) / 1000
		m.state.Running = false
		m.state.HeapAfter = memory.HeapAlloc
		m.state.Allocated = memory.TotalAlloc - m.allocated
		m.cancel()
		close(m.done)
		m.mu.Unlock()
	}()
	fail := func() { m.mu.Lock(); m.state.Error = "could not start disposable loopback servers"; m.mu.Unlock() }
	if mode == "fairness" {
		for _, limit := range []int{4, 1} {
			if ctx.Err() != nil {
				return
			}
			f, err := newFixture(limit, 2000)
			if err != nil {
				fail()
				return
			}
			m.attach(f)
			f.first.delay.Store(80)
			f.second.delay.Store(80)
			if limit == 4 {
				m.phase("shared pool only")
			} else {
				m.phase("tenant protected")
			}
			m.traffic(ctx, f, 13, 2*time.Second)
			f.close()
		}
		return
	}
	f, err := newFixture(2, 150)
	if err != nil {
		fail()
		return
	}
	defer f.close()
	m.attach(f)
	m.phase("baseline")
	m.traffic(ctx, f, 2, 1500*time.Millisecond)
	if mode == "baseline" {
		return
	}
	if mode == "failure" {
		f.first.server.Close()
		m.phase("backend stopped")
	} else {
		f.first.delay.Store(350)
		m.phase("latency injected")
	}
	m.traffic(ctx, f, 2, 2500*time.Millisecond)
	if mode == "failure" {
		if f.first.start() != nil {
			fail()
			return
		}
	} else {
		f.first.delay.Store(0)
	}
	m.phase("recovery")
	m.traffic(ctx, f, 2, 2*time.Second)
}
