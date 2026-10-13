package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/state"
)

// A delete that arrives with a canceled request context (a refreshed tab or
// a dropped mobile connection) must still finish: the handler detaches from
// r.Context(), so the remove runs and the record leaves state.json. Before
// the detach, the context-canceled remove aborted the pipeline and the
// project came back on the next refresh.
func TestDeleteDetachedFromCanceledRequest(t *testing.T) {
	d, md, pinOut, st := newProjectDeps(t)
	if err := st.Mutate(func(doc *state.Document) error {
		doc.Projects["abc"] = state.Project{Repo: "git@example.com:x.git"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Stop(mock.Anything, "pcoder-abc", mock.Anything).Return(nil)
	// The mock mimics the docker client: a canceled context aborts the remove.
	// Without the handler's detach, this fires and the delete never completes.
	md.EXPECT().Remove(mock.Anything, "pcoder-abc", true).RunAndReturn(
		func(ctx context.Context, _ string, _ bool) error {
			if ctx.Err() != nil {
				t.Errorf("remove called with canceled context: %v", ctx.Err())
				return ctx.Err()
			}
			return nil
		})
	md.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-home").Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, "pcoder-abc-repo").Return(nil)

	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/abc?scope=all", nil).WithContext(ctx)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete with canceled context: %d %s", rec.Code, rec.Body)
	}
	var found bool
	st.View(func(doc *state.Document) { _, found = doc.Projects["abc"] })
	if found {
		t.Fatal("record survived a canceled-context delete")
	}
}
