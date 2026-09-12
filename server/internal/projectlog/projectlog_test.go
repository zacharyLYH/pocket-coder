package projectlog

import (
	"sync"
	"testing"
)

func TestAppendReadCursor(t *testing.T) {
	m := NewManager(0)
	m.Append("a", "preview.start", "Preview started on :3000", map[string]any{"port": 3000})
	m.Append("a", "preview.open", "Preview opened", nil)
	m.Append("b", "preview.start", "other project", nil)

	got := m.Read("a", 0, 0)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("read a = %+v, want 2 entries ids 1,2", got)
	}
	if got[0].Message != "Preview started on :3000" || got[0].Type != "preview.start" {
		t.Fatalf("entry = %+v, want type+message", got[0])
	}
	if len(m.Read("b", 0, 0)) != 1 {
		t.Fatal("project b should be isolated")
	}
	if len(m.Read("ghost", 0, 0)) != 0 {
		t.Fatal("unknown project reads empty")
	}
	// cursor: only newer entries
	got = m.Read("a", 1, 0)
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("read after=1 = %+v, want [2]", got)
	}
	// limit
	got = m.Read("a", 0, 1)
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("read limit=1 = %+v, want [1]", got)
	}
}

func TestRingDropsOldest(t *testing.T) {
	m := NewManager(3)
	for i := 0; i < 5; i++ {
		m.Append("a", "t", "m", nil)
	}
	got := m.Read("a", 0, 0)
	if len(got) != 3 || got[0].ID != 3 || got[2].ID != 5 {
		t.Fatalf("ring = %+v, want ids 3..5", got)
	}
}

func TestNilSafe(t *testing.T) {
	var m *Manager
	m.Append("a", "t", "m", nil)
	if len(m.Read("a", 0, 0)) != 0 {
		t.Fatal("nil manager must read empty")
	}
}

func TestConcurrent(t *testing.T) {
	m := NewManager(0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.Append("a", "t", "m", nil)
				m.Read("a", 0, 10)
			}
		}()
	}
	wg.Wait()
	if len(m.Read("a", 0, 0)) != 400 {
		t.Fatalf("entries = %d, want 400", len(m.Read("a", 0, 0)))
	}
}
