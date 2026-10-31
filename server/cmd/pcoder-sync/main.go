// Command pcoder-sync is the static Work Sync client. The laptop always
// starts a sync: it validates the folder against the project the code is
// bound to, confirms the overwrite, backs up the target, transfers, and
// verifies. It never commits, pushes, or pulls.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	worksync "pcoder/internal/sync"
)

// backupRoot is where the laptop keeps its archives.
func backupRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pcoder-sync/backups"
	}
	return filepath.Join(home, ".pcoder-sync", "backups")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		restoreMain(os.Args[2:])
		return
	}
	if err := syncMain(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	var e *worksync.Error
	if errors.As(err, &e) {
		fmt.Fprintln(os.Stderr, e.Message)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// restoreMain implements `pcoder-sync restore <archive> --dir <dir>`.
func restoreMain(args []string) {
	// The archive comes before flags; Go's flag package stops at the first
	// non-flag argument, so split it off before parsing.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fail(fmt.Errorf("usage: pcoder-sync restore <archive> --dir <project>"))
	}
	archive, rest := args[0], args[1:]
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	dir := fs.String("dir", ".", "project folder to restore into")
	_ = fs.Parse(rest)
	meta, err := worksync.RestoreLocal(archive, *dir)
	if err != nil {
		fail(fmt.Errorf("restore failed: %w", err))
	}
	fmt.Printf("Restored %d paths from %s\n", len(meta.Entries), archive)
}

func syncMain() error {
	fs := flag.NewFlagSet("pcoder-sync", flag.ExitOnError)
	server := fs.String("server", os.Getenv("PCODER_SYNC_SERVER"), "pcoder base URL")
	project := fs.String("project", "", "project id, owner/repo")
	code := fs.String("code", "", "sync code from pcoder")
	dir := fs.String("dir", ".", "project folder")
	direction := fs.String("direction", "", "send or bring")
	yes := fs.Bool("yes", false, "skip the overwrite confirmation")
	_ = fs.Parse(os.Args[1:])

	if *server == "" {
		return errors.New("missing --server: run the command printed on pcoder")
	}
	base := strings.TrimSuffix(*server, "/")
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	info, err := worksync.GitInfoFor(abs)
	if err != nil {
		return worksync.NotGitRepo()
	}
	if *project == "" {
		*project = info.Project
	}
	if *project == "" {
		return errors.New("cannot read a project from this folder's origin remote\npass --project owner/repo")
	}
	if *code == "" {
		*code = prompt("Sync code: ")
	}

	hello, err := postJSON[helloResponse](base+"/api/sync/hello", map[string]string{"project": *project, "code": *code})
	if err != nil {
		return err
	}
	if err := validateLocal(info, hello); err != nil {
		return err
	}
	fmt.Printf("Project %s\nBranch  %s\nCommit  %s\n", hello.Project, hello.Branch, worksync.ShortSHA(hello.Commit))

	dirChoice, err := pickDirection(*direction)
	if err != nil {
		return err
	}
	manifest, err := worksync.BuildManifest(abs)
	if err != nil {
		return worksync.NotGitRepo()
	}
	plan, err := postJSON[planResponse](base+"/api/sync/"+hello.Session+"/plan", map[string]any{
		"direction": dirChoice,
		"manifest":  manifest,
	})
	if err != nil {
		return err
	}
	added, overwritten, deleted := worksync.Counts(plan.Ops)

	var backup string
	if dirChoice == worksync.Download {
		// Tell the user where the laptop backup will land before asking.
		backup = worksync.BackupPath(backupRoot(), hello.Project, time.Now())
	}
	if !confirm(plan, backup, *yes) {
		abort(base, hello.Session)
		fmt.Println("Cancelled. Nothing changed.")
		return nil
	}

	if dirChoice == worksync.Upload {
		res, err := send(base, hello, *code, plan, abs, manifest)
		if err != nil {
			abort(base, hello.Session)
			return err
		}
		fmt.Printf("\nUploaded. %d updated, %d deleted on pcoder.\n", res.Applied, res.Deleted)
		if res.Backup != "" {
			fmt.Printf("pcoder backup: %s\n", res.Backup)
		}
		return nil
	}

	backup, err = receive(base, hello, *code, plan, abs, manifest)
	if err != nil {
		abort(base, hello.Session)
		return err
	}
	fmt.Printf("\nDownloaded. %d updated, %d deleted here.\n", added+overwritten, deleted)
	if backup != "" {
		fmt.Printf("Backup:  %s\n", backup)
		fmt.Printf("Restore: pcoder-sync restore %s --dir .\n", backup)
		// The client may be gone (new laptop, cleared cache); the bootstrap
		// restores the same archive without it.
		fmt.Printf("Fallback: curl -fsSL %s/sync.sh | sh -s -- restore %s --dir .\n", base, backup)
	}
	return nil
}

type helloResponse struct {
	Session   string    `json:"session"`
	Nonce     string    `json:"nonce"`
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	Commit    string    `json:"commit"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type planResponse struct {
	Manifest worksync.Manifest `json:"manifest"`
	Ops      []worksync.Op     `json:"ops"`
}

// validateLocal enforces the same-project, same-branch, same-commit rules
// before any file moves.
func validateLocal(info worksync.GitInfo, hello helloResponse) error {
	if info.Detached || hello.Branch == "HEAD" {
		return worksync.DetachedHead()
	}
	if info.Project != hello.Project {
		return worksync.RemoteMismatch(hello.Project, info.Project)
	}
	if info.Branch != hello.Branch {
		return worksync.BranchMismatch(info.Branch, hello.Branch)
	}
	if info.Commit != hello.Commit {
		return worksync.CommitMismatch(info.Commit, hello.Commit)
	}
	return nil
}

func pickDirection(v string) (worksync.Direction, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "upload", "u":
		return worksync.Upload, nil
	case "download", "d":
		return worksync.Download, nil
	case "":
	default:
		return "", fmt.Errorf("unknown direction %q — use upload or download", v)
	}
	ans := strings.ToLower(strings.TrimSpace(prompt("Upload to pcoder or Download to laptop? [u/d]: ")))
	if ans == "u" || ans == "upload" {
		return worksync.Upload, nil
	}
	if ans == "d" || ans == "download" {
		return worksync.Download, nil
	}
	return "", errors.New("choose upload or download")
}

func confirm(plan planResponse, backup string, yes bool) bool {
	added, overwritten, deleted := worksync.Counts(plan.Ops)
	parts := make([]string, 0, 3)
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	parts = append(parts, fmt.Sprintf("%d files will be overwritten", overwritten))
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", deleted))
	}
	fmt.Printf("\n%s.\n", strings.Join(parts, ", "))
	// pcoder cannot tell whether an agent is mid-turn (a running CLI is not
	// reliably observable), so the human confirms it instead: a file written
	// during the transfer fails verification, and nothing is applied.
	fmt.Println("Stop agent turns in this project first: a file written mid-transfer fails the sync.")
	if backup != "" {
		fmt.Printf("Backup: %s\n", backup)
	}
	if yes {
		return true
	}
	return strings.ToLower(strings.TrimSpace(prompt("Continue? [y/N] "))) == "y"
}

// send streams the encrypted tar of changed local files to pcoder.
func send(base string, hello helloResponse, code string, plan planResponse, dir string, local worksync.Manifest) (sendResponse, error) {
	key, err := worksync.DeriveKey(worksync.NormalizeCode(code), hello.Nonce)
	if err != nil {
		return sendResponse{}, err
	}
	entries := worksync.TransferSet(local, plan.Ops)
	pr, pw := io.Pipe()
	go func() {
		fw, err := worksync.NewFrameWriter(pw, key, aad(hello, worksync.Upload))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.CloseWithError(writeLocalTar(dir, entries, fw))
	}()
	req, err := http.NewRequest(http.MethodPost, base+"/api/sync/"+hello.Session+"/send", pr)
	if err != nil {
		return sendResponse{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	return doJSON[sendResponse](req)
}

// receive downloads the encrypted tar and applies it to the laptop, after
// backing up every affected local path.
func receive(base string, hello helloResponse, code string, plan planResponse, dir string, local worksync.Manifest) (string, error) {
	key, err := worksync.DeriveKey(worksync.NormalizeCode(code), hello.Nonce)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/sync/"+hello.Session+"/receive", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("connect to pcoder: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", parseErr(resp)
	}
	entries := worksync.TransferSet(plan.Manifest, plan.Ops)

	staged, err := os.MkdirTemp("", "pcoder-sync-in-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	fr, err := worksync.NewFrameReader(resp.Body, key, aad(hello, worksync.Download))
	if err != nil {
		return "", err
	}
	if err := worksync.WriteStaged(fr, staged, entries); err != nil {
		return "", err
	}

	backup, err := worksync.WriteBackup(backupRoot(), hello.Project, hello.Branch, hello.Commit, dir, local, plan.Ops, time.Now())
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	worksync.PruneBackups(backupRoot(), hello.Project, 10)

	if err := worksync.ApplyLocal(dir, staged, plan.Manifest, plan.Ops); err != nil {
		if _, rerr := worksync.RestoreLocal(backup, dir); rerr != nil {
			return backup, fmt.Errorf("%v; automatic restore failed: %w. Restore from %s", err, rerr, backup)
		}
		return backup, err
	}
	got, err := worksync.BuildManifest(dir)
	if err != nil {
		return backup, err
	}
	if err := worksync.Compare(plan.Manifest, got); err != nil {
		if _, rerr := worksync.RestoreLocal(backup, dir); rerr != nil {
			return backup, fmt.Errorf("%v; automatic restore failed: %w. Restore from %s", err, rerr, backup)
		}
		return backup, err
	}
	return backup, nil
}

type sendResponse struct {
	Applied int    `json:"applied"`
	Deleted int    `json:"deleted"`
	Backup  string `json:"backup"`
}

// abort releases the project so the web UI unlocks immediately after a
// cancel or failure; the session is dead either way.
func abort(base, session string) {
	req, err := http.NewRequest(http.MethodPost, base+"/api/sync/"+session+"/abort", nil)
	if err != nil {
		return
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

func aad(hello helloResponse, d worksync.Direction) worksync.AAD {
	return worksync.AAD{Session: hello.Session, Project: hello.Project, Branch: hello.Branch, Commit: hello.Commit, Direction: string(d)}
}

func writeLocalTar(dir string, entries []worksync.Entry, w io.Writer) error {
	tw := tar.NewWriter(&chunkWriter{w: w})
	for _, e := range entries {
		src := filepath.Join(dir, filepath.FromSlash(e.Path))
		if err := worksync.WriteTarEntry(tw, e.Path, src, e.Type, e.Mode); err != nil {
			return err
		}
	}
	return tw.Close()
}

// chunkWriter splits arbitrary writes into frame-sized chunks.
type chunkWriter struct{ w io.Writer }

func (c *chunkWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > worksync.FrameSize {
			n = worksync.FrameSize
		}
		if _, err := c.w.Write(p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

type apiError struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Detail  map[string]any `json:"detail"`
	} `json:"error"`
}

func parseErr(resp *http.Response) error {
	var body apiError
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Error.Code != "" {
		return worksync.NewError(body.Error.Code, body.Error.Message).WithDetail(body.Error.Detail)
	}
	return fmt.Errorf("pcoder answered HTTP %d", resp.StatusCode)
}

func postJSON[T any](url string, body any) (T, error) {
	var zero T
	raw, err := json.Marshal(body)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON[T](req)
}

func doJSON[T any](req *http.Request) (T, error) {
	var out T
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("connect to pcoder: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, parseErr(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("read pcoder response: %w", err)
	}
	return out, nil
}

func prompt(label string) string {
	fmt.Print(label)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}
