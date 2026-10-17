package preview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeWorker struct {
	mu     sync.Mutex
	closed int
}

func (w *fakeWorker) Endpoint() Endpoint {
	return Endpoint{CDP: "private-cdp", Display: "private-display"}
}
func (w *fakeWorker) Close(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed++
	return nil
}

func (w *fakeWorker) closedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

type fakeFactory struct {
	mu      sync.Mutex
	starts  int
	delay   time.Duration
	workers []*fakeWorker
	byID    map[string]*fakeWorker
}

// worker returns the worker the factory handed out for a project: the fake
// must remember which is which now that tests assert on per-project Close.
func (f *fakeFactory) worker(projectID string) *fakeWorker {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byID[projectID]
}

func (f *fakeFactory) Start(_ context.Context, cfg Config) (Worker, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	// A nonzero delay holds the start open so concurrent callers pile onto
	// the same in-flight call instead of running sequentially.
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	w := &fakeWorker{}
	f.mu.Lock()
	f.workers = append(f.workers, w)
	if f.byID == nil {
		f.byID = map[string]*fakeWorker{}
	}
	f.byID[cfg.ProjectID] = w
	f.mu.Unlock()
	return w, nil
}

func (f *fakeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func TestManagerEnsuresOneWorkerPerProject(t *testing.T) {
	f := &fakeFactory{}
	m := NewManager(f)

	cfg := Config{ProjectID: "p1", ContainerID: "container-p1"}
	want, err := m.Ensure(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Ensure(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || f.starts != 1 {
		t.Fatalf("worker=%p/%p starts=%d; want same worker and one start", got, want, f.starts)
	}
}

func TestManagerConcurrentEnsureStartsOnce(t *testing.T) {
	f := &fakeFactory{delay: 50 * time.Millisecond}
	m := NewManager(f)
	cfg := Config{ProjectID: "p1", ContainerID: "container-p1"}
	results := make([]Worker, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := m.Ensure(context.Background(), cfg)
			if err != nil {
				t.Errorf("ensure: %v", err)
				return
			}
			results[i] = w
		}()
	}
	wg.Wait()

	for i, w := range results {
		if w == nil || w != results[0] {
			t.Fatalf("result %d got worker %p; want shared worker %p", i, w, results[0])
		}
	}
	if f.count() != 1 {
		t.Fatalf("starts=%d; want exactly one shared start", f.count())
	}
	if got, err := m.Get("p1"); err != nil || got != results[0] {
		t.Fatalf("manager holds %p (err %v); want shared worker %p", got, err, results[0])
	}
}

// Close is what reclaims containers: a preview sidecar and its relay are
// standalone docker containers, so the worker's Close is the ONLY thing that
// removes them. Stop must close exactly once, a second Stop must not close
// again, and Manager.Close must reclaim whatever is left.
func TestManagerStopClosesWorkerExactlyOnce(t *testing.T) {
	f := &fakeFactory{}
	m := NewManager(f)
	ctx := context.Background()
	for _, id := range []string{"p1", "p2"} {
		if _, err := m.Ensure(ctx, Config{ProjectID: id, ContainerID: "container-" + id}); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Stop(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got := f.worker("p1").closedCount(); got != 1 {
		t.Fatalf("Stop closed p1's worker %d times, want 1 (else the sidecar leaks)", got)
	}
	if !errors.Is(m.Stop(ctx, "p1"), ErrNotFound) {
		t.Fatal("second stop should report not found")
	}
	if got := f.worker("p1").closedCount(); got != 1 {
		t.Fatalf("second Stop closed again (%d), want exactly 1", got)
	}

	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.worker("p2").closedCount(); got != 1 {
		t.Fatalf("Manager.Close closed p2's worker %d times, want 1", got)
	}
}

func TestManagerStopAndCloseAreIdempotent(t *testing.T) {
	f := &fakeFactory{}
	m := NewManager(f)
	if _, err := m.Ensure(context.Background(), Config{ProjectID: "p1", ContainerID: "container-p1"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(m.Stop(context.Background(), "p1"), ErrNotFound) {
		t.Fatal("second stop should report not found")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal("close should be idempotent")
	}
	if _, err := m.Ensure(context.Background(), Config{ProjectID: "p2", ContainerID: "container-p2"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("ensure after close = %v, want ErrClosed", err)
	}
}

func TestManagerRejectsInvalidConfig(t *testing.T) {
	m := NewManager(&fakeFactory{})
	for _, cfg := range []Config{{}, {ContainerID: "container-p1"}, {ProjectID: "p1"}} {
		if _, err := m.Ensure(context.Background(), cfg); err == nil {
			t.Fatalf("config %+v unexpectedly accepted", cfg)
		}
	}
}

func TestManagerTokenMintNoRotateTouchSweep(t *testing.T) {
	m := NewManager(&fakeFactory{})
	cfg := Config{ProjectID: "p1", ContainerID: "container-p1"}
	if _, err := m.Ensure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t1, _, ok := m.TokenOrMint("p1")
	if !ok || len(t1) != 32 {
		t.Fatalf("token = %q ok=%v, want 32 hex chars", t1, ok)
	}
	if _, err := m.Ensure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if t2, _, _ := m.TokenOrMint("p1"); t2 != t1 {
		t.Fatalf("re-Ensure rotated %q -> %q", t1, t2)
	}
	if !m.CheckToken("p1", t1) || m.CheckToken("p1", "wrong") || m.CheckToken("p1", "") {
		t.Fatal("CheckToken mismatch")
	}
	base := time.Now()
	m.now = func() time.Time { return base.Add(time.Minute) }
	m.Touch("p1") // invalid tokens never touch; Touch needs a live token
	m.Sweep(base.Add(2*time.Minute), 90*time.Second)
	if _, _, ok := m.TokenOrMint("p1"); !ok {
		t.Fatal("touched token swept too early")
	}
	m.Sweep(base.Add(5*time.Minute), 90*time.Second)
	tok, minted, ok := m.TokenOrMint("p1")
	if !ok || tok == t1 || len(tok) != 32 {
		t.Fatalf("TokenOrMint = %q ok=%v, want fresh rotation", tok, ok)
	}
	if !minted {
		t.Fatal("post-sweep mint did not report minted")
	}
}

func TestManagerStopDeletesTokenRestartForgets(t *testing.T) {
	m := NewManager(&fakeFactory{})
	cfg := Config{ProjectID: "p1", ContainerID: "container-p1"}
	if _, err := m.Ensure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	t1, _, _ := m.TokenOrMint("p1")
	if err := m.Stop(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}
	if m.CheckToken("p1", t1) {
		t.Fatal("token survived Stop")
	}
	if _, err := m.Ensure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if t2, _, _ := m.TokenOrMint("p1"); t2 == t1 {
		t.Fatal("restart reused pre-close token")
	}
	fresh := NewManager(&fakeFactory{})
	if fresh.CheckToken("p1", t1) {
		t.Fatal("fresh manager remembers tokens")
	}
}

// TestManagerSweepStopsIdleWorker pins the idle-container cleanup that
// StartSweeper performs: when Sweep removes a project's token after silence,
// Stop must also close the worker (which closes the sidecar container).
func TestManagerSweepStopsIdleWorker(t *testing.T) {
	f := &fakeFactory{}
	m := NewManager(f)
	cfg := Config{ProjectID: "p1", ContainerID: "container-p1"}
	if _, err := m.Ensure(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	fw := f.worker("p1")
	if fw == nil {
		t.Fatal("fake worker not registered")
	}

	// Touch sets lastSeen on the mock clock so Sweep can age it past silence.
	base := time.Now()
	m.now = func() time.Time { return base.Add(5 * time.Minute) }
	m.Touch("p1")

	// Sweep at 10 min with a 1-min silence threshold: the 5-min gap qualifies.
	swept := m.Sweep(base.Add(10*time.Minute), time.Minute)
	if len(swept) != 1 || swept[0] != "p1" {
		t.Fatalf("swept = %v, want [p1]", swept)
	}
	// Token is gone after sweep, but the worker (and its container) still live.
	if m.CheckToken("p1", "anything") {
		t.Fatal("token survived Sweep")
	}
	if _, err := m.Get("p1"); err != nil {
		t.Fatalf("worker gone before Stop: %v", err)
	}

	// StartSweeper calls Stop on each swept project — that must close the worker.
	if err := m.Stop(context.Background(), "p1"); err != nil {
		t.Fatalf("Stop after Sweep: %v", err)
	}
	if fw.closedCount() != 1 {
		t.Fatalf("worker Close count = %d, want 1", fw.closedCount())
	}
	if _, err := m.Get("p1"); err == nil {
		t.Fatal("worker still live after Stop")
	}
}