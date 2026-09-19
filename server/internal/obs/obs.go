// Package obs is per-project observability: one parseable JSONL log per
// project (persisted, capped, searchable) plus trace + error-group helpers.
//
// There is no logging API here on purpose — call sites use plain slog and
// the Handler routes on record attrs (see its contract). obs only owns the
// file writer, the read/projection side, and trace plumbing.
//
// Contract worth knowing: file writes are buffered ~1s (Read flushes, so
// tails stay fresh); a crash loses at most 1s; overflow past 1024 queued
// lines drops and counts (Dropped); seq can gap on drops — cursors are
// gap-safe; rotation keeps seq climbing across files.
package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"time"
)

// Entry is the one JSON shape everywhere (single-line JSONL).
type Entry struct {
	TS      time.Time      `json:"ts"`
	Seq     int64          `json:"seq"`
	Project string         `json:"project"`
	Trace   string         `json:"trace"`
	Source  string         `json:"source"`
	Level   string         `json:"level"`
	Type    string         `json:"type"`
	Msg     string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// ResourceSample is one stats poll; no persistence (FE keeps last 60).
type ResourceSample struct {
	TS         time.Time `json:"ts"`
	CPUPercent float64   `json:"cpuPercent"`
	MemUsed    uint64    `json:"memUsed"`
	MemLimit   uint64    `json:"memLimit"`
	NetRX      uint64    `json:"netRx"`
	NetTX      uint64    `json:"netTx"`
	BlockR     uint64    `json:"blockR"`
	BlockW     uint64    `json:"blockW"`
	PIDs       int       `json:"pids"`
	DiskUsed   uint64    `json:"diskUsed"`
	DiskTotal  uint64    `json:"diskTotal"`
	State      string    `json:"state"`
}

// Group is one errors-inbox row.
type Group struct {
	Key         string    `json:"key"`
	Type        string    `json:"type"`
	Count       int       `json:"count"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	SampleTrace string    `json:"sampleTrace"`
	SampleMsg   string    `json:"sampleMsg"`
}

// CpuPercent mirrors the docker calc so it is unit-testable without an engine.
func CpuPercent(cpuDelta, sysDelta float64, cpus int) float64 {
	if sysDelta <= 0 || cpus <= 0 {
		return 0
	}
	return cpuDelta / sysDelta * float64(cpus) * 100
}

// Template strips volatile tokens so equal failures group together.
func Template(msg string) string {
	s := ipRe.ReplaceAllString(msg, "<ip>")
	s = hexRe.ReplaceAllString(s, "<hex>")
	return numRe.ReplaceAllString(s, "<n>")
}

var (
	ipRe  = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	hexRe = regexp.MustCompile(`\b[0-9a-fA-F]{7,64}\b`)
	numRe = regexp.MustCompile(`\b\d+\b`)
)

// traceKey carries the OTel-lite trace.
type traceKey struct{}

// WithTrace puts trace in ctx; TraceOf reads it.
func WithTrace(ctx context.Context, trace string) context.Context {
	return context.WithValue(ctx, traceKey{}, trace)
}

// TraceOf returns the ctx trace or "".
func TraceOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	t, _ := ctx.Value(traceKey{}).(string)
	return t
}

// NewTrace mints hex16.
func NewTrace() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// Middleware mints a trace per request when missing.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := r.Header.Get("X-Trace")
		if t == "" {
			t = NewTrace()
		}
		next.ServeHTTP(w, r.WithContext(WithTrace(r.Context(), t)))
	})
}
