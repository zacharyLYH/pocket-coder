package sync

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Result is what the client prints after a transfer.
type Result struct {
	Applied int    `json:"applied"`
	Deleted int    `json:"deleted"`
	Backup  string `json:"backup,omitempty"`
}

// Service owns server-side sessions, the plan, and the apply pipeline. It is
// process-local: a restart aborts active syncs.
type Service struct {
	store  *CodeStore
	io     ContainerIO
	backup string // server-side backup root
	now    func() time.Time
}

// New wires a sync service. backupRoot lives on the server host (the data
// dir), not in the project container.
func New(c ContainerIO, backupRoot string) *Service {
	return &Service{store: NewCodeStore(), io: c, backup: backupRoot, now: time.Now}
}

// Abort ends a session the laptop cancelled or failed. The project is
// immediately interactive again.
func (s *Service) Abort(id string) { s.store.Finish(id) }

// Store exposes the code/session store for handlers.
func (s *Service) Store() *CodeStore { return s.store }

// Git reads a project container's branch and commit. Handlers call it before
// a session exists.
func (s *Service) Git(ctx context.Context, container, dir string) (string, string, error) {
	return ContainerGit(ctx, s.io, container, dir)
}

// Generate creates a code for one project.
func (s *Service) Generate(project string) (string, time.Time, error) {
	return s.store.Generate(project)
}

// Hello validates the code and binds a session to the project's container.
func (s *Service) Hello(ctx context.Context, project, code, container, dir, branch, commit string) (*Session, error) {
	return s.store.Lookup(code, project, container, dir, branch, commit)
}

// Plan builds the server manifest, binds the direction, and returns the
// delta. It does not move any bytes.
func (s *Service) Plan(ctx context.Context, sess *Session, direction Direction, client Manifest) (Manifest, []Op, error) {
	if !ValidDirection(direction) {
		return Manifest{}, nil, BadPlan("Choose Upload or Download.")
	}
	if err := client.Normalize(); err != nil {
		return Manifest{}, nil, BadPlan("The laptop manifest is malformed.")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.accepted {
		return Manifest{}, nil, BadPlan("This sync was already planned.")
	}
	server, err := ContainerManifest(ctx, s.io, sess.container, sess.dir)
	if err != nil {
		return Manifest{}, nil, err
	}
	branch, commit, err := ContainerGit(ctx, s.io, sess.container, sess.dir)
	if err != nil {
		return Manifest{}, nil, err
	}
	if branch != sess.Branch || commit != sess.Commit {
		return Manifest{}, nil, BadPlan("The project changed while the code was being entered. Generate a new code.")
	}
	ops := PlanOps(direction, client, server)
	sess.client, sess.server, sess.ops = client, server, ops
	sess.direction, sess.accepted = direction, true
	sess.phase.Store(string(direction) + "ing")
	s.store.markUsed(sess)
	return server, ops, nil
}

// PlanOps orients the delta at the side that will change: Upload overwrites
// pcoder, Download overwrites the laptop.
func PlanOps(direction Direction, client, server Manifest) []Op {
	if direction == Download {
		return Diff(server, client)
	}
	return Diff(client, server)
}

// Send receives the encrypted tar and applies it to the project container.
// The target is backed up first and restored automatically on any failure.
func (s *Service) Send(ctx context.Context, sess *Session, body io.Reader) (Result, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.accepted || sess.direction != Upload {
		return Result{}, BadPlan("Upload this project's plan first.")
	}
	if sess.finished.Load() {
		return Result{}, CodeUsed()
	}
	key, err := DeriveKey(NormalizeCode(sess.code), sess.Nonce)
	if err != nil {
		return Result{}, err
	}
	entries := TransferSet(sess.client, sess.ops)

	staged, err := os.MkdirTemp("", "pcoder-sync-in-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(staged)
	fr, err := NewFrameReader(body, key, aadFor(sess, Upload))
	if err != nil {
		return Result{}, err
	}
	if err := WriteStaged(fr, staged, entries); err != nil {
		return Result{}, err
	}

	// Snapshot the target before changing it: only affected paths, never
	// unrelated files.
	backupPath, err := s.backupTarget(ctx, sess)
	if err != nil {
		return Result{}, err
	}

	applyErr := s.apply(ctx, sess, staged, entries)
	if applyErr == nil {
		applyErr = s.verify(ctx, sess, sess.client)
	}
	if applyErr != nil {
		if rerr := s.rollback(ctx, sess, backupPath); rerr != nil {
			return Result{}, fmt.Errorf("%v; automatic restore failed: %w. Backup kept at %s", applyErr, rerr, backupPath)
		}
		return Result{}, applyErr
	}
	sess.finished.Store(true)
	s.store.Finish(sess.ID)
	added, overwritten, deleted := Counts(sess.ops)
	return Result{Applied: added + overwritten, Deleted: deleted, Backup: backupPath}, nil
}

// Receive streams the encrypted tar of every server file the laptop needs.
func (s *Service) Receive(ctx context.Context, sess *Session, w io.Writer) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.accepted || sess.direction != Download {
		return BadPlan("Download this project's plan first.")
	}
	if sess.finished.Load() {
		return CodeUsed()
	}
	key, err := DeriveKey(NormalizeCode(sess.code), sess.Nonce)
	if err != nil {
		return err
	}
	fw, err := NewFrameWriter(w, key, aadFor(sess, Download))
	if err != nil {
		return err
	}
	if err := writeSynced(ctx, fw, s.io, sess, TransferSet(sess.server, sess.ops)); err != nil {
		return err
	}
	sess.finished.Store(true)
	s.store.Finish(sess.ID)
	return nil
}

// writeSynced copies the container tar through the frame writer in chunks.
func writeSynced(ctx context.Context, fw *FrameWriter, c ContainerIO, sess *Session, entries []Entry) error {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	r := containerTar(ctx, c, sess.container, sess.dir, paths)
	defer r.Close()
	buf := make([]byte, FrameSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := fw.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// apply writes the staged files and removes deleted paths.
func (s *Service) apply(ctx context.Context, sess *Session, staged string, entries []Entry) error {
	if err := UploadFiles(ctx, s.io, sess.container, sess.dir, staged, entries); err != nil {
		return err
	}
	var deleted []string
	for _, op := range sess.ops {
		if op.Action == ActionDeleted {
			deleted = append(deleted, op.Path)
		}
	}
	return RemovePaths(ctx, s.io, sess.container, sess.dir, deleted)
}

// backupTarget archives every existing affected path before an apply.
func (s *Service) backupTarget(ctx context.Context, sess *Session) (string, error) {
	affected := existingEntries(sess.server, sess.ops)
	if len(affected) == 0 {
		return "", nil
	}
	pre, err := s.stageServerFiles(ctx, sess, affected)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(pre)
	path, err := WriteBackup(s.backup, sess.Project, sess.Branch, sess.Commit, pre, sess.server, sess.ops, s.now())
	if err != nil {
		return "", err
	}
	sess.backup = path
	PruneBackups(s.backup, sess.Project, 10)
	return path, nil
}

// verify checks the applied tree against the source manifest. A mismatch is
// a hard failure: the caller rolls back.
func (s *Service) verify(ctx context.Context, sess *Session, want Manifest) error {
	got, err := ContainerManifest(ctx, s.io, sess.container, sess.dir)
	if err != nil {
		return err
	}
	if err := Compare(want, got); err != nil {
		return BadPayload("The applied tree does not match the manifest: " + err.Error() + ".")
	}
	return nil
}

// existingEntries is target's entries for ops that touch existing paths.
func existingEntries(target Manifest, ops []Op) []Entry {
	byPath := target.Index()
	var out []Entry
	for _, op := range ops {
		if op.Action == ActionAdded {
			continue
		}
		if e, ok := byPath[op.Path]; ok {
			out = append(out, e)
		}
	}
	return out
}

// stageServerFiles downloads the given entries into a temp dir so the local
// backup writer can archive them.
func (s *Service) stageServerFiles(ctx context.Context, sess *Session, entries []Entry) (string, error) {
	tmp, err := os.MkdirTemp("", "pcoder-sync-backup-")
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	r := containerTar(ctx, s.io, sess.container, sess.dir, paths)
	defer r.Close()
	// The tar may contain extra members (tar walks directories); keep only
	// the entries we asked for.
	if err := copyTarMembers(r, tmp, entries, false); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// rollback re-uploads the backup archive's files and removes paths the
// failed apply added.
func (s *Service) rollback(ctx context.Context, sess *Session, backupPath string) error {
	if backupPath == "" {
		return nil
	}
	tmp, err := os.MkdirTemp("", "pcoder-sync-rollback-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	meta, err := ExtractBackup(backupPath, tmp)
	if err != nil {
		return err
	}
	files := stagedEntries(tmp, meta)
	if err := UploadFiles(ctx, s.io, sess.container, sess.dir, tmp, files); err != nil {
		return err
	}
	var added []string
	for _, op := range sess.ops {
		if op.Action == ActionAdded {
			added = append(added, op.Path)
		}
	}
	return RemovePaths(ctx, s.io, sess.container, sess.dir, added)
}

// stagedEntries maps backup entries to manifest entries of the staged tree.
func stagedEntries(root string, meta BackupMeta) []Entry {
	var out []Entry
	for _, be := range meta.Entries {
		if be.Action == ActionAdded {
			continue
		}
		e := Entry{Path: be.Path, Mode: be.Mode, Type: be.Type, Hash: be.Hash}
		if be.Type == TypeFile {
			if fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(be.Path))); err == nil {
				e.Size = fi.Size()
			}
		}
		out = append(out, e)
	}
	return out
}

func aadFor(sess *Session, d Direction) AAD {
	return AAD{Session: sess.ID, Project: sess.Project, Branch: sess.Branch, Commit: sess.Commit, Direction: string(d)}
}
