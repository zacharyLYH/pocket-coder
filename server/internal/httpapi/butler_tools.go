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
	butlerToolServerPublicKey  = "server_public_key"
	butlerToolArchitecture     = "architecture"
	butlerToolFleetHealth      = "fleet_health"
	butlerToolSSHProbe         = "ssh_probe"
	butlerToolSMTPStatus       = "smtp_status"
	butlerToolListShortcuts    = "list_shortcuts"
	butlerToolHarnessDetail    = "harness_detail"
	butlerToolProjectHarnesses = "project_harnesses"
	butlerToolThreadRecap      = "thread_recap"
)

// Write tools: propose-only, Confirm applies. Safe first, sensitive after.
const (
	butlerToolCreateProject  = "create_project"
	butlerToolStart          = "start_project"
	butlerToolStop           = "stop_project"
	butlerToolRestart        = "restart_project"
	butlerToolSessionCreate  = "session_create"
	butlerToolSessionKill    = "session_kill"
	butlerToolSessionRestart = "session_restart"
	butlerToolSessionRename  = "session_rename"
	butlerToolPreviewStart   = "preview_start"
	butlerToolPreviewClose   = "preview_close"
	butlerToolGitPull        = "git_pull"
	butlerToolGitPush        = "git_push"
	butlerToolGitSwitch      = "git_switch"
	butlerToolDeleteProject  = "delete_project"
	butlerToolCreateHarness  = "create_harness"
	butlerToolInstallHarness = "install_harness"
	butlerToolDeleteHarness  = "delete_harness"
	butlerToolFanoutExec     = "fanout_exec"
	butlerToolCreateAIModel  = "create_ai_model"
	butlerToolUpdateAIModel  = "update_ai_model"
	butlerToolSaveShortcut   = "save_shortcut"
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
	butlerToolServerPublicKey,
	butlerToolArchitecture,
	butlerToolFleetHealth,
	butlerToolSSHProbe,
	butlerToolSMTPStatus,
	butlerToolListShortcuts,
	butlerToolHarnessDetail,
	butlerToolProjectHarnesses,
	butlerToolThreadRecap,
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
	butlerToolCreateAIModel,
	butlerToolUpdateAIModel,
	butlerToolSaveShortcut,
}
