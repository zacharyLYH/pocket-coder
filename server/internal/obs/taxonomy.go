package obs

// Canonical log taxonomy. Every observe key is a const here; call sites use
// the const, never a literal — so renames propagate and the Audit/Build
// sets below (which reference the same consts) cannot drift from what
// handlers actually emit.
//
// Rule of thumb when adding a key: user-initiated milestones go in
// AuditTypes; container/project lifecycle goes in BuildTypes; per-step or
// per-interaction noise (tool calls, clicks, keystrokes, polls) goes in
// neither. Events-log-only keys (harness.launch, validation.failed,
// login.*, boot) must NOT appear here — they are never observe lines, so
// filtering the observe tail by them can only match nothing.

const (
	// Project lifecycle (project.Service + project handlers).
	ProjectCreate     = "project.create"
	ProjectReady      = "project.ready"
	ProjectGet        = "project.get"
	ProjectPatch      = "project.patch"
	ProjectDelete     = "project.delete"
	ProjectStart      = "project.start"
	ProjectStop       = "project.stop"
	ProjectRestart    = "project.restart"
	ProjectImageBuild = "project.image.build"
	ProjectReconcile  = "project.reconcile"
	ProjectClone      = "project.clone"
	ProjectHarnesses  = "project.harnesses"
	ProjectBootstrap  = "project.bootstrap"
	ProjectSSHKeys    = "project.sshkeys"

	// Sessions + terminal transport.
	SessionList    = "session.list"
	SessionCreate  = "session.create"
	SessionKill    = "session.kill"
	SessionDelete  = "session.delete"
	SessionRestart = "session.restart"
	SessionRename  = "session.rename"
	SessionInject  = "session.inject"
	TerminalAttach = "terminal.attach"

	// Multi-project fan-out.
	ProjectsExec   = "projects.exec"
	HarnessInstall = "harness.install"

	// Git tab.
	GitStatus    = "git.status"
	GitDiff      = "git.diff"
	GitStage     = "git.stage"
	GitStageHunk = "git.stage_hunk"

	// Codemap turns + threads + file reader.
	CodemapBusy              = "codemap.busy"
	CodemapTurn              = "codemap.turn"
	CodemapTurnReserved      = "codemap.turn_reserved"
	CodemapThreadInitialized = "codemap.thread_initialized"
	CodemapRetryReserved     = "codemap.retry_reserved"
	CodemapStart             = "codemap.start"
	CodemapRound             = "codemap.round"
	CodemapTool              = "codemap.tool"
	CodemapToolResult        = "codemap.tool_result"
	CodemapRefDropped        = "codemap.ref_dropped"
	CodemapLineage           = "codemap.lineage"
	CodemapTurnSaved         = "codemap.turn_saved"
	CodemapDone              = "codemap.done"
	CodemapAsk               = "codemap.ask"
	CodemapRetry             = "codemap.retry"
	CodemapFile              = "codemap.file"
	CodemapThreads           = "codemap.threads"
	CodemapThread            = "codemap.thread"
	CodemapThreadDeleted     = "codemap.thread_deleted"

	// Preview worker + tools.
	PreviewStart      = "preview.start"
	PreviewClose      = "preview.close"
	PreviewOpen       = "preview.open"
	PreviewStatus     = "preview.status"
	PreviewHeartbeat  = "preview.heartbeat"
	PreviewSurface    = "preview.surface"
	PreviewPorts      = "preview.ports"
	PreviewScreenshot = "preview.screenshot"
	PreviewInspect    = "preview.inspect"
	PreviewConsole    = "preview.console"
	PreviewNetwork    = "preview.network"
	PreviewNavigate   = "preview.navigate"
	PreviewClick      = "preview.click"
	PreviewType       = "preview.type"
	PreviewReload     = "preview.reload"
	PreviewScroll     = "preview.scroll"
	PreviewViewport   = "preview.viewport"

	// Observe reads.
	ObserveRead = "observe.read"
)

// BuildTypes are the create→ready pipeline plus lifecycle: the Build view.
var BuildTypes = []string{
	ProjectCreate,
	ProjectImageBuild,
	ProjectClone,
	ProjectReady,
	ProjectReconcile,
	ProjectStart,
	ProjectStop,
	ProjectRestart,
}

// AuditTypes are the milestone subset of the runtime tail: one entry per
// user-meaningful action, not per step. Backs the audit filter.
var AuditTypes = []string{
	ProjectCreate,
	ProjectReady,
	ProjectClone,
	ProjectReconcile,
	ProjectStart,
	ProjectStop,
	ProjectRestart,
	ProjectDelete,
	ProjectPatch,
	ProjectImageBuild,
	ProjectBootstrap,
	SessionCreate,
	SessionDelete,
	SessionRestart,
	SessionRename,
	SessionInject,
	SessionKill,
	TerminalAttach,
	HarnessInstall,
	ProjectsExec,
	CodemapDone,
	CodemapThreadDeleted,
	PreviewStart,
	PreviewClose,
	PreviewOpen,
	PreviewNavigate,
	GitStage,
	GitStageHunk,
}
