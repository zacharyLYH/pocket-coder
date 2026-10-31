package sync

import "fmt"

// Stable error codes. Both the HTTP payload and the client's printed
// guidance key off these; never rename one without a migration.
const (
	ErrProjectNotFound  = "project_not_found"
	ErrCodeInvalid      = "code_invalid"
	ErrCodeWrongProject = "code_wrong_project"
	ErrCodeExpired      = "code_expired"
	ErrCodeUsed         = "code_used"
	ErrSyncRunning      = "sync_running"
	ErrHTTPSRequired    = "https_required"
	ErrBranchMismatch   = "branch_mismatch"
	ErrCommitMismatch   = "commit_mismatch"
	ErrDetachedHead     = "detached_head"
	ErrNotGitRepo       = "not_git_repo"
	ErrRemoteMismatch   = "remote_mismatch"
	ErrSessionUnknown   = "session_unknown"
	ErrSessionExpired   = "session_expired"
	ErrBadPlan          = "bad_plan"
	ErrBadPayload       = "bad_payload"
	ErrInternal         = "internal"
)

// Error is the stable, user-facing error both sides share. The message is
// printed verbatim; detail carries the values the message names.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Error implements error.
func (e *Error) Error() string { return e.Message }

// NewError builds a coded error.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// WithDetail returns a copy carrying detail.
func (e *Error) WithDetail(detail map[string]any) *Error {
	e.Detail = detail
	return e
}

// Message-building helpers. Each one returns the exact text from the Work
// Sync spec, so the client and server can never drift.

func ProjectNotFound(id string) *Error {
	return NewError(ErrProjectNotFound,
		fmt.Sprintf("Project %q not found on this pcoder.\nCheck the name. It looks like owner/repo.", id))
}

func CodeInvalid() *Error {
	return NewError(ErrCodeInvalid, "Code is not valid.\nGenerate a new code on pcoder.")
}

func CodeWrongProject(codeProject, asked string) *Error {
	return NewError(ErrCodeWrongProject, fmt.Sprintf(
		"Code is for %q, not %q.\nOpen %s on pcoder and generate a code there.", codeProject, asked, codeProject)).
		WithDetail(map[string]any{"code": codeProject, "laptop": asked})
}

func CodeExpired() *Error {
	return NewError(ErrCodeExpired, "Code expired.\nGenerate a new code on pcoder.")
}

func CodeUsed() *Error {
	return NewError(ErrCodeUsed, "Code already used.\nGenerate a new code on pcoder.")
}

func SyncRunning() *Error {
	return NewError(ErrSyncRunning, "Sync already running for this project.\nIf nothing seems to be happening, just wait — it sorts itself out within 10 minutes, then generate a new code.")
}

func HTTPSRequired() *Error {
	return NewError(ErrHTTPSRequired, "HTTPS is required.\nOpen pcoder over HTTPS, or use a loopback address for local development.")
}

func BranchMismatch(laptop, pcoder string) *Error {
	return NewError(ErrBranchMismatch, fmt.Sprintf(
		"Branch mismatch.\nLaptop: %s\npcoder: %s\nCheckout the same branch on both sides, then run sync again.",
		laptop, pcoder)).WithDetail(map[string]any{"laptop": laptop, "pcoder": pcoder})
}

func CommitMismatch(laptop, pcoder string) *Error {
	return NewError(ErrCommitMismatch, fmt.Sprintf(
		"Commit mismatch.\nLaptop: %s\npcoder: %s\nSync needs the same commit on both sides. Pull or reset first.",
		ShortSHA(laptop), ShortSHA(pcoder))).WithDetail(map[string]any{"laptop": laptop, "pcoder": pcoder})
}

func DetachedHead() *Error {
	return NewError(ErrDetachedHead, "Detached HEAD on one side.\nCheckout the branch you want to sync, then run sync again.")
}

func NotGitRepo() *Error {
	return NewError(ErrNotGitRepo, "Folder is not a git repository.\nRun this inside your project, or pass --dir /path/to/project.")
}

func RemoteMismatch(project, folder string) *Error {
	return NewError(ErrRemoteMismatch, fmt.Sprintf(
		"Folder remote does not match project.\nProject: %s\nFolder: %s", project, folder)).
		WithDetail(map[string]any{"project": project, "folder": folder})
}

func SessionUnknown() *Error {
	return NewError(ErrSessionUnknown, "Sync session not found.\nGenerate a new code on pcoder.")
}

func SessionExpired() *Error {
	return NewError(ErrSessionExpired, "Sync session expired.\nGenerate a new code on pcoder.")
}

func BadPlan(msg string) *Error {
	return NewError(ErrBadPlan, "Sync plan is not valid.\n"+msg+"\nGenerate a new code on pcoder and try again.")
}

func BadPayload(msg string) *Error {
	return NewError(ErrBadPayload, "Sync payload did not verify.\n"+msg+"\nNothing was applied. Run sync again.")
}

// ShortSHA is the 7-character form users see in messages.
func ShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
