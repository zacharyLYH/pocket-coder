package httpapi

import (
	"errors"
	"net/http/httptest"
	"testing"

	"pcoder/internal/obs"
)

// TestObsFailAt pins the deferred guard's contract: silent while err is nil,
// and exactly one error log (with the msg's err attr merged into data) once
// err is set. This is the shared shape behind every handler's error prologue.
func TestObsFailAt(t *testing.T) {
	var got []map[string]any
	st := obs.NewStore(t.TempDir())
	t.Cleanup(st.Close)
	obs.Configure(st, func(typ string, data map[string]any) { got = append(got, data) })
	t.Cleanup(func() { obs.Configure(nil, nil) })

	r := httptest.NewRequest("GET", "/x", nil)
	r = r.WithContext(obs.WithProject(r.Context(), "o/r"))

	var err error
	obsFailAt(r, "k", "msg", &err, nil)()
	if len(got) != 0 {
		t.Fatalf("logged on nil err: %+v", got)
	}

	err = errors.New("boom")
	obsFailAt(r, "k", "msg", &err, map[string]any{"path": "a.txt"})()
	if len(got) != 1 {
		t.Fatalf("want 1 log, got %+v", got)
	}
	if got[0]["error"] != "boom" || got[0]["path"] != "a.txt" {
		t.Fatalf("attrs = %+v", got[0])
	}
}
