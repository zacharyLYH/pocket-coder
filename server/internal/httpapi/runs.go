package httpapi

import "sync"

// runTracker serializes agent runs and remembers the in-flight thread per
// key: codemap keys by project, butler by butlerRunKey (one global
// assistant). The value drives per-row status so a remounted UI finds the
// live run instead of showing a dead panel.
type runTracker struct {
	mu sync.Mutex
	m  map[string]string
}

func newRunTracker() *runTracker { return &runTracker{m: map[string]string{}} }

// take holds the slot unless one is held. threadID may be "" (taken before
// the folder id is known); set repoints it after reserve.
func (t *runTracker) take(key, threadID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[key]; ok {
		return false
	}
	t.m[key] = threadID
	return true
}

// set repoints a held slot at the reserved thread id (fresh folder ids on
// new threads). No-op unless the slot is held.
func (t *runTracker) set(key, threadID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[key]; ok {
		t.m[key] = threadID
	}
}

// running reports the in-flight thread id. "" with true means taken
// before reserve; callers treat empty as "busy, thread unknown".
func (t *runTracker) running(key string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tid, ok := t.m[key]
	return tid, ok
}

func (t *runTracker) done(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, key)
}

var codemapRuns = newRunTracker()

// butlerRunKey is the single tracker key for butler: one assistant per
// instance, so one global slot.
const butlerRunKey = ""

var butlerRuns = newRunTracker()
