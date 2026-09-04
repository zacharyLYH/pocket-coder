package preview

import (
	"context"
	"errors"
	"sync"
	"testing"
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

func TestManagerConcurrentEnsureClosesLosingWorker(t *testing.T) {
	f := &fakeFactory{}
	m := NewManager(f)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Ensure(context.Background(), Config{ProjectID: "p1", ContainerID: "container-p1"}); err != nil {
				t.Errorf("ensure: %v", err)
			}
		}()
	}
	wg.Wait()

	f.mu.Lock()
	starts := f.starts
	workers := append([]*fakeWorker(nil), f.workers...)
	f.mu.Unlock()
	if starts < 1 {
		t.Fatal("factory was never called")
	}
	closed := 0
	for _, w := range workers {
		w.mu.Lock()
		closed += w.closed
		w.mu.Unlock()
	}
	if closed != starts-1 {
		t.Fatalf("closed=%d starts=%d; want every losing worker closed", closed, starts)
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
