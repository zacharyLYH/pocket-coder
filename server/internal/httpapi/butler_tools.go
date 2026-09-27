// Butler tool names. Consts, not raw strings: every registry row, prompt
// list, and test references one of these, so a rename touches one line.
// butlerReadNames and butlerWriteNames drive construction; TestButlerToolNames
// pins them against the registries and the prompt.
package httpapi

// Read tools: free, no confirm.
const (
	butlerToolListProjects     = "list_projects"
	butlerToolProjectDetail    = "project_detail"
	butlerToolListSessions     = "list_sessions"
	butlerToolPreviewState     = "preview_state"
	butlerToolGitMeta          = "git_meta"
	butlerToolEventsTail       = "events_tail"
	butlerToolHealth           = "health"
	butlerToolHarnessInventory = "harness_inventory"
	butlerToolEnvNames         = "env_names"
	butlerToolConfigStatus     = "config_status"
	butlerToolListAIModels     = "list_ai_models"
	butlerToolArchitecture     = "architecture"
)

// Write tools: propose-only, Confirm applies. Safe first, sensitive after.
const (
	butlerToolCreateProject   = "create_project"
	butlerToolStart           = "start"
	butlerToolStop            = "stop"
	butlerToolRestart         = "restart"
	butlerToolSessionCreate   = "session_create"
	butlerToolSessionKill     = "session_kill"
	butlerToolSessionRestart  = "session_restart"
	butlerToolSessionRename   = "session_rename"
	butlerToolPreviewStart    = "preview_start"
	butlerToolPreviewClose    = "preview_close"
	butlerToolGitPull         = "git_pull"
	butlerToolGitPush         = "git_push"
	butlerToolGitSwitch       = "git_switch"
	butlerToolDeleteProject   = "delete_project"
	butlerToolCreateHarness   = "create_harness"
	butlerToolInstallHarness  = "install_harness"
	butlerToolDeleteHarness   = "delete_harness"
	butlerToolFanoutExec      = "fanout_exec"
	butlerToolProposeEnvFix   = "propose_env_fix"
	butlerToolSwitchModel     = "switch_model"
	butlerToolUpdateAIModel   = "update_ai_model"
	butlerToolSaveShortcut    = "save_shortcut"
	butlerToolSaveGitIdentity = "save_git_identity"
	butlerToolAddSSHKey       = "add_ssh_key"
)

// butlerReadNames is every read tool in registry order.
var butlerReadNames = []string{
	butlerToolListProjects,
	butlerToolProjectDetail,
	butlerToolListSessions,
	butlerToolPreviewState,
	butlerToolGitMeta,
	butlerToolEventsTail,
	butlerToolHealth,
	butlerToolHarnessInventory,
	butlerToolEnvNames,
	butlerToolConfigStatus,
	butlerToolListAIModels,
	butlerToolArchitecture,
}

// butlerWriteNames is every write tool in registry order.
var butlerWriteNames = []string{
	butlerToolCreateProject,
	butlerToolStart,
	butlerToolStop,
	butlerToolRestart,
	butlerToolSessionCreate,
	butlerToolSessionKill,
	butlerToolSessionRestart,
	butlerToolSessionRename,
	butlerToolPreviewStart,
	butlerToolPreviewClose,
	butlerToolGitPull,
	butlerToolGitPush,
	butlerToolGitSwitch,
	butlerToolDeleteProject,
	butlerToolCreateHarness,
	butlerToolInstallHarness,
	butlerToolDeleteHarness,
	butlerToolFanoutExec,
	butlerToolProposeEnvFix,
	butlerToolSwitchModel,
	butlerToolUpdateAIModel,
	butlerToolSaveShortcut,
	butlerToolSaveGitIdentity,
	butlerToolAddSSHKey,
}
