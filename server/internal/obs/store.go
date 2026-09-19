package obs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxFile is the active per-project log cap; one .1 backup is kept.
const MaxFile = 4 << 20

const (
	// chanCap bounds memory when bursty writers outrun disk: queueing never
	// blocks, overflow drops and counts.
	chanCap = 1024
	// flushInterval batches disk writes: at most ~1s of lines sit in
	// memory before fsync. Readers flush first, so tails stay fresh.
	flushInterval = time.Second
)

// item is one queued line awaiting flush.
type item struct {
	project string
	line    []byte
}

// openFile is one flush-loop-owned handle: size is tracked here so the
// loop never stats on the hot path.
type openFile struct {
	f    *os.File
	w    *bufio.Writer
	size int64
}

// Store is the buffered per-project file writer: observe/<escaped>.log.jsonl
// (+.1) under dataDir. One flush-loop goroutine owns every file: queueing is
// a non-blocking chan send (drop+count past 1024), the loop drains to bufio
// every second with fsync, and rotates past 4MB. seq is per-project atomic;
// file size is loop-owned.
//
// Locking is two-mutex by design, never nested: wmu serializes all file I/O
// (loop flush, read-triggered flush, delete, close); mu guards the seq map
// and the errors cache. The seq resume scan takes no locks — max is
// idempotent under a racing rotation, so a missed line can only reuse a seq
// that still sorts correctly.
type Store struct {
	dir   string
	mu    sync.Mutex // seq, errs
	seq   map[string]*atomic.Int64
	files map[string]*openFile // wmu only
	wmu   sync.Mutex
	ch    chan item
	stop  chan struct{}
	stops chan struct{}
	once  sync.Once
	// dropped counts lines never persisted (chan overflow or disk errors).
	dropped atomic.Int64
	// flushErrs counts disk failures since the last stderr note.
	flushErrs atomic.Int64
	// reported tracks flushErrs already reported, so a dead disk notes once
	// per new failure instead of every second.
	reported atomic.Int64
	now      func() time.Time
	errs     map[string]cachedErrs
}

type cachedErrs struct {
	at time.Time
	gs []Group
}

// NewStore creates the observe dir and starts the flush loop. Close it
// when done (tests via t.Cleanup); the server store lives forever.
func NewStore(dataDir string) *Store {
	dir := filepath.Join(dataDir, "observe")
	_ = os.MkdirAll(dir, 0o700)
	s := &Store{
		dir: dir, seq: map[string]*atomic.Int64{}, files: map[string]*openFile{},
		ch: make(chan item, chanCap), stop: make(chan struct{}), stops: make(chan struct{}),
		now: time.Now, errs: map[string]cachedErrs{},
	}
	go s.loop()
	return s
}

// Close stops the flush loop (draining the remainder) and closes files.
// Idempotent; queued lines after Close drop on the next flush attempt.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.stop)
		<-s.stops
		s.wmu.Lock()
		defer s.wmu.Unlock()
		for p, of := range s.files {
			_ = of.w.Flush()
			_ = of.f.Sync()
			_ = of.f.Close()
			delete(s.files, p)
		}
	})
}

// Dropped reports lines never persisted (overflow or disk errors).
func (s *Store) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

func (s *Store) loop() {
	defer close(s.stops)
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.flush()
			return
		case <-t.C:
			s.flush()
		}
	}
}

// flush drains queued lines to disk: bufio writes, one flush+fsync per
// project per cycle, rotate past MaxFile. Called by the 1s loop and
// synchronously by Read/DeleteProject so tails stay fresh.
func (s *Store) flush() {
	var batch []item
	for {
		select {
		case it := <-s.ch:
			batch = append(batch, it)
		default:
			goto drained
		}
	}
drained:
	if len(batch) == 0 {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	seen := map[string]bool{}
	for _, it := range batch {
		of := s.openLocked(it.project)
		if of == nil {
			s.dropped.Add(1)
			s.flushErrs.Add(1)
			continue
		}
		if _, err := of.w.Write(it.line); err != nil {
			s.dropped.Add(1)
			s.flushErrs.Add(1)
			continue
		}
		of.size += int64(len(it.line))
		seen[it.project] = true
	}
	for project := range seen {
		of := s.files[project]
		if of == nil {
			continue
		}
		_ = of.w.Flush()
		_ = of.f.Sync()
		if of.size > MaxFile {
			s.rotateLocked(project)
		}
	}
	// Disk failures note directly to stderr (never via slog: the handler
	// routes records back here, so that would recurse).
	if errs, rep := s.flushErrs.Load(), s.reported.Load(); errs > rep {
		s.reported.Store(errs)
		fmt.Fprintf(os.Stderr, "obs: %d lines lost to disk failures\n", errs-rep)
	}
}

// openLocked returns the loop-owned handle, resuming size from disk.
func (s *Store) openLocked(project string) *openFile {
	if of, ok := s.files[project]; ok {
		return of
	}
	f, err := os.OpenFile(s.path(project), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	of := &openFile{f: f, w: bufio.NewWriterSize(f, 64<<10), size: size}
	s.files[project] = of
	return of
}

// rotateLocked closes cur, shifts it to .1 (dropping the old .1), and lets
// the next open recreate cur. seq keeps climbing across rotations so the
// merged read stays ordered.
func (s *Store) rotateLocked(project string) {
	of := s.files[project]
	if of == nil {
		return
	}
	_ = of.w.Flush()
	_ = of.f.Sync()
	_ = of.f.Close()
	delete(s.files, project)
	p := s.path(project)
	_ = os.Remove(p + ".1")
	_ = os.Rename(p, p+".1")
}

func sanitize(id string) string {
	s := strings.ToLower(id)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/':
			b.WriteByte('-')
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "project"
	}
	return out
}

func (s *Store) path(project string) string {
	return filepath.Join(s.dir, sanitize(project)+".log.jsonl")
}

// append assigns the next per-file seq and queues one envelope line.
// Non-blocking: past 1024 queued it drops and counts. ts comes from the
// slog record so file time matches stderr time.
func (s *Store) append(project, trace, source, level, typ, msg string, ts time.Time, data map[string]any) Entry {
	seq := s.counter(project).Add(1)
	e := Entry{TS: ts.UTC(), Seq: seq, Project: project, Trace: trace, Source: source, Level: level, Type: typ, Msg: msg, Attrs: data}
	line, _ := json.Marshal(e)
	select {
	case s.ch <- item{project: project, line: append(line, '\n')}:
	default:
		s.dropped.Add(1)
	}
	return e
}

// counter returns the per-project seq, resuming from disk on first use.
func (s *Store) counter(project string) *atomic.Int64 {
	s.mu.Lock()
	if c, ok := s.seq[project]; ok {
		s.mu.Unlock()
		return c
	}
	s.mu.Unlock()
	max := s.maxSeq(project)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.seq[project]; ok {
		return c // lost the race; reuse
	}
	c := &atomic.Int64{}
	c.Store(max)
	s.seq[project] = c
	return c
}

func (s *Store) maxSeq(project string) int64 {
	var max int64
	for _, p := range []string{s.path(project) + ".1", s.path(project)} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Seq > max {
				max = e.Seq
			}
		}
		_ = f.Close()
	}
	return max
}

// Read returns filtered lines with first/last seq cursors. before>0 pages
// older (seq<before, oldest chunk tail), after>0 tails newer (seq>after).
// It flushes queued lines first, and the file scan holds the write lock, so
// a tail can neither miss nor double-count across a racing rotation.
func (s *Store) Read(project string, after, before int64, limit int, level, source, typ, trace, q string) (logs []Entry, firstSeq, lastSeq int64) {
	if s == nil {
		return nil, 0, 0
	}
	s.flush()
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	q = strings.ToLower(q)
	var all []Entry
	s.wmu.Lock()
	for _, p := range []string{s.path(project) + ".1", s.path(project)} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && match(e, level, source, typ, trace, q) {
				all = append(all, e)
			}
		}
		_ = f.Close()
	}
	s.wmu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	if before > 0 {
		var older []Entry
		for _, e := range all {
			if e.Seq < before {
				older = append(older, e)
			}
		}
		all = older
		if len(all) > limit {
			all = all[len(all)-limit:]
		}
	} else if after > 0 {
		var newer []Entry
		for _, e := range all {
			if e.Seq > after {
				newer = append(newer, e)
				if len(newer) >= limit {
					break
				}
			}
		}
		all = newer
	} else if len(all) > limit {
		all = all[len(all)-limit:]
	}
	if len(all) == 0 {
		return []Entry{}, 0, 0
	}
	return all, all[0].Seq, all[len(all)-1].Seq
}

// matchType reports whether got is in the comma-separated want set. A
// single type still matches exactly, so existing callers are unaffected;
// the audit filter passes several milestone types at once.
func matchType(got, want string) bool {
	if !strings.Contains(want, ",") {
		return got == want
	}
	for _, w := range strings.Split(want, ",") {
		if strings.TrimSpace(w) == got {
			return true
		}
	}
	return false
}

func match(e Entry, level, source, typ, trace, q string) bool {
	if level != "" && level != "all" && e.Level != level {
		return false
	}
	if source != "" && source != "all" && e.Source != source {
		return false
	}
	if typ != "" && !matchType(e.Type, typ) {
		return false
	}
	if trace != "" && e.Trace != trace {
		return false
	}
	if q != "" {
		hay := strings.ToLower(e.Msg + " " + e.Type)
		if a, _ := json.Marshal(e.Attrs); len(a) > 0 {
			hay += " " + strings.ToLower(string(a))
		}
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// Errors groups level==error lines by type+template, top 20 by count (5s cache).
func (s *Store) Errors(project string) []Group {
	if s == nil {
		return []Group{}
	}
	s.mu.Lock()
	if c, ok := s.errs[project]; ok && s.now().Sub(c.at) < 5*time.Second {
		gs := c.gs
		s.mu.Unlock()
		return gs
	}
	s.mu.Unlock()
	logs, _, _ := s.Read(project, 0, 0, 1000, "error", "", "", "", "")
	byKey := map[string]*Group{}
	for _, e := range logs {
		k := e.Type + "|" + Template(e.Msg)
		g := byKey[k]
		if g == nil {
			g = &Group{Key: k, Type: e.Type, FirstSeen: e.TS, SampleTrace: e.Trace, SampleMsg: e.Msg}
			byKey[k] = g
		}
		g.Count++
		g.LastSeen = e.TS
		if e.TS.Before(g.FirstSeen) {
			g.FirstSeen = e.TS
		}
	}
	gs := make([]Group, 0, len(byKey))
	for _, g := range byKey {
		gs = append(gs, *g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Count > gs[j].Count })
	if len(gs) > 20 {
		gs = gs[:20]
	}
	if gs == nil {
		gs = []Group{}
	}
	s.mu.Lock()
	s.errs[project] = cachedErrs{at: s.now(), gs: gs}
	s.mu.Unlock()
	return gs
}

// DeleteProject flushes, then removes a project's observe files and seq. A
// write racing the delete may recreate an empty file; readers treat unknown
// projects as empty, so this self-heals on the next delete.
func (s *Store) DeleteProject(project string) {
	if s == nil {
		return
	}
	s.flush()
	s.wmu.Lock()
	if of, ok := s.files[project]; ok {
		_ = of.w.Flush()
		_ = of.f.Sync()
		_ = of.f.Close()
		delete(s.files, project)
	}
	p := s.path(project)
	_ = os.Remove(p)
	_ = os.Remove(p + ".1")
	s.wmu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seq, project)
	delete(s.errs, project)
}
