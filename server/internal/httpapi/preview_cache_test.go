package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/session"
)

// seedCacheSession registers a live loopback websocket in cdpCache. The CDP
// cache is process-global, so every seed is evicted again on cleanup.
func seedCacheSession(t *testing.T, projectID string) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial loopback ws: %v", err)
	}
	cdpMu.Lock()
	cdpCache[projectID] = &cdpSession{ws: conn, pending: make(map[int]chan json.RawMessage)}
	cdpMu.Unlock()
	t.Cleanup(func() { evictCDP(projectID) })
}

func cachedSession(t *testing.T, projectID string) *cdpSession {
	t.Helper()
	cdpMu.Lock()
	defer cdpMu.Unlock()
	return cdpCache[projectID]
}

func TestPreviewCloseEvictsCDPSession(t *testing.T) {
	d, pinOut := newTestDeps(t)
	m := preview.NewManager(previewTestFactory{ep: privatePreviewEndpoint})
	d.Preview = m
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	seedCacheSession(t, "p1")
	if cachedSession(t, "p1") == nil {
		t.Fatal("seeded session missing from cache")
	}
	rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/p1/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("close = %d body=%s", rec.Code, rec.Body)
	}
	if cachedSession(t, "p1") != nil {
		t.Fatal("CDP session survived preview close — next tools call would dial a dead socket")
	}
}

func TestProjectStopEvictsCDPSession(t *testing.T) {
	d, md, pinOut, _ := newProjectDeps(t)
	d.Sessions = session.New(md)
	d.Preview = preview.NewManager(previewTestFactory{ep: privatePreviewEndpoint})
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	md.EXPECT().EnsureNetwork(mock.Anything, docker.DefaultNetwork).Return(nil)
	md.EXPECT().InspectImage(mock.Anything, mock.Anything).Return(nil)
	md.EXPECT().Run(mock.Anything, mock.Anything).Return("cid", nil)
	md.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	rec := authedPost(t, h, cookie, "/api/projects", `{"repoUrl":"https://github.com/x/hello.git"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rec.Code, rec.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("bad create body %q err=%v", rec.Body, err)
	}

	seedCacheSession(t, created.ID)
	md.EXPECT().Stop(mock.Anything, project.ContainerName(created.ID), mock.Anything).Return(nil)
	rec = authedPost(t, h, cookie, "/api/projects/"+url.PathEscape(created.ID)+"/stop", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d body=%s", rec.Code, rec.Body)
	}
	if cachedSession(t, created.ID) != nil {
		t.Fatal("CDP session survived project stop — next tools call would block until timeout")
	}
}
