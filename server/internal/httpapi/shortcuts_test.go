package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/project"
	"pcoder/internal/state"
)

func authedPatch(t *testing.T, h http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

// Saving the shortcuts list (commands and keys alike) persists it, and
// GET serves it back with no other command surface.
func TestShortcutsPatchGetRoundTrip(t *testing.T) {
	d, md, pinOut, st := newProjectDeps(t)
	h := New(d)
	const id = "x/hello"
	seedProject(t, st, id)
	cookie := loginCookie(t, h, pinOut)

	patch := `{"shortcuts":[
		{"id":"s-dev","alias":"dev","kind":"cmd","command":"npm run dev"},
		{"id":"s-intr","alias":"intr","kind":"keys","keys":"Ctrl-C"}]}`
	if rec := authedPatch(t, h, cookie, "/api/projects/"+url.PathEscape(id), patch); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %q, want 200", rec.Code, rec.Body)
	}

	var doc state.Document
	st.View(func(d *state.Document) { doc = *d })
	got := doc.Projects[id]
	if len(got.Shortcuts) != 2 {
		t.Fatalf("stored shortcuts = %+v, want 2 rows", got.Shortcuts)
	}

	md.EXPECT().Inspect(mock.Anything, project.ContainerName(id)).
		Return(docker.Container{Running: true, Status: "running"}, nil)
	rec := authedGet(t, h, cookie, "/api/projects/"+url.PathEscape(id))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		Shortcuts []state.Shortcut `json:"shortcuts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Shortcuts) != 2 || body.Shortcuts[0].Alias != "dev" || body.Shortcuts[1].Keys != "Ctrl-C" {
		t.Fatalf("get shortcuts = %+v, want dev + intr", body.Shortcuts)
	}
	if strings.Contains(rec.Body.String(), "quickCommands") {
		t.Fatalf("get still exposes quickCommands: %s", rec.Body)
	}
}

func TestShortcutsPatchValidation(t *testing.T) {
	d, _, pinOut, st := newProjectDeps(t)
	h := New(d)
	const id = "x/hello"
	seedProject(t, st, id)
	cookie := loginCookie(t, h, pinOut)
	path := "/api/projects/" + url.PathEscape(id)

	for _, body := range []string{
		`{"shortcuts":[{"id":"s-1","alias":"","kind":"cmd","command":"x"}]}`,
		`{"shortcuts":[{"id":"s-1","alias":"-bad","kind":"cmd","command":"x"}]}`,
		`{"shortcuts":[{"id":"s-1","alias":"a","kind":"bogus","command":"x"}]}`,
		`{"shortcuts":[{"id":"s-1","alias":"a","kind":"keys","keys":""}]}`,
		`{"shortcuts":[{"id":"s-1","alias":"a","kind":"cmd","command":"x"},{"id":"s-2","alias":"a","kind":"cmd","command":"y"}]}`,
	} {
		if rec := authedPatch(t, h, cookie, path, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s: got %d %q, want 400", body, rec.Code, rec.Body)
		}
	}
}
