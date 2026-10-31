package sync

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- codes ---

func TestCodeFormatAndBinding(t *testing.T) {
	s := NewCodeStore()
	code, expires, err := s.Generate("acme/api")
	require.NoError(t, err)
	require.Regexp(t, `^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$`, code)
	require.WithinDuration(t, time.Now().Add(CodeTTL), expires, time.Second)

	sess, err := s.Lookup(code, "acme/api", "container", "/w", "main", "abc")
	require.NoError(t, err)
	require.Equal(t, "acme/api", sess.Project)

	// Repeating hello with the same code and project returns the same live
	// session: the code stays one-time, not one-handshake.
	again, err := s.Lookup(code, "acme/api", "container", "/w", "main", "abc")
	require.NoError(t, err)
	require.Equal(t, sess.ID, again.ID)

	// Lowercase and ambiguous characters still resolve to the same code.
	lower := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	if _, err := s.Lookup(lower, "acme/api", "container", "/w", "main", "abc"); err != nil {
		t.Fatalf("normalized code lookup: %v", err)
	}
}

func TestCodeWrongProjectThenExpiry(t *testing.T) {
	s := NewCodeStore()
	base := time.Now()
	s.now = func() time.Time { return base }
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)

	_, err = s.Lookup(code, "acme/web", "c", "/w", "main", "abc")
	var e *Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeWrongProject, e.Code)
	require.Contains(t, e.Message, `Code is for "acme/api", not "acme/web"`)

	s.now = func() time.Time { return base.Add(CodeTTL + time.Second) }
	_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeExpired, e.Code)
}

func TestCodeDiesAfterTenAttempts(t *testing.T) {
	s := NewCodeStore()
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)
	for i := 0; i < MaxAttempts; i++ {
		_, _ = s.Lookup(code, "acme/web", "c", "/w", "main", "abc")
	}
	_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	var e *Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeInvalid, e.Code)
}

func TestUsedCodeIsRejected(t *testing.T) {
	s := NewCodeStore()
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)
	sess, err := s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.NoError(t, err)
	s.markUsed(sess)
	s.Finish(sess.ID)
	_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	var e *Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeUsed, e.Code)
}

func TestGenerateRefusesWhileRunning(t *testing.T) {
	s := NewCodeStore()
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)
	_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.NoError(t, err)
	_, _, err = s.Generate("acme/api")
	var e *Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrSyncRunning, e.Code)
}

// --- manifests and deltas ---

func TestDiffActions(t *testing.T) {
	src := Manifest{Files: []Entry{
		{Path: "added.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("a"))},
		{Path: "same.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("s"))},
		{Path: "changed.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("new"))},
		{Path: "mode.txt", Size: 1, Mode: 0o755, Type: TypeFile, Hash: HashBytes([]byte("m"))},
		{Path: "link", Size: 0, Mode: 0o777, Type: TypeSymlink, Hash: HashBytes([]byte("target"))},
		{Path: "bin.dat", Size: 4, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte{0, 1, 2, 3})},
	}}
	dst := Manifest{Files: []Entry{
		{Path: "same.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("s"))},
		{Path: "changed.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("old"))},
		{Path: "mode.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("m"))},
		{Path: "gone.txt", Size: 1, Mode: 0o644, Type: TypeFile, Hash: HashBytes([]byte("g"))},
	}}
	ops := Diff(src, dst)
	want := map[string]string{
		"added.txt": ActionAdded, "changed.txt": ActionOverwritten, "mode.txt": ActionOverwritten,
		"link": ActionAdded, "bin.dat": ActionAdded, "gone.txt": ActionDeleted,
	}
	require.Len(t, ops, len(want))
	for _, op := range ops {
		require.Equal(t, want[op.Path], op.Action, op.Path)
	}
	add, over, del := Counts(ops)
	require.Equal(t, 3, add)
	require.Equal(t, 2, over)
	require.Equal(t, 1, del)

	transfer := TransferSet(src, ops)
	require.Len(t, transfer, 5) // everything but the delete
	for _, e := range transfer {
		require.NotEqual(t, "gone.txt", e.Path)
	}
}

func TestPlanOpsOrientsAtTheTarget(t *testing.T) {
	client := Manifest{Files: []Entry{
		{Path: "README.md", Type: TypeFile, Mode: 0o644, Hash: HashBytes([]byte("readme"))},
		{Path: "hello.txt", Type: TypeFile, Mode: 0o644, Hash: HashBytes([]byte("laptop"))},
		{Path: "laptop.txt", Type: TypeFile, Mode: 0o644, Hash: HashBytes([]byte("only here"))},
	}}
	server := Manifest{Files: []Entry{
		{Path: "README.md", Type: TypeFile, Mode: 0o644, Hash: HashBytes([]byte("readme"))},
		{Path: "hello.txt", Type: TypeFile, Mode: 0o644, Hash: HashBytes([]byte("pcoder"))},
	}}

	// Upload changes pcoder: laptop.txt is added and hello.txt overwritten.
	sendOps := PlanOps(Upload, client, server)
	require.Equal(t, []Op{
		{Path: "hello.txt", Action: ActionOverwritten},
		{Path: "laptop.txt", Action: ActionAdded},
	}, sendOps)

	// Download changes the laptop: the server-only path is deleted here.
	bringOps := PlanOps(Download, client, server)
	require.Equal(t, []Op{
		{Path: "hello.txt", Action: ActionOverwritten},
		{Path: "laptop.txt", Action: ActionDeleted},
	}, bringOps)

	// The transfer set is always drawn from the source side.
	require.Len(t, TransferSet(client, sendOps), 2)
	require.Len(t, TransferSet(server, bringOps), 1) // hello.txt only; nothing else differs
}

func TestManifestRejectsUnsafePaths(t *testing.T) {
	for _, p := range []string{"../x", "/abs", ".git/config", "a//b", "", "a/../b"} {
		require.False(t, ValidPath(p), p)
	}
	m := Manifest{Files: []Entry{{Path: "../escape", Type: TypeFile, Hash: HashBytes(nil), Mode: 0o644}}}
	require.Error(t, m.Normalize())
}

// --- crypto ---

func TestFrameRoundTripAndRejection(t *testing.T) {
	key, err := DeriveKey("ABCD-EFGH-JKMN-PQRS", "nonce-1")
	require.NoError(t, err)
	aad := AAD{Session: "s1", Project: "acme/api", Branch: "main", Commit: "c1", Direction: "send"}

	var buf bytes.Buffer
	fw, err := NewFrameWriter(&buf, key, aad)
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("work-sync "), 10_000)
	for i := 0; i < len(payload); i += FrameSize {
		end := min(i+FrameSize, len(payload))
		_, err := fw.Write(payload[i:end])
		require.NoError(t, err)
	}

	fr, err := NewFrameReader(bytes.NewReader(buf.Bytes()), key, aad)
	require.NoError(t, err)
	var got bytes.Buffer
	_, err = got.ReadFrom(fr)
	require.NoError(t, err)
	require.Equal(t, payload, got.Bytes())

	// Wrong direction: valid ciphertext, wrong associated data.
	bad := aad
	bad.Direction = "bring"
	fr, err = NewFrameReader(bytes.NewReader(buf.Bytes()), key, bad)
	require.NoError(t, err)
	_, err = io.ReadAll(fr)
	require.ErrorIs(t, err, ErrBadFrame)

	// Tampered ciphertext fails the tag.
	tampered := buf.Bytes()
	tampered[len(tampered)-1] ^= 0xff
	fr, err = NewFrameReader(bytes.NewReader(tampered), key, aad)
	require.NoError(t, err)
	_, err = io.ReadAll(fr)
	require.ErrorIs(t, err, ErrBadFrame)
}

func TestDeriveKeyIsDeterministicAndNonceBound(t *testing.T) {
	a, err := DeriveKey("code", "n1")
	require.NoError(t, err)
	b, err := DeriveKey("code", "n1")
	require.NoError(t, err)
	c, err := DeriveKey("code", "n2")
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.NotEqual(t, a, c)
	require.Len(t, a, 32)
}

// --- local manifest and backup ---

// repo creates a git work tree with one commit and returns its path.
func repo(t *testing.T) string {
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
	return dir
}

func TestBuildManifestCoversTrackedUntrackedAndSymlinks(t *testing.T) {
	dir := repo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("changed\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("secret\n"), 0o644))
	require.NoError(t, os.Symlink("hello.txt", filepath.Join(dir, "link")))
	require.NoError(t, os.Remove(filepath.Join(dir, ".gitignore")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644))
	// A tracked file deleted from the work tree drops out of the manifest.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("x"), 0o644))
	cmd := exec.Command("git", "-C", dir, "add", "tracked.txt")
	require.NoError(t, cmd.Run())
	require.NoError(t, os.Remove(filepath.Join(dir, "tracked.txt")))

	m, err := BuildManifest(dir)
	require.NoError(t, err)
	paths := map[string]Entry{}
	for _, e := range m.Files {
		paths[e.Path] = e
	}
	require.Contains(t, paths, "hello.txt")
	require.Equal(t, HashBytes([]byte("changed\n")), paths["hello.txt"].Hash)
	require.Contains(t, paths, "new.txt")
	require.Contains(t, paths, "link")
	require.Equal(t, TypeSymlink, paths["link"].Type)
	require.Equal(t, HashBytes([]byte("hello.txt")), paths["link"].Hash)
	require.NotContains(t, paths, "ignored.txt")
	require.NotContains(t, paths, "tracked.txt")
	require.NotContains(t, paths, ".git/config")
}

func TestGitInfoForReadsProjectBranchAndCommit(t *testing.T) {
	dir := repo(t)
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "git@github.com:acme/api.git")
	require.NoError(t, cmd.Run())
	info, err := GitInfoFor(dir)
	require.NoError(t, err)
	require.Equal(t, "acme/api", info.Project)
	require.Equal(t, "main", info.Branch)
	require.False(t, info.Detached)
	require.Len(t, info.Commit, 40)
}

func TestProjectFromRemoteForms(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/api.git":                   "acme/api",
		"https://github.com/acme/api.git":               "acme/api",
		"https://github.com/acme/api":                   "acme/api",
		"ssh://git@github.com/acme/api.git":             "acme/api",
		"git://host.docker.internal:8091/e2e/run-1":     "e2e/run-1",
		"git://host.docker.internal:8091/e2e/run-1.git": "e2e/run-1",
	}
	for remote, want := range cases {
		require.Equal(t, want, ProjectFromRemote(remote), remote)
	}
}

func TestBackupAndRestoreEachAction(t *testing.T) {
	dir := repo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("keep me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("untouched\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\nunrelated.txt\n"), 0o644))

	target, err := BuildManifest(dir)
	require.NoError(t, err)
	source := Manifest{Files: []Entry{}}

	ops := Diff(source, target)
	// Every target path is deleted in a Bring of an empty source; add one
	// added and one overwritten case to cover the other actions.
	require.Contains(t, ops, Op{Path: "hello.txt", Action: ActionDeleted})
	sourceOps := []Op{
		{Path: "hello.txt", Action: ActionOverwritten},
		{Path: "added.txt", Action: ActionAdded},
		{Path: "gone.txt", Action: ActionDeleted},
	}
	root := t.TempDir()
	archive, err := WriteBackup(root, "acme/api", "main", "c0ffee", dir, target, sourceOps, time.Now())
	require.NoError(t, err)
	require.FileExists(t, archive)
	require.Contains(t, archive, filepath.Join("acme-api", ""))

	// Apply the plan: overwrite, add, delete.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("overwritten\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "added.txt"), []byte("new\n"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))

	meta, err := RestoreLocal(archive, dir)
	require.NoError(t, err)
	require.Len(t, meta.Entries, 3)
	hello, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(hello))
	gone, err := os.ReadFile(filepath.Join(dir, "gone.txt"))
	require.NoError(t, err)
	require.Equal(t, "keep me\n", string(gone))
	_, err = os.Stat(filepath.Join(dir, "added.txt"))
	require.True(t, os.IsNotExist(err))
	untouched, err := os.ReadFile(filepath.Join(dir, "unrelated.txt"))
	require.NoError(t, err)
	require.Equal(t, "untouched\n", string(untouched))
}

func TestBackupKeepsLastTen(t *testing.T) {
	root := t.TempDir()
	base := time.Now()
	for i := 0; i < 12; i++ {
		_, err := WriteBackup(root, "acme/api", "main", "c", t.TempDir(), Manifest{}, []Op{}, base.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}
	PruneBackups(root, "acme/api", 10)
	entries, err := os.ReadDir(filepath.Join(root, "acme-api"))
	require.NoError(t, err)
	require.Len(t, entries, 10)
}

func TestParseManifestRejectsShortRecords(t *testing.T) {
	_, err := parseManifest("a\x00" + "1\x00")
	require.Error(t, err)
	require.Contains(t, err.Error(), "multiple of 5")
}

func TestErrorMessagesMatchSpec(t *testing.T) {
	var e *Error
	require.ErrorAs(t, ProjectNotFound("acme/api"), &e)
	require.Equal(t, "Project \"acme/api\" not found on this pcoder.\nCheck the name. It looks like owner/repo.", e.Message)
	require.ErrorAs(t, BranchMismatch("main", "feature/login"), &e)
	require.Contains(t, e.Message, "Checkout the same branch on both sides, then run sync again.")
	require.ErrorAs(t, CommitMismatch("4f2a1c9ffff", "9b1e77dffff"), &e)
	require.Contains(t, e.Message, "Sync needs the same commit on both sides. Pull or reset first.")
}

// TestExpiredCodeAttemptsCount pins the spec's attempt budget: every failed
// Lookup burns an attempt, expiry included, and the tenth failure kills the
// code.
func TestExpiredCodeAttemptsCount(t *testing.T) {
	s := NewCodeStore()
	base := time.Now()
	s.now = func() time.Time { return base }
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)

	s.now = func() time.Time { return base.Add(CodeTTL + time.Minute) }
	var e *Error
	for i := 1; i <= MaxAttempts; i++ {
		_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
		require.ErrorAs(t, err, &e)
		require.Equal(t, ErrCodeExpired, e.Code, "attempt %d", i)
	}
	// The budget is spent, so the code is dead: an expired code that has
	// burned every attempt is no longer distinguishable from invalid.
	_, err = s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeInvalid, e.Code)
}

// TestUnknownCodeAttemptsAreTracked pins the other half of the attempt
// budget: a code that never existed still burns attempts, so an attacker
// cannot guess for free, and the entry dies after the tenth failure.
func TestUnknownCodeAttemptsAreTracked(t *testing.T) {
	s := NewCodeStore()
	const code = "0000-0000-0000-0000"
	hash := hashCode(code)
	var e *Error
	for i := 1; i < MaxAttempts; i++ {
		_, err := s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
		require.ErrorAs(t, err, &e)
		require.Equal(t, ErrCodeInvalid, e.Code)
		entry := s.entries[hash]
		require.NotNil(t, entry, "attempt %d was not recorded", i)
		require.Equal(t, i, entry.attempts)
	}
	_, err := s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.ErrorAs(t, err, &e)
	require.Equal(t, ErrCodeInvalid, e.Code)
	require.Nil(t, s.entries[hash], "the code must die after %d failed attempts", MaxAttempts)
}

// TestFinishClearsPlaintext pins the spec's "the plaintext exists in memory
// for the life of the session": once the session ends the store keeps the
// hash but not the code.
func TestFinishClearsPlaintext(t *testing.T) {
	s := NewCodeStore()
	code, _, err := s.Generate("acme/api")
	require.NoError(t, err)
	sess, err := s.Lookup(code, "acme/api", "c", "/w", "main", "abc")
	require.NoError(t, err)
	hash := hashCode(code)
	require.Equal(t, code, s.entries[hash].plain)

	s.Finish(sess.ID)
	require.NotContains(t, s.sessions, sess.ID)
	require.Empty(t, s.entries[hash].plain, "plaintext code outlived its session")
}
