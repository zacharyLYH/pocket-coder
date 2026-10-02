package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/agent"
	"pcoder/internal/docker"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/prompt"
	"pcoder/internal/state"
	"pcoder/internal/threads"
)

// butlerToolByName finds one tool in a registry by name, reserving its
// thread. Returns the run func and the thread id holding its approvals.
func butlerToolByName(t *testing.T, d Deps, name string) (func(ctx context.Context, args string) (string, error), string) {
	t.Helper()
	threadID, _, err := d.Butler.ReserveNewThread(butlerScope, "test approval", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range append(butlerReadTools(d), butlerWriteTools(d, d.Butler, threadID, "turn-test")...) {
		if tl.Name == name {
			return tl.Run, threadID
		}
	}
	t.Fatalf("no such butler tool %q", name)
	return nil, ""
}

// Full read-tool pass against fakes: names and counts only — no paths,
// no hunks, no secret values.
func TestButlerReadTools(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	byName := map[string]func(ctx context.Context, args string) (string, error){}
	for _, tl := range butlerReadTools(d) {
		byName[tl.Name] = tl.Run
	}
	if len(byName) != len(butlerReadNames) {
		t.Fatalf("read tools = %d, want %d", len(byName), len(butlerReadNames))
	}

	// list_projects: ids, branches, container status — no repo URLs.
	out, err := byName[butlerToolListProjects](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list_projects: %v", err)
	}
	if !strings.Contains(out, `"a/b"`) || !strings.Contains(out, `"main"`) || !strings.Contains(out, `"running"`) {
		t.Fatalf("list_projects = %s, want id+branch+status", out)
	}
	if strings.Contains(out, "github.com") {
		t.Fatalf("list_projects leaks repo URL: %s", out)
	}

	// project_detail: one project, no paths.
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	out, err = byName[butlerToolProjectDetail](context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("project_detail: %v", err)
	}
	if !strings.Contains(out, `"a/b"`) || !strings.Contains(out, `"preview":"stopped"`) {
		t.Fatalf("project_detail = %s, want id + preview slot", out)
	}

	// list_sessions: names plus alive + harness, no paths.
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "one\ntwo\n"}, nil)
	for _, name := range []string{"one", "two"} {
		md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
			return len(argv) == 3 && strings.Contains(argv[2], "has-session -t "+name)
		}), false).Return(docker.ExecResult{ExitCode: 0}, nil)
	}
	out, err = byName[butlerToolListSessions](context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("list_sessions: %v", err)
	}
	var sess []struct {
		Name  string `json:"name"`
		Alive bool   `json:"alive"`
	}
	if err := json.Unmarshal([]byte(out), &sess); err != nil {
		t.Fatalf("list_sessions not JSON: %v (%s)", err, out)
	}
	if len(sess) != 2 || !sess[0].Alive || !sess[1].Alive {
		t.Fatalf("list_sessions = %s, want names + alive", out)
	}

	// preview_state: readiness slots + ports, no endpoints or tokens.
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "ss -tlnp")
	}), mock.Anything).Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil)
	out, err = byName[butlerToolPreviewState](context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("preview_state: %v", err)
	}
	if !strings.Contains(out, `"stopped"`) || strings.Contains(out, "token") {
		t.Fatalf("preview_state = %s, want stopped slot, never tokens", out)
	}

	// git_meta: counts + upstream + unborn/detached, no paths or hunks.
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "rev-parse --abbrev-ref @{u}")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "origin/main"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "rev-parse --abbrev-ref")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "main"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "status --porcelain")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "META-BEGIN\n M a\nM  b\n"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "rev-list --left-right")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "1 2"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "rev-parse --verify --quiet HEAD")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "0"}, nil)
	out, err = byName[butlerToolGitMeta](context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("git_meta: %v", err)
	}
	var meta struct {
		Branch       string `json:"branch"`
		Upstream     string `json:"upstream"`
		ChangedFiles int    `json:"changedFiles"`
		Ahead        int    `json:"ahead"`
		Behind       int    `json:"behind"`
		Unborn       bool   `json:"unborn"`
		Detached     bool   `json:"detached"`
	}
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		t.Fatalf("git_meta not JSON: %v (%s)", err, out)
	}
	if meta.Branch != "main" || meta.Upstream != "origin/main" || meta.ChangedFiles != 2 ||
		meta.Ahead != 2 || meta.Behind != 1 || meta.Unborn || meta.Detached {
		t.Fatalf("git_meta = %s", out)
	}

	// events_tail: types + times only.
	if _, err := d.Events.Append("butler.turn", map[string]any{"threadId": "x"}); err != nil {
		t.Fatal(err)
	}
	out, err = byName[butlerToolEventsTail](context.Background(), `{"limit":5}`)
	if err != nil {
		t.Fatalf("events_tail: %v", err)
	}
	if !strings.Contains(out, `"butler.turn"`) || strings.Contains(out, "threadId") {
		t.Fatalf("events_tail = %s, want types only", out)
	}

	// health: version + docker + uptime + disk, no secrets.
	out, err = byName[butlerToolHealth](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !strings.Contains(out, `"version"`) || !strings.Contains(out, `"diskFreeBytes"`) {
		t.Fatalf("health = %s", out)
	}

	// harness_inventory: names + hasInstall, no commands.
	out, err = byName[butlerToolHarnessInventory](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("harness_inventory: %v", err)
	}
	if !strings.Contains(out, `"Fake"`) || strings.Contains(out, "fakecli") && strings.Contains(out, `"command"`) {
		t.Fatalf("harness_inventory = %s, want names only", out)
	}

	// env_names + config_status: names and booleans only — key values
	// never render even though the state file holds them.
	out, err = byName[butlerToolEnvNames](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("env_names: %v", err)
	}
	if !strings.Contains(out, "SMTP_PASSWORD") {
		t.Fatalf("env_names = %s", out)
	}
	for _, name := range []string{"PCODER_DATA_DIR", "PCODER_BIND", "PCODER_DOCKER_SOCK"} {
		if !strings.Contains(out, name) {
			t.Fatalf("env_names = %s, want documented %s", out, name)
		}
	}
	out, err = byName[butlerToolConfigStatus](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("config_status: %v", err)
	}
	var cfgStatus map[string]any
	if err := json.Unmarshal([]byte(out), &cfgStatus); err != nil {
		t.Fatalf("config_status not JSON: %v", err)
	}
	for _, v := range cfgStatus {
		if _, ok := v.(bool); !ok {
			t.Fatalf("config_status = %s, want booleans only", out)
		}
	}
	if strings.Contains(out, "test-token") || strings.Contains(out, `"k"`) {
		t.Fatalf("config_status leaks secrets: %s", out)
	}

	// list_ai_models: entries with id/label/model/hasKey, never key values.
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{
			{ID: "m1", Label: "liq", BaseURL: "https://x", APIKey: "secret-key-1", Model: "gpt-3.5"},
			{ID: "m2", Label: "four", BaseURL: "https://y", APIKey: "secret-key-2", Model: "gpt-4o"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err = byName[butlerToolListAIModels](context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list_ai_models: %v", err)
	}
	for _, want := range []string{"m1", "liq", "gpt-3.5", "m2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list_ai_models = %s, want %q", out, want)
		}
	}
	for _, banned := range []string{"secret-key-1", "secret-key-2"} {
		if strings.Contains(out, banned) {
			t.Fatalf("list_ai_models leaks key values: %s", out)
		}
	}
	_ = cookie
	_ = h
}

// The consts drive everything: the read/write registries build in const
// order, so the lists and the registries cannot drift.
func TestButlerToolNames(t *testing.T) {
	d, _, _, _ := newSessionDeps(t)
	if len(butlerReadNames) != 12 || len(butlerWriteNames) != 22 {
		t.Fatalf("consts = %d reads + %d writes, want 12 + 22",
			len(butlerReadNames), len(butlerWriteNames))
	}
	var built []string
	for _, tl := range butlerReadTools(d) {
		built = append(built, tl.Name)
	}
	for _, def := range butlerWriteTable {
		built = append(built, def.name)
	}
	want := append(append([]string{}, butlerReadNames...), butlerWriteNames...)
	if strings.Join(built, ",") != strings.Join(want, ",") {
		t.Fatalf("registry order = %v, consts = %v: registries build from the consts", built, want)
	}
	seen := map[string]bool{}
	for _, n := range want {
		if seen[n] {
			t.Fatalf("duplicate tool name %q", n)
		}
		seen[n] = true
	}
}

// The guide locks the scope: it carries the yes/no question, every tool
// name, the non-goals, and the pinned refusal. Built from the same
// consts, so a new tool documents itself in the prompt. Workflows are
// not in here: they ride the second prompt (TestButlerTurnSecondPrompt).
func TestButlerGuideLocksScope(t *testing.T) {
	names := append(append([]string{}, butlerReadNames...), butlerWriteNames...)
	for _, n := range names {
		if !strings.Contains(butlerGuide, n) {
			t.Fatalf("guide omits tool %q: registry and guide drifted", n)
		}
	}
	for _, want := range []string{
		"is this something you can help with",   // the yes/no lockdown
		"read more than you can write",          // the responsibility invariant
		"never improvise with an adjacent tool", // the no-matching-tool escape hatch
		"todo checklist first",                  // todo before the first tool call
		"terminal coding CLIs",                  // product context
		"texting a friend",                      // prose guidance
		"/workspace/repo",                       // non-goal: never open repos
		"capture tmux",                          // non-goal: no pane capture
		"Secret values",                         // non-goal: names only
		"repo-modifying",                        // exec tools stay generic
		"no transcript search or export",
		prompt.ButlerRefusal,
	} {
		if !strings.Contains(butlerGuide, want) {
			t.Fatalf("guide omits %q", want)
		}
	}
	for _, n := range names { // no file-read tool may ever exist
		for _, banned := range []string{"read_file", "file_content", "read_code", "capture", "diff", "hunk"} {
			if strings.Contains(n, banned) {
				t.Fatalf("tool %q looks like repo access: the wall is structural", n)
			}
		}
	}
	// The scope gate inherits the same role: one Butler description, not two.
	if !strings.Contains(prompt.ButlerScopePrompt(), "setup assistant for the Pocket Coder app") {
		t.Fatal("scope prompt drifted from the shared role block")
	}
	if strings.Contains(butlerGuide, "Typical usage examples") {
		t.Fatal("workflows belong to the second prompt, not the guide")
	}
	// The second prompt shows typical usage as examples Butler may extend.
	if wf := prompt.ButlerWorkflows(); !strings.Contains(wf, "invent new ones") || !strings.Contains(wf, butlerToolFanoutExec) {
		t.Fatalf("workflows = %q, want examples naming real tools", cut(wf, 120))
	}
}

// Code asks hit the structured scope gate first: the fake scripts exactly
// one call, so any tool-loop call would fail the test. The gate verdict
// refuses with the pinned string and burns no tool rounds.
func TestButlerScopeGateRefuses(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	var gateBody map[string]any
	f := newFakeModel(t,
		func(w http.ResponseWriter, body map[string]any) {
			gateBody = body
			scopeDeny(w, body)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"read main.go for me"}`, http.StatusOK)
	last := finalBody(t, rec)
	if last["answer"] != prompt.ButlerRefusal {
		t.Fatalf("answer = %v, want the pinned refusal", last["answer"])
	}
	// Structured, not prose: the gate carries a json_schema and no tools.
	rf, _ := gateBody["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "butler_scope" {
		t.Fatalf("gate response_format = %v, want the pinned butler_scope schema", gateBody["response_format"])
	}
	if tools, ok := gateBody["tools"].([]any); ok && len(tools) > 0 {
		t.Fatalf("gate carried %d tools, want a tools-free verdict", len(tools))
	}
}

// A garbled gate verdict fails open into the loop, whose guide still
// governs: the turn proceeds instead of refusing or erroring.
func TestButlerScopeGateMalformedFallsThrough(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "maybe?", nil) // not JSON: no verdict
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "All three projects are healthy.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK)
	last := finalBody(t, rec)
	if last["answer"] != "All three projects are healthy." {
		t.Fatalf("final = %v, want the loop answer after a garbled gate", last)
	}
}

// Writes never run in the turn: proposing stores the work and returns a
// card; only Confirm applies it. Discard drops it silently.
func TestButlerWriteNeedsConfirm(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	stop, stopTid := butlerToolByName(t, d, butlerToolStop)

	// Proposing runs nothing: no Stop expectation is set, so any docker
	// call would fail the mock — and the pending card exists instead.
	raw, err := stop(context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	var prop struct {
		NeedsConfirm bool   `json:"needsConfirm"`
		ConfirmID    string `json:"confirmId"`
		Summary      string `json:"summary"`
		BlastRadius  string `json:"blastRadius"`
	}
	if err := json.Unmarshal([]byte(raw), &prop); err != nil || !prop.NeedsConfirm || prop.ConfirmID == "" {
		t.Fatalf("proposal = %s (err %v), want needsConfirm + confirmId", raw, err)
	}
	if prop.BlastRadius == "" {
		t.Fatalf("proposal = %s, want a blast-radius card", raw)
	}
	apprs, aerr := d.Butler.Approvals(stopTid)
	if aerr != nil || len(apprs) != 1 || apprs[0].ID != prop.ConfirmID {
		t.Fatalf("approvals = %+v, want the stored card", apprs)
	}
	if n := d.Butler.ApprovalCount(); n != 1 {
		t.Fatalf("pendings = %d, want 1", n)
	}

	// Confirm applies: now the docker call is expected and runs.
	md.EXPECT().Stop(mock.Anything, "pcoder-a-b", mock.Anything).Return(nil)
	rec := authedPost(t, h, cookie, "/api/butler/confirms/"+prop.ConfirmID+"/apply", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: got %d %q", rec.Code, rec.Body.String())
	}
	if n := d.Butler.ApprovalCount(); n != 0 {
		t.Fatalf("pendings after apply = %d, want 0", n)
	}

	// Discard does nothing: propose again, discard, pending gone, no call.
	raw2, err := stop(context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("propose 2: %v", err)
	}
	var prop2 struct {
		ConfirmID string `json:"confirmId"`
	}
	if err := json.Unmarshal([]byte(raw2), &prop2); err != nil {
		t.Fatal(err)
	}
	rec = authedPost(t, h, cookie, "/api/butler/confirms/"+prop2.ConfirmID+"/discard", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard: got %d %q", rec.Code, rec.Body.String())
	}
	if n := d.Butler.ApprovalCount(); n != 0 {
		t.Fatalf("pendings after discard = %d, want 0", n)
	}
	a, _, err := d.Butler.FindApproval(prop2.ConfirmID)
	if err != nil || a.Status != threads.ApprovalDiscarded {
		t.Fatalf("discarded approval = %+v, err=%v", a, err)
	}
	// Discard only resolves: no closure turn is added.
	if n := d.Butler.ApprovalCount(); n != 0 {
		t.Fatalf("pendings after discard = %d, want 0", n)
	}
}

// Every write tool proposes: valid args store a blast-radius card and run
// nothing; invalid args fail with no card. One row per tool, shared
// fixture (only delete_project's blast touches docker, for its session
// count — everything else is pure validation or state reads).
func TestButlerWriteProposeAll(t *testing.T) {
	d, md, _, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "m1", Model: "gpt-3.5"}, {ID: "m2", Model: "gpt-4o"}}
		doc.Harnesses["fake"] = state.Harness{ID: "fake", Name: "Fake", Command: "fake", Config: json.RawMessage(`{"model":"gpt-3.5"}`)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "one\n"}, nil)

	rows := []struct{ tool, args string }{
		{butlerToolCreateProject, `{"repoUrl":"https://github.com/x/y.git","branch":"main"}`},
		{butlerToolStart, `{"project":"a/b"}`},
		{butlerToolStop, `{"project":"a/b"}`},
		{butlerToolRestart, `{"project":"a/b"}`},
		{butlerToolSessionCreate, `{"project":"a/b","name":"dev"}`},
		{butlerToolSessionKill, `{"project":"a/b","name":"dev"}`},
		{butlerToolSessionRestart, `{"project":"a/b","name":"dev"}`},
		{butlerToolSessionRename, `{"project":"a/b","old":"dev","new":"dev2"}`},
		{butlerToolPreviewStart, `{"project":"a/b","port":3000}`},
		{butlerToolPreviewClose, `{"project":"a/b"}`},
		{butlerToolGitPull, `{"project":"a/b"}`},
		{butlerToolGitPush, `{"project":"a/b"}`},
		{butlerToolGitSwitch, `{"project":"a/b","branch":"dev"}`},
		{butlerToolDeleteProject, `{"project":"a/b","scope":"container"}`},
		{butlerToolCreateHarness, `{"name":"H","command":"hcli"}`},
		{butlerToolInstallHarness, `{"harness":"fake","projects":["a/b"]}`},
		{butlerToolDeleteHarness, `{"id":"fake"}`},
		{butlerToolFanoutExec, `{"projects":["a/b"],"command":"echo hi"}`},
		{butlerToolProposeEnvFix, `{"name":"SMTP_PASSWORD"}`},
		{butlerToolSwitchModel, `{"harness":"fake","model":"gpt-4o"}`},
		{butlerToolUpdateAIModel, `{"id":"m2","label":"Production"}`},
		{butlerToolSaveShortcut, `{"project":"a/b","alias":"retest","kind":"cmd","command":"npm test"}`},
	}
	if len(rows) != len(butlerWriteTable) {
		t.Fatalf("table rows = %d, write tools = %d: add the new tool here", len(rows), len(butlerWriteTable))
	}
	for _, row := range rows {
		run, _ := butlerToolByName(t, d, row.tool)
		raw, err := run(context.Background(), row.args)
		if err != nil {
			t.Fatalf("%s propose: %v", row.tool, err)
		}
		var prop struct {
			NeedsConfirm bool   `json:"needsConfirm"`
			ConfirmID    string `json:"confirmId"`
			Summary      string `json:"summary"`
			BlastRadius  string `json:"blastRadius"`
		}
		if err := json.Unmarshal([]byte(raw), &prop); err != nil || !prop.NeedsConfirm || prop.ConfirmID == "" {
			t.Fatalf("%s proposal = %s, want needsConfirm + confirmId", row.tool, raw)
		}
		if prop.Summary == "" || prop.BlastRadius == "" {
			t.Fatalf("%s proposal = %s, want summary + blast radius", row.tool, raw)
		}
	}
	if n := d.Butler.ApprovalCount(); n != len(rows) {
		t.Fatalf("pendings = %d, want %d (nothing ran, everything stored)", n, len(rows))
	}
	beforeUnknown := d.Butler.ApprovalCount()
	run, _ := butlerToolByName(t, d, butlerToolSwitchModel)
	if _, err := run(context.Background(), `{"harness":"fake","model":"not-configured"}`); err == nil {
		t.Fatal("unknown model was proposed")
	}
	if d.Butler.ApprovalCount() != beforeUnknown {
		t.Fatal("unknown model created an approval")
	}

	bad := []struct{ tool, args string }{
		{butlerToolCreateProject, `{}`},
		{butlerToolStart, `{}`},
		{butlerToolSessionCreate, `{"project":"a/b","name":"bad name!"}`},
		{butlerToolPreviewStart, `{"project":"a/b","port":0}`},
		{butlerToolPreviewStart, `{"project":"a/b","port":70000}`},
		{butlerToolSessionKill, `{"project":"a/b","name":"bad name!"}`},
		{butlerToolSessionRestart, `{"project":"a/b","name":""}`},
		{butlerToolDeleteProject, `{"project":"a/b","scope":"everything"}`},
		{butlerToolGitSwitch, `{"project":"a/b"}`},
		{butlerToolInstallHarness, `{"harness":"fake","projects":[]}`},
		{butlerToolFanoutExec, `{"projects":[],"command":"echo hi"}`},
		{butlerToolProposeEnvFix, `{}`},
		{butlerToolUpdateAIModel, `{"id":"nope","label":"X"}`},
		{butlerToolUpdateAIModel, `{"id":"m1"}`},
		{butlerToolSaveShortcut, `{"project":"a/b","alias":"x","kind":"bogus"}`},
	}
	before := d.Butler.ApprovalCount()
	for _, row := range bad {
		run, _ := butlerToolByName(t, d, row.tool)
		if _, err := run(context.Background(), row.args); err == nil {
			t.Fatalf("%s with %s: want validation error", row.tool, row.args)
		}
	}
	if n := d.Butler.ApprovalCount(); n != before {
		t.Fatalf("pendings = %d, want %d (invalid args store nothing)", n, before)
	}
}

// State-only applies: propose, take, run — then the registry shows it.
// No docker involved: harnesses, shortcuts, model lines, keys.
func TestButlerWriteApplyStateOnly(t *testing.T) {
	d, _, _, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(doc *state.Document) error {
		doc.User.Email = "me@example.com"
		doc.AIModels = []state.AIModel{{ID: "m1", Label: "liq", BaseURL: "https://x", APIKey: "k", Model: "gpt-3.5"}, {ID: "m2", Label: "GPT-4o", BaseURL: "https://x", APIKey: "k2", Model: "gpt-4o"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	apply := func(tool, args string) string {
		t.Helper()
		run, _ := butlerToolByName(t, d, tool)
		raw, err := run(context.Background(), args)
		if err != nil {
			t.Fatalf("%s propose: %v", tool, err)
		}
		var prop struct {
			ConfirmID string `json:"confirmId"`
		}
		if err := json.Unmarshal([]byte(raw), &prop); err != nil {
			t.Fatal(err)
		}
		out, err := applyButlerTestApproval(t, d, prop.ConfirmID, "")
		if err != nil {
			t.Fatalf("%s apply: %v", tool, err)
		}
		return out
	}

	apply(butlerToolCreateHarness, `{"name":"H","command":"hcli","install":"npm i -g hcli"}`)
	if err := st.Mutate(func(doc *state.Document) error {
		h := doc.Harnesses["h"]
		h.Config = json.RawMessage(`{"model":"gpt-3.5"}`)
		doc.Harnesses["h"] = h
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Harnesses.Get("h"); err != nil {
		t.Fatalf("harness h missing after apply: %v", err)
	}
	apply(butlerToolSwitchModel, `{"harness":"h","model":"gpt-4o"}`)
	h, _ := d.Harnesses.Get("h")
	if path, got, ok := butlerFindModel(h.Config); !ok || got != "gpt-4o" {
		t.Fatalf("harness model = %v %q, want [model] gpt-4o", path, got)
	}
	apply(butlerToolUpdateAIModel, `{"id":"m1","label":"Liquid"}`)
	st.View(func(doc *state.Document) {
		m := doc.AIModels[0]
		if m.Label != "Liquid" || m.Model != "gpt-3.5" || m.APIKey != "k" {
			t.Fatalf("after rename: %+v, want label changed, model+key untouched", m)
		}
	})
	if out := apply(butlerToolUpdateAIModel, `{"id":"m1","label":"Liquid"}`); !strings.Contains(out, "Liquid") {
		t.Fatalf("update_ai_model result = %q", out)
	}
	apply(butlerToolSaveShortcut, `{"project":"a/b","alias":"retest","kind":"cmd","command":"npm test"}`)
	var shortcuts []state.Shortcut
	st.View(func(doc *state.Document) { shortcuts = doc.Projects["a/b"].Shortcuts })
	if len(shortcuts) != 1 || shortcuts[0].Alias != "retest" || shortcuts[0].Command != "npm test" {
		t.Fatalf("shortcuts = %+v, want the saved row", shortcuts)
	}
	apply(butlerToolDeleteHarness, `{"id":"h"}`)
	if _, err := d.Harnesses.Get("h"); err == nil {
		t.Fatal("harness h survives delete apply")
	}
}

// Live-state enum injection: write-tool schemas pin project/harness/model
// ids at build time, so a model cannot hallucinate an id — the provider
// rejects the call before the tool ever runs.
func TestButlerWriteSchemaEnums(t *testing.T) {
	d, _, _, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "m1", Model: "gpt-4o"}}
		doc.Harnesses["fake"] = state.Harness{ID: "fake", Name: "Fake", Command: "fake"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	enums := butlerLiveEnums(d)
	if got := enums[butlerToolStart]["project"]; len(got) != 1 || got[0] != "a/b" {
		t.Fatalf("start project enum = %v, want [a/b]", got)
	}
	if got := enums[butlerToolSwitchModel]["harness"]; len(got) != 1 || got[0] != "fake" {
		t.Fatalf("switch_model harness enum = %v, want [fake]", got)
	}
	if got := enums[butlerToolUpdateAIModel]["id"]; len(got) != 1 || got[0] != "m1" {
		t.Fatalf("update_ai_model id enum = %v, want [m1]", got)
	}
	// The shipped tool schema carries the enum, and the plain-text prop
	// (session name) stays open.
	tools := butlerWriteTools(d, d.Butler, "t", "turn")
	byName := map[string]agent.Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	raw, _ := json.Marshal(byName[butlerToolSwitchModel].Schema)
	if !strings.Contains(string(raw), `"enum":["fake"]`) {
		t.Fatalf("switch_model schema = %s, want enum on harness", string(raw))
	}
	raw, _ = json.Marshal(byName[butlerToolSwitchModel].Schema)
	if strings.Contains(string(raw), `"enum"`) && strings.Contains(string(raw), `"model"`) {
		// model prop may also be enum-free; only harness carries one
		if strings.Contains(string(raw), `"model":{`+`"type":"string","enum"`) {
			t.Fatalf("switch_model model prop unexpectedly constrained: %s", string(raw))
		}
	}
	// Empty state: no enum, schema untouched (stays open).
	d2, _, _, _ := newSessionDeps(t)
	if props := butlerLiveEnums(d2)[butlerToolStart]; props != nil {
		t.Fatalf("empty state produced enums: %v", props)
	}
}

// Confirm-card summaries show the human-readable name, never the minted
// hex id alone — the id may only disambiguate in the blast radius.
func TestButlerWriteSummaryUsesDisplayName(t *testing.T) {
	d, _, _, st := newSessionDeps(t)
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "988f1d277463f60a", Label: "liq", Model: "liquid/lfm-2.5-2.6b"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run, _ := butlerToolByName(t, d, butlerToolUpdateAIModel)
	raw, err := run(context.Background(), `{"id":"988f1d277463f60a","label":"liquid"}`)
	if err != nil {
		t.Fatal(err)
	}
	var prop struct{ Summary, BlastRadius string }
	if err := json.Unmarshal([]byte(raw), &prop); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prop.Summary, `"liq"`) || !strings.Contains(prop.Summary, `"liquid"`) || strings.Contains(prop.Summary, "988f1d277463f60a") {
		t.Fatalf("summary = %q, want display name, no raw id", prop.Summary)
	}
	if !strings.Contains(prop.BlastRadius, "988f1d277463f60a") || !strings.Contains(prop.BlastRadius, "liquid/lfm-2.5-2.6b") {
		t.Fatalf("blast = %q, want id + model string for disambiguation", prop.BlastRadius)
	}
}

// Container applies: start, session_create, and git_pull run their docker
// calls only after Confirm (propose sets no expectations, so any early
// call would fail the mock).
func TestButlerWriteApplyContainer(t *testing.T) {
	d, md, _, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}

	apply := func(tool, args string) {
		t.Helper()
		run, _ := butlerToolByName(t, d, tool)
		raw, err := run(context.Background(), args)
		if err != nil {
			t.Fatalf("%s propose: %v", tool, err)
		}
		var prop struct {
			ConfirmID string `json:"confirmId"`
		}
		if err := json.Unmarshal([]byte(raw), &prop); err != nil {
			t.Fatal(err)
		}
		if _, err := applyButlerTestApproval(t, d, prop.ConfirmID, ""); err != nil {
			t.Fatalf("%s apply: %v", tool, err)
		}
	}

	md.EXPECT().Start(mock.Anything, "pcoder-a-b").Return(nil)
	apply(butlerToolStart, `{"project":"a/b"}`)

	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) > 1 && argv[0] == "tmux" && argv[1] == "new-session"
	}), false).Return(docker.ExecResult{ExitCode: 0}, nil)
	apply(butlerToolSessionCreate, `{"project":"a/b","name":"dev"}`)

	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b", mock.MatchedBy(func(argv []string) bool {
		return len(argv) == 3 && strings.Contains(argv[2], "pull --ff-only")
	}), false).Return(docker.ExecResult{ExitCode: 0, Output: "Already up to date."}, nil)
	apply(butlerToolGitPull, `{"project":"a/b"}`)
}

func applyButlerTestApproval(t *testing.T, d Deps, id, secret string) (string, error) {
	t.Helper()
	a, threadID, err := d.Butler.FindApproval(id)
	if err != nil {
		return "", err
	}
	for i := range butlerWriteTable {
		if butlerWriteTable[i].name == a.Tool {
			out, err := butlerWriteTable[i].exec(context.Background(), d, butlerArgs(string(a.Args)), secret)
			if err == nil {
				err = d.Butler.ResolveApproval(threadID, id, threads.ApprovalApproved)
			}
			return out, err
		}
	}
	return "", fmt.Errorf("unknown tool %s", a.Tool)
}

// Each delete scope states its own blast radius: container takes the home
// volume, repo takes the code, metadata takes only the record, all takes
// everything.
func TestButlerDeleteBlastPerScope(t *testing.T) {
	d, md, _, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "one\n"}, nil)

	cases := []struct{ scope, want string }{
		{"container", "home volume"},
		{"repo", "repo volume (the code)"},
		{"metadata", "only the project record"},
		{"all", "both volumes, and the project record"},
	}
	for _, c := range cases {
		run, _ := butlerToolByName(t, d, butlerToolDeleteProject)
		raw, err := run(context.Background(), `{"project":"a/b","scope":"`+c.scope+`"}`)
		if err != nil {
			t.Fatalf("scope %s: %v", c.scope, err)
		}
		var prop struct {
			BlastRadius string `json:"blastRadius"`
		}
		if err := json.Unmarshal([]byte(raw), &prop); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prop.BlastRadius, c.want) {
			t.Fatalf("scope %s blast = %q, want %q", c.scope, prop.BlastRadius, c.want)
		}
	}
}

// Nested profile configs resolve to their dotted model path, and the
// apply writes back at that path instead of adding a stray top-level key.
func TestButlerNestedModel(t *testing.T) {
	raw := json.RawMessage(`{"verbose":true,"providers":{"openai":{"model":"gpt-3"}}}`)
	path, old, ok := butlerFindModel(raw)
	if !ok || old != "gpt-3" || strings.Join(path, ".") != "providers.openai.model" {
		t.Fatalf("find = %v %q, want providers.openai.model gpt-3", path, old)
	}
	out := butlerSetModel(raw, "gpt-4o")
	if path2, got, ok := butlerFindModel(out); !ok || got != "gpt-4o" || strings.Join(path2, ".") != "providers.openai.model" {
		t.Fatalf("set = %s, want the nested path updated", out)
	}
	if _, _, ok := butlerFindModel(json.RawMessage(`{"a":1}`)); ok {
		t.Fatal("find on model-less config must miss")
	}
	if _, _, ok := butlerFindModel(json.RawMessage(`not json`)); ok {
		t.Fatal("find on garbage must miss")
	}
}

// Applies serialize with turns: proposing is open, but Confirm while a
// turn runs 409s instead of interleaving a second writer.
func TestButlerApplyBusy409s(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	run, _ := butlerToolByName(t, d, butlerToolStop)
	raw, err := run(context.Background(), `{"project":"a/b"}`)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	var prop struct {
		ConfirmID string `json:"confirmId"`
	}
	if err := json.Unmarshal([]byte(raw), &prop); err != nil {
		t.Fatal(err)
	}
	// AI configured: discard now runs the closure turn, which needs a
	// model even though it proposes nothing.
	f := newFakeModel(t, func(w http.ResponseWriter, body map[string]any) {
		writeCompletion(w, "stop", "Fine — nothing ran.", nil)
	})
	seedAI(t, st, f.srv.URL)
	if !butlerRuns.take(butlerRunKey, "busy-test") {
		t.Fatal("busy slot not taken")
	}
	defer func() { butlerRuns.done(butlerRunKey) }()
	rec := authedPost(t, h, cookie, "/api/butler/confirms/"+prop.ConfirmID+"/apply", `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("apply while busy: got %d, want 409", rec.Code)
	}
	// Discard stays open: dropping a proposal is always safe.
	rec = authedPost(t, h, cookie, "/api/butler/confirms/"+prop.ConfirmID+"/discard", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("discard while busy: got %d, want 200", rec.Code)
	}
}

// Sensitive writes: the delete card states the blast radius (sessions +
// volumes), unknown confirm ids never run anything, and the masked env
// value never appears in results, events, or errors.
func TestButlerSensitiveWrites(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(doc *state.Document) error {
		doc.AIModels = []state.AIModel{{ID: "m1", Model: "gpt-4o"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Delete card: blast radius names sessions and volumes.
	md.EXPECT().Exec(mock.Anything, "pcoder-a-b",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "one\ntwo\nthree\n"}, nil)
	del, _ := butlerToolByName(t, d, butlerToolDeleteProject)
	raw, err := del(context.Background(), `{"project":"a/b","scope":"container"}`)
	if err != nil {
		t.Fatalf("propose delete: %v", err)
	}
	var prop struct {
		ConfirmID   string `json:"confirmId"`
		BlastRadius string `json:"blastRadius"`
	}
	if err := json.Unmarshal([]byte(raw), &prop); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prop.BlastRadius, "3 sessions") || !strings.Contains(prop.BlastRadius, "repo volume stays") {
		t.Fatalf("blast radius = %q, want sessions + surviving volume", prop.BlastRadius)
	}

	// Destructive tooling needs the explicit id: a wrong one 404s.
	rec := authedPost(t, h, cookie, "/api/butler/confirms/deadbeefdeadbeefdeadbeef/apply", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown confirm apply: got %d %q, want 404", rec.Code, rec.Body.String())
	}

	// Masked env value: apply carries it, nothing echoes it.
	env, _ := butlerToolByName(t, d, butlerToolProposeEnvFix)
	envRaw, err := env(context.Background(), `{"name":"SMTP_PASSWORD"}`)
	if err != nil {
		t.Fatalf("propose env fix: %v", err)
	}
	var envProp struct {
		ConfirmID string `json:"confirmId"`
	}
	if err := json.Unmarshal([]byte(envRaw), &envProp); err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-masked-value"
	rec = authedPost(t, h, cookie, "/api/butler/confirms/"+envProp.ConfirmID+"/apply", `{"value":"`+secret+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("env apply: got %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("apply response echoes the masked value: %q", rec.Body.String())
	}
	evs, err := d.Events.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		rawEv, _ := json.Marshal(e)
		if strings.Contains(string(rawEv), secret) {
			t.Fatalf("event log holds the masked value: %s", rawEv)
		}
	}

	// One-line model diff: the switch card shows old -> new.
	switchTool, _ := butlerToolByName(t, d, butlerToolSwitchModel)
	swRaw, err := switchTool(context.Background(), `{"harness":"fake","model":"gpt-4o"}`)
	if err != nil {
		t.Fatalf("propose switch: %v", err)
	}
	if !strings.Contains(swRaw, "gpt-4o") {
		t.Fatalf("switch proposal = %s, want the one-line diff", swRaw)
	}
}

// The fanout example proposes first: "update opencode everywhere" runs
// nowhere until Confirm.
func TestButlerFanoutProposesOnly(t *testing.T) {
	d, _, _, _ := newSessionDeps(t)
	fanout, _ := butlerToolByName(t, d, butlerToolFanoutExec)
	raw, err := fanout(context.Background(), `{"projects":["a/b"],"command":"npm i -g opencode-ai@latest"}`)
	if err != nil {
		t.Fatalf("propose fanout: %v", err)
	}
	if !strings.Contains(raw, "needsConfirm") {
		t.Fatalf("fanout = %s, want a confirm card, no execution", raw)
	}
	if n := d.Butler.ApprovalCount(); n != 1 {
		t.Fatalf("pendings = %d, want 1 (nothing ran)", n)
	}
}

// Closing a preview that is already stopped succeeds: proposals go stale
// (model proposes close on a stopped preview, user confirms later), and
// the end state is what was asked for — not a 502 "preview worker not
// found".
func TestButlerPreviewCloseIdempotent(t *testing.T) {
	d, _, _, _ := newSessionDeps(t)
	d.Preview = preview.NewManager(previewTestFactory{ep: privatePreviewEndpoint})
	var def *butlerWriteDef
	for i := range butlerWriteTable {
		if butlerWriteTable[i].name == butlerToolPreviewClose {
			def = &butlerWriteTable[i]
			break
		}
	}
	if def == nil {
		t.Fatal("preview_close missing from write table")
	}
	out, err := def.exec(context.Background(), d, map[string]any{"project": "a/b"}, "")
	if err != nil {
		t.Fatalf("close stopped preview: %v, want success", err)
	}
	if !strings.Contains(out, "already stopped") {
		t.Fatalf("close stopped preview = %q, want already-stopped", out)
	}
}

// Discarding cards resolves just that card: no closure turn, no 502, no
// FE/backend desync. Discarding the last one simply unblocks the thread.
func TestButlerDiscardSiblingPending(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{
				toolCall("c1", butlerToolPreviewClose, `{"project":"a/b"}`),
				toolCall("c2", butlerToolPreviewStart, `{"project":"a/b","port":3000}`),
			})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "Two cards for you.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := butlerPost(t, h, cookie, `{"prompt":"toggle the preview"}`, http.StatusOK)
	last := finalBody(t, rec)
	tid, _ := last["threadId"].(string)
	th0, err := d.Butler.Get(butlerScope, tid)
	if err != nil || len(th0.Approvals) != 2 {
		t.Fatalf("approvals = %+v, want 2 cards on the thread", th0.Approvals)
	}
	idOf := func(i int) string { return th0.Approvals[i].ID }
	// First discard: ok, no closure turn, thread still blocked.
	d1 := authedPost(t, h, cookie, "/api/butler/confirms/"+idOf(0)+"/discard", `{}`)
	if d1.Code != http.StatusOK {
		t.Fatalf("discard sibling: got %d %q, want 200", d1.Code, d1.Body.String())
	}
	var d1body map[string]any
	if err := json.Unmarshal(d1.Body.Bytes(), &d1body); err != nil || d1body["ok"] != true {
		t.Fatalf("discard sibling = %q, want ok", d1.Body.String())
	}
	if _, has := d1body["result"]; has {
		t.Fatalf("discard sibling must not run a closure turn: %q", d1.Body.String())
	}
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil || len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, err=%v — no closure turn while a sibling pends", th.Turns, err)
	}
	if blocked := authedPost(t, h, cookie, "/api/butler/turn", `{"prompt":"x","threadId":"`+tid+`"}`); blocked.Code != http.StatusConflict {
		t.Fatalf("follow-up with sibling pending: got %d, want 409", blocked.Code)
	}
	// Last discard: ok, no closure turn either, thread unblocks.
	d2 := authedPost(t, h, cookie, "/api/butler/confirms/"+idOf(1)+"/discard", `{}`)
	if d2.Code != http.StatusOK {
		t.Fatalf("discard last: got %d %q, want 200", d2.Code, d2.Body.String())
	}
	var d2body map[string]any
	if err := json.Unmarshal(d2.Body.Bytes(), &d2body); err != nil || d2body["ok"] != true {
		t.Fatalf("discard last = %q, want ok", d2.Body.String())
	}
	if _, has := d2body["result"]; has {
		t.Fatalf("discard last must not run a closure turn: %q", d2.Body.String())
	}
	th, err = d.Butler.Get(butlerScope, tid)
	if err != nil || len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, err=%v, want still 1 turn", th.Turns, err)
	}
}
