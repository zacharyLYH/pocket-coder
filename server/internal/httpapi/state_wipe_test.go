package httpapi

import (
	"context"
	"net/http"
	"testing"

	"pcoder/internal/push"
	"pcoder/internal/state"
)

// TestDeleteStateWipesGlobalData proves the settings wipe clears everything
// outside the project pipeline: butler threads, push credentials, the audit
// log, and AI models. Login + server key survive.
func TestDeleteStateWipesGlobalData(t *testing.T) {
	d, _, st := newSessionDeps(t)

	ps, err := push.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.Push = ps
	oldKey := ps.PublicKey()
	if err := ps.Subscribe(context.Background(), push.Subscription{Endpoint: "https://fcm.googleapis.com/x"}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := d.Butler.ReserveNewThread("", "keep this?", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Events.Append("old", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SSHKeys.EnsureKeypair(); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(doc *state.Document) error {
		doc.User.Email = "me@example.com"
		doc.AIModels = []state.AIModel{{ID: "m1", BaseURL: "https://example.com", APIKey: "secret", Model: "m"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h := New(d)
	cookie := loginCookie(t, h)
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/state"); rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/state = %d: %s", rec.Code, rec.Body.String())
	}

	if got, err := d.Butler.List(""); err != nil || len(got) != 0 {
		t.Fatalf("butler threads survived wipe: %+v (%v)", got, err)
	}
	if ps.PublicKey() == oldKey {
		t.Fatal("VAPID key did not rotate")
	}
	if got := ps.List(); len(got) != 0 {
		t.Fatalf("push subscriptions survived wipe: %+v", got)
	}
	if got, err := d.Events.Read(0, 0); err != nil || len(got) != 0 {
		t.Fatalf("event log survived wipe: %+v (%v)", got, err)
	}
	st.View(func(doc *state.Document) {
		if len(doc.AIModels) != 0 {
			t.Fatalf("AI models survived wipe: %+v", doc.AIModels)
		}
		if doc.User.Email == "" {
			t.Fatal("login user was wiped")
		}
		if doc.ServerKey == nil || doc.ServerKey.PrivateKey == "" {
			t.Fatal("server SSH key was wiped")
		}
	})
}
