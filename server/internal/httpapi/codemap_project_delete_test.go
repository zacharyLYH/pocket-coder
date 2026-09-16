// Codemap lifecycle across project deletion: chats are project-scoped
// artifacts, so scope=all (and scope=metadata) deletes must remove every
// thread and lineage file from the data dir. This pins both the cascade
// and the prompt-title naming of thread + lineage files end to end.
package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/harness"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	dockermocks "pcoder/mocks/docker"
)

// codemapDeleteDeps is newSessionDeps with the data dir exposed (to assert
// on the store's files) and the codemap store wired into the project
// service, mirroring main.go's SetCodemaps cascade.
func codemapDeleteDeps(t *testing.T) (Deps, *dockermocks.MockClient, *bytes.Buffer, *state.Store, string) {
	t.Helper()
	d, pinOut, dataDir := newTestDepsInDir(t)
	md := dockermocks.NewMockClient(t)
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	d.Sessions = session.New(md)
	hs := harness.New(st)
	if _, err := hs.Save(harness.Harness{Name: "Fake", Command: "fakecli", Install: "npm i -g fakecli"}); err != nil {
		t.Fatal(err)
	}
	d.Harnesses = hs
	d.SSHKeys = sshkeys.New(st)
	d.Projects = project.NewService(project.Open(st), md, d.Events)
	d.Projects.SetCodemaps(d.Codemaps)
	d.State = st
	return d, md, pinOut, st, dataDir
}

func TestCodemapDeletedWithProject(t *testing.T) {
	d, md, pinOut, st, dataDir := codemapDeleteDeps(t)
	mockRepoDir(md, "abc123")
	mockOrientation(md, "package.json\nsrc/\nindex.html\n")
	mockHydrate(md, "func main() {\n")
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", finalCodemapJSON(), nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// One real turn through the loop: creates the thread file (named
	// after the prompt) and the lineage file beside it.
	const prompt = "Map the auth flow"
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap", `{"prompt":"`+prompt+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("codemap: got %d %q, want 200", rec.Code, rec.Body)
	}
	var body struct {
		ThreadID    string `json:"threadId"`
		ThreadTitle string `json:"threadTitle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ThreadID == "" || body.ThreadTitle != prompt {
		t.Fatalf("thread identity = %+v, want id and prompt title %q", body, prompt)
	}

	// Naming contract: thread file is the prompt title; lineage is the
	// same name with a .lineage.json suffix.
	dir := filepath.Join(dataDir, "codemaps", "abc")
	threadPath := filepath.Join(dir, prompt+".json")
	lineagePath := filepath.Join(dir, prompt+".lineage.json")
	for _, p := range []string{threadPath, lineagePath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
	lineageRaw, err := os.ReadFile(lineagePath)
	if err != nil {
		t.Fatal(err)
	}
	var lineageMeta struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(lineageRaw, &lineageMeta); err != nil || lineageMeta.ThreadID != body.ThreadID {
		t.Fatalf("lineage threadId = %q (%v), want %q", lineageMeta.ThreadID, err, body.ThreadID)
	}

	// Delete the whole project: codemap files must go with it.
	cname := project.ContainerName("abc")
	md.EXPECT().Stop(mock.Anything, cname, mock.Anything).Return(nil)
	md.EXPECT().Remove(mock.Anything, cname, true).Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-home").Return(nil)
	md.EXPECT().RemoveVolume(mock.Anything, cname+"-repo").Return(nil)
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc?scope=all")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete project: got %d %q, want 200", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("codemap dir after project delete: %v", err)
	}
	rec = authedGet(t, h, cookie, "/api/projects/abc/codemap/threads")
	var listed struct {
		Threads []codemapthreads.Summary `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 0 {
		t.Fatalf("threads after project delete = %+v, want none", listed.Threads)
	}
}

func TestCodemapDeletedWithMetadataScope(t *testing.T) {
	d, _, pinOut, st, dataDir := codemapDeleteDeps(t)
	seedProject(t, st, "abc")
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	// A thread file without a model call: the create endpoint suffices.
	rec := authedPost(t, h, cookie, "/api/projects/abc/codemap/threads", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create thread: got %d %q, want 201", rec.Code, rec.Body)
	}
	dir := filepath.Join(dataDir, "codemaps", "abc")
	if names, err := os.ReadDir(dir); err != nil || len(names) == 0 {
		t.Fatalf("thread file missing before delete: %v %v", names, err)
	}

	// scope=metadata drops the record and, with it, the chats on disk.
	// No docker expectations: metadata leaves the container alone.
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/projects/abc?scope=metadata")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete metadata: got %d %q, want 200", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("codemap dir after metadata delete: %v", err)
	}
}


