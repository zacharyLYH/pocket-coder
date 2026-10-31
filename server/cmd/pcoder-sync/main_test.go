package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	worksync "pcoder/internal/sync"
)

// gitRepo creates a one-commit work tree whose origin resolves to acme/api,
// the shape the client expects from a real laptop folder.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=e2e@example.com", "-c", "user.name=e2e", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644))
	run("add", "-A")
	run("commit", "-qm", "first")
	run("remote", "add", "origin", "git@github.com:acme/api.git")
	return dir
}

// lineWith returns the first output line containing substr, or "".
func lineWith(out, substr string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// runSyncMain runs the client with args and returns everything it printed.
func runSyncMain(t *testing.T, args ...string) string {
	t.Helper()
	oldArgs, oldStdout := os.Args, os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Args, os.Stdout = append([]string{"pcoder-sync"}, args...), w
	defer func() { os.Args, os.Stdout = oldArgs, oldStdout }()

	err = syncMain()
	require.NoError(t, w.Close())
	out, rerr := io.ReadAll(r)
	require.NoError(t, rerr)
	require.NoError(t, err, "client failed:\n%s", out)
	return string(out)
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// TestDownloadPrintsRestoreCommands pins the download result the spec
// requires: the backup path, the direct pcoder-sync restore command, and
// the curl bootstrap fallback for a laptop that no longer has the client.
func TestDownloadPrintsRestoreCommands(t *testing.T) {
	dir := gitRepo(t)
	info, err := worksync.GitInfoFor(dir)
	require.NoError(t, err)
	local, err := worksync.BuildManifest(dir)
	require.NoError(t, err)

	const (
		code      = "ABCD-EFGH-JKMN-PQRS"
		sessionID = "session-1"
		nonce     = "nonce-1"
	)
	serverBody := []byte("server version\n")

	// The pcoder tree differs from the laptop only in hello.txt.
	server := worksync.Manifest{Files: append([]worksync.Entry(nil), local.Files...)}
	for i := range server.Files {
		if server.Files[i].Path == "hello.txt" {
			server.Files[i].Size = int64(len(serverBody))
			server.Files[i].Hash = worksync.HashBytes(serverBody)
		}
	}
	ops := worksync.PlanOps(worksync.Download, local, server)
	require.Equal(t, []worksync.Op{{Path: "hello.txt", Action: worksync.ActionOverwritten}}, ops)

	// The receive payload: hello.txt's pcoder bytes, sealed as frames.
	key, err := worksync.DeriveKey(worksync.NormalizeCode(code), nonce)
	require.NoError(t, err)
	var payload bytes.Buffer
	fw, err := worksync.NewFrameWriter(&payload, key, worksync.AAD{
		Session: sessionID, Project: "acme/api", Branch: info.Branch, Commit: info.Commit, Direction: string(worksync.Download),
	})
	require.NoError(t, err)
	tw := tar.NewWriter(fw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "hello.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(serverBody))}))
	_, err = tw.Write(serverBody)
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sync/hello", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONResponse(t, w, map[string]any{
			"session": sessionID, "nonce": nonce, "project": "acme/api",
			"branch": info.Branch, "commit": info.Commit, "expiresAt": time.Now().Add(time.Minute),
		})
	})
	mux.HandleFunc("POST /api/sync/{session}/plan", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeJSONResponse(t, w, map[string]any{"manifest": server, "ops": ops})
	})
	mux.HandleFunc("GET /api/sync/{session}/receive", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload.Bytes())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("HOME", t.TempDir()) // keep the backup archive out of the real home
	out := runSyncMain(t, "--server", srv.URL, "--project", "acme/api", "--code", code,
		"--dir", dir, "--direction", "download", "--yes")
	require.Contains(t, out, "Downloaded. 1 updated, 0 deleted here.")

	// The confirmation screen carries the agent caveat, before any bytes move:
	// pcoder cannot detect a running agent, so the human acknowledges it.
	caveat := lineWith(out, "Stop agent turns")
	require.NotEmpty(t, caveat, out)
	require.Less(t, strings.Index(out, caveat), strings.Index(out, "Downloaded. 1 updated"),
		"the agent caveat must print before the transfer result")

	backupLine := lineWith(out, "Backup:  ")
	require.NotEmpty(t, backupLine, out)
	archive := strings.TrimSpace(strings.TrimPrefix(backupLine, "Backup:  "))
	require.Contains(t, out, "Restore: pcoder-sync restore "+archive+" --dir .")

	fallback := lineWith(out, "curl -fsSL")
	require.NotEmpty(t, fallback, "download printed no curl restore fallback:\n%s", out)
	require.Contains(t, fallback, srv.URL+"/sync.sh | sh -s -- restore "+archive)
	require.True(t, strings.HasSuffix(strings.TrimSpace(fallback), "--dir ."), fallback)
}
