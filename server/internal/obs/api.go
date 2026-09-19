package obs

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Staged write path. Configure once at boot; Info/Warn/Error then write one
// project line to the observe file + stderr (plus the events sink when one
// is configured — production passes nil: events.log holds only the global
// audit for actions with no project, written by explicit Appends, never by
// fan-out). The project rides in ctx (see WithProject) — it never changes
// through a request's lifecycle, so call sites pass only key, message, data.
// Key identifies the use case and is shared by a use case's info and error
// lines alike (level carries the outcome); data is plain attrs (nil ok);
// error values stringify instead of marshaling as {}.
var (
	apiMu sync.Mutex
	apiSt *Store
	apiEv func(typ string, data map[string]any)
)

// Configure sets the file store and events sink for Info/Warn/Error.
// Nil store means stderr-only; nil sink means no events fan-out. Tests
// re-configure per test (the suite is sequential) and reset on cleanup.
func Configure(store *Store, events func(typ string, data map[string]any)) {
	apiMu.Lock()
	defer apiMu.Unlock()
	apiSt, apiEv = store, events
}

// projectKey carries the project id.
type projectKey struct{}

// WithProject puts the project id in ctx. Set once where the id becomes
// known (handler top, or service Create after parsing); everything
// downstream — service calls included — inherits it.
func WithProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, projectKey{}, project)
}

// ProjectOf returns the ctx project or "".
func ProjectOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	p, _ := ctx.Value(projectKey{}).(string)
	return p
}

// sourceKey carries the origin subsystem. Sources routing to the tail.
type sourceKey struct{}

// Sources are the origin facet: which subsystem wrote the line. Default is
// Server; preview routes set Preview.
const (
	SourceServer  = "server"
	SourcePreview = "preview"
)

// WithSource puts the origin subsystem in ctx. Set alongside WithProject
// where the area is known (preview route block); everything downstream
// inherits it.
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// SourceOf returns the ctx source or the server default.
func SourceOf(ctx context.Context) string {
	if ctx == nil {
		return SourceServer
	}
	s, _ := ctx.Value(sourceKey{}).(string)
	if s == "" {
		return SourceServer
	}
	return s
}

// Info writes key at info level.
func Info(ctx context.Context, key, msg string, data map[string]any) {
	emit(ctx, "info", key, msg, data)
}

// Warn writes key at warn level.
func Warn(ctx context.Context, key, msg string, data map[string]any) {
	emit(ctx, "warn", key, msg, data)
}

// Error writes key at error level: it lands in the errors inbox grouped by
// key+template(msg).
func Error(ctx context.Context, key, msg string, data map[string]any) {
	emit(ctx, "error", key, msg, data)
}

func emit(ctx context.Context, level, key, msg string, data map[string]any) {
	project := ProjectOf(ctx)
	if project == "" {
		return
	}
	trace := TraceOf(ctx)
	if trace == "" {
		trace = "none"
	}
	source := SourceOf(ctx)
	clean := make(map[string]any, len(data))
	for k, v := range data {
		if err, ok := v.(error); ok {
			v = err.Error()
		}
		clean[k] = v
	}
	if len(clean) == 0 {
		clean = nil
	}
	apiMu.Lock()
	st, fn := apiSt, apiEv
	apiMu.Unlock()
	if st != nil {
		st.append(project, trace, source, level, key, msg, time.Now(), clean)
	}
	if fn != nil {
		ed := make(map[string]any, len(clean)+2)
		for k, v := range clean {
			ed[k] = v
		}
		ed["trace"] = trace
		if _, ok := ed["id"]; !ok {
			ed["id"] = project
		}
		fn(key, ed)
	}
	// stderr stays terse: message plus routing keys (full data is in the
	// file and the event).
	switch level {
	case "error":
		slog.Error(msg, "project", project, "type", key, "trace", trace)
	case "warn":
		slog.Warn(msg, "project", project, "type", key, "trace", trace)
	default:
		slog.Info(msg, "project", project, "type", key, "trace", trace)
	}
}
