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

type fakeFactory struct {
	mu      sync.Mutex
	starts  int
	workers []*fakeWorker
}

func (f *fakeFactory) Start(context.Context, Config) (Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	w := &fakeWorker{}
	f.workers = append(f.workers, w)
	return w, nil
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

type slowFactory struct {
	mu     sync.Mutex
	starts int
}

func (f *slowFactory) Start(context.Context, Config) (Worker, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	// Hold the start open so all 20 callers pile onto the same in-flight
	// call instead of running sequentially.
	time.Sleep(50 * time.Millisecond)
	return &fakeWorker{}, nil
}

func (f *slowFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func TestManagerConcurrentEnsureStartsOnce(t *testing.T) {
	f := &slowFactory{}
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
