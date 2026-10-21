package events

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendReadPagination(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.log"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()

	for i := 0; i < 5; i++ {
		if _, err := l.Append("test", map[string]any{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	all, err := l.Read(0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 5 || all[0].ID != 1 || all[4].ID != 5 {
		t.Fatalf("unexpected events: %+v", all)
	}

	page, err := l.Read(2, 2)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(page) != 2 || page[0].ID != 3 || page[1].ID != 4 {
		t.Fatalf("unexpected page: %+v", page)
	}

	tail, err := l.Read(4, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(tail) != 1 || tail[0].ID != 5 {
		t.Fatalf("unexpected tail: %+v", tail)
	}
}

func TestOpenContinuesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Append("a", nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer l2.Close()
	ev, err := l2.Append("b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != 2 {
		t.Fatalf("id = %d, want 2 (monotonic across restarts)", ev.ID)
	}
}

func TestResetTruncatesAndRestartsIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()
	for i := 0; i < 3; i++ {
		if _, err := l.Append("old", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Reset(); err != nil {
		t.Fatal(err)
	}
	evs, err := l.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("events survived Reset: %+v", evs)
	}
	ev, err := l.Append("after", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != 1 {
		t.Fatalf("id after Reset = %d, want 1", ev.ID)
	}
}

// A torn write (partial line, no trailing newline) or any other corrupt line
// must never fail a read — the good lines before it still come back, and the
// id counter continues past the damage.
func TestReadToleratesTornAndCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.log")

	l, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, typ := range []string{"a", "b"} {
		if _, err := l.Append(typ, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// a whole garbage line plus a torn write cut off mid-JSON
	if _, err := f.WriteString("\nnot json at all\n" + `{"id":3,"typ`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen over corrupt tail: %v", err)
	}
	defer l2.Close()
	evs, err := l2.Read(0, 0)
	if err != nil {
		t.Fatalf("read should skip corrupt lines: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "a" || evs[1].Type != "b" {
		t.Fatalf("events = %+v, want the two good ones", evs)
	}

	next, err := l2.Append("c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != 3 {
		t.Fatalf("id after corrupt tail = %d, want 3", next.ID)
	}
}
