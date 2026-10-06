//go:build integration

// Butler tool tests (faithful): one folder per tier.
//
//	testdata/butler/tier-N/
//	  state.json — the tier's state.json, realistic and literal: user,
//	    serverKey, smtp, harness registry, per-tier projects (keyed by
//	    the deterministic itest-tier-N/repo id) and ai_models.
//	  config.json — {"openedTerminalNames": [{"project", "name"}]}: the
//	    live tmux terminals the harness opens after boot (same call as
//	    the UI's New Session dialog). State only records sessions;
//	    this list is what makes rows pin alive:true.
//	  suite.json — [{"tool","args","want"}] for that tier's tools, keyed by
//	    the tool const name (list_projects, stop, ...).
//	    "__CONFIRM_ID__" stands for the random confirm id.
//	    Rows with "apply":true execute the proposal for real: "want"
//	    pins the apply result and "wantState" pins state.json sections
//	    after the write (declare keys with null, UPDATE_GOLDEN fills,
//	    review the diff). Mutating applies go last — later rows observe
//	    their effects. Skipped applies: preview_* (no sidecar wired),
//	    install_harness (real npm), create_project (real clone),
//	    git pull/push (daemon released after boot).
//
// One container per tier, reused across its suite entries. Each entry is
// one LLM-level tool call: seed -> Run(argsJSON) -> golden output.
//
// Run: go test -tags=integration -count=1 ./internal/httpapi/ -run TestButlerTools
// Refresh: UPDATE_GOLDEN=1 go test -tags=integration -count=1 ./internal/httpapi/ -run TestButlerTools
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pcoder/internal/auth"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
	"pcoder/internal/testutil"
	"pcoder/internal/threads"
)

type toolCase struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	// Apply executes the proposal for real after proposing: result goes
	// to Want, state sections to WantState. Reads and propose-only rows
	// leave both unset-style (Want holds tool output, no WantState).
	Apply     bool           `json:"apply,omitempty"`
	Want      any            `json:"want"`
	WantState map[string]any `json:"wantState,omitempty"` // section -> expected doc section
	Secret    string         `json:"secret,omitempty"`    // masked apply value; travels only in the apply call, never chat
}

// openTerminal is one entry of config.json: the project the live tmux
// terminal lives in plus its session name. Opening uses the same call
// the UI's New Session dialog makes — simulated user entry, not boot.
type openTerminal struct {
	Project string `json:"project"`
	Name    string `json:"name"`
}

// bootTier boots one tier folder exactly like prod and returns deps bound
// to its live state: seed copied to a fresh data dir, services wired over
// the real engine, the same BringAllUp main runs before serving, then the
// config.json terminals opened. Everything registers on the parent t: a
// subtest-scoped cleanup would drop shared state early.
func bootTier(t *testing.T, tierName string) Deps {
	t.Helper()
	stateRaw := mustRead(t, tierName, "state.json")
	daemon := serveFixtureIfSeeded(t, tierName, stateRaw)
	d, h, cookie := wireDeps(t, stateRaw)
	bringUp(t, tierName, d, h, cookie, stateRaw, daemon)
	openTerminals(t, tierName, h, cookie, mustReadTerminals(t, tierName))
	return d
}

// serveFixtureIfSeeded starts the git daemon when the seed names projects.
// The owner carries the tier number, so the repo URL is hardcoded in
// state.json. Nil for project-less tiers.
func serveFixtureIfSeeded(t *testing.T, tierName string, stateRaw []byte) *testutil.GitDaemonFixture {
	t.Helper()
	if !hasProjects(stateRaw) {
		return nil
	}
	return testutil.NewFixedGitDaemonFixture(t, "itest-"+tierName)
}

// hasProjects reports whether the seed names any projects.
func hasProjects(stateRaw []byte) bool {
	var probe struct {
		Projects map[string]any `json:"projects"`
	}
	_ = json.Unmarshal(stateRaw, &probe)
	return len(probe.Projects) > 0
}

// wireDeps copies the seed to a fresh data dir and wires live services
// over the real engine, mirroring main. Returns deps and an authed handler.
func wireDeps(t *testing.T, stateRaw []byte) (Deps, http.Handler, *http.Cookie) {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "state.json"), stateRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	lc := testutil.NewLifecycle(t)
	lc.EnsureNetwork(t)
	ev, err := events.Open(filepath.Join(dataDir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ev.Close() })
	dkr := lc.Docker()
	st, err := state.Open(dataDir, state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	var pinOut bytes.Buffer
	authSvc := auth.New("me@example.com", []byte(testSecret), auth.ConsoleMailer{Out: &pinOut})
	sshKeyStore := sshkeys.New(st)
	svc := project.NewService(project.Open(st), dkr)
	svc.SetSSHKeys(sshKeyStore)
	svc.SetAllowAnyRepo(true)
	d := Deps{
		Events: ev, Version: "itest", Auth: authSvc, Projects: svc,
		Obs: installObs(t, dataDir, ev), Docker: dkr, Sessions: session.New(dkr),
		Harnesses: harness.New(st), SSHKeys: sshKeyStore, State: st,
		Butler:   threads.New(t.TempDir(), true),
		Codemaps: threads.New(filepath.Join(dataDir, "codemaps"), false),
	}
	h := New(d)
	return d, h, login(t, h, &pinOut)
}

// bringUp runs the same boot pass as main and blocks until the seeded
// project is running. Empty tiers boot to an empty engine: zero projects
// is a no-op, and butler questions against it pin real behavior.
func bringUp(t *testing.T, tierName string, d Deps, h http.Handler, cookie *http.Cookie, stateRaw []byte, daemon *testutil.GitDaemonFixture) {
	t.Helper()
	if !hasProjects(stateRaw) {
		if err := d.Projects.BringAllUp(context.Background()); err != nil {
			t.Fatalf("tier %s: boot: %v", tierName, err)
		}
		return
	}
	id := "itest-" + tierName + "/repo" // matches the seed's hardcoded project key
	dropLeftovers(d, id)
	if err := d.Projects.BringAllUp(context.Background()); err != nil {
		t.Fatalf("tier %s: boot recovery: %v", tierName, err)
	}
	waitForStatus(t, h, cookie, id, "running")
	if daemon != nil {
		daemon.Close() // clones only happen at boot: frees the fixed port for the next tier
	}
	deleteTestProject(t, h, cookie, id) // scope=all cleanup when the test finishes
}

// dropLeftovers removes container + volumes orphaned by killed runs
// (cleanup never ran). Docker-level only: the API delete would also drop
// the seeded project record that recovery needs.
func dropLeftovers(d Deps, id string) {
	ctx := context.Background()
	ctr := project.ContainerName(id)
	_ = d.Docker.Remove(ctx, ctr, true)
	_ = d.Docker.RemoveVolume(ctx, ctr+"-repo")
	_ = d.Docker.RemoveVolume(ctx, ctr+"-home")
}

// openTerminals performs the config.json entries: one session create per
// terminal. Simulated user entry after boot, not boot itself.
func openTerminals(t *testing.T, tierName string, h http.Handler, cookie *http.Cookie, terms []openTerminal) {
	t.Helper()
	for _, term := range terms {
		body := fmt.Sprintf(`{"name":%q,"create":true}`, term.Name)
		code, resp := doJSON(t, h, cookie, http.MethodPost, projectPath(term.Project, "/sessions"), body)
		if code != http.StatusCreated {
			t.Fatalf("tier %s: open terminal %q in %q: %d %v", tierName, term.Name, term.Project, code, resp)
		}
	}
}

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

// normalizeOutput swaps volatile values for stable placeholders so
// suite.json stays readable and rerunnable.
func normalizeOutput(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return mintedIDInTextRe.ReplaceAllString(strings.TrimSpace(raw), "__ID__")
	}
	scrubOutput(v)
	normVolatileState(v)
	return marshalNoEscape(v)
}

func marshalNoEscape(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// mintedIDRe matches state.MintID output (8 bytes as 16 hex chars).
// Golden files pin "__ID__" instead so minted ids stay deterministic.
var mintedIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
var mintedIDInTextRe = regexp.MustCompile(`\b[0-9a-f]{16}\b`)

// normVolatileState swaps minted entry ids for "__ID__" in place so
// golden files stay deterministic. Only the "id" key qualifies, so fixed
// seed ids ("m1") and hashes never match. Fixture API keys stay literal:
// the seeds already carry them, and visible values make "untouched"
// assertions readable.
func normVolatileState(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			if k == "id" {
				if s, ok := t[k].(string); ok && mintedIDRe.MatchString(s) {
					t[k] = "__ID__"
					continue
				}
			}
			normVolatileState(t[k])
		}
	case []any:
		for _, e := range t {
			normVolatileState(e)
		}
	}
}

func scrubOutput(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			switch k {
			case "confirmId":
				t[k] = "__CONFIRM_ID__"
			case "uptimeSeconds", "diskFreeBytes":
				t[k] = 0 // volatile per run: pin structure, not the number
			case "time":
				t[k] = "__TIME__" // events_tail times are wall-clock: pin types + order, not stamps
			default:
				scrubOutput(t[k])
			}
		}
	case []any:
		for _, e := range t {
			scrubOutput(e)
		}
	}
}

func canonicalJSON(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return strings.TrimSpace(string(raw))
	}
	return marshalNoEscape(v)
}

func isJSONObject(raw string) bool {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
		return false
	}
	var v any
	return json.Unmarshal([]byte(raw), &v) == nil
}

func prettyJSON(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

// TestButlerTools walks testdata/butler/tier-N: each folder boots one
// live container from its state.json, then runs its suite.json entries
// against it. Each entry is named by its tool const. Colocated so a dev
// verifies seed + calls + outputs together.
func TestButlerTools(t *testing.T) {
	origProbe := probeGitHubSSH
	probeGitHubSSH = func(ctx context.Context, kp sshkeys.Key) (string, error) { return "octocat", nil }
	t.Cleanup(func() { probeGitHubSSH = origProbe })
	root := filepath.Join("testdata", "butler")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("testdata/butler: %v", err)
	}
	var tiers []string
	for _, e := range entries {
		if e.IsDir() {
			tiers = append(tiers, e.Name())
		}
	}
	if len(tiers) == 0 {
		t.Fatal("testdata/butler has no tier folders")
	}
	update := os.Getenv("UPDATE_GOLDEN") != ""
	for _, tier := range tiers {
		dir := filepath.Join(root, tier)
		suite := mustReadSuite(t, tier)
		env := bootTier(t, tier)
		for i := range suite {
			c := suite[i]
			t.Run(tier+"/"+c.Tool, func(t *testing.T) {
				argsStr, got := runTool(t, env, c.Tool, c.Args)
				if c.Apply {
					got = applyProposal(t, env, c.Tool, argsStr, got, c.Secret)
				}
				got = normalizeOutput(got)
				if update {
					suite[i].Want = parseWant(got)
					refreshWantState(t, env, &suite[i])
					return
				}
				checkWant(t, dir, c, got)
				checkWantState(t, env, c)
			})
		}
		if update {
			mustWriteSuite(t, tier, suite)
		}
	}
}

// runTool runs one LLM-level tool call and returns the raw output.
// Applies parse the proposal; normalization happens after apply.
func runTool(t *testing.T, d Deps, tool string, args map[string]any) (string, string) {
	t.Helper()
	argsRaw, _ := json.Marshal(args)
	argsStr := string(argsRaw)
	if argsStr == "null" {
		argsStr = "{}"
	}
	run, _ := butlerToolByName(t, d, tool)
	got, err := run(context.Background(), argsStr)
	if err != nil {
		return argsStr, "ERROR: " + err.Error()
	}
	return argsStr, got
}

// applyProposal executes a stored proposal for real: finds the write-table
// entry, runs its exec with the masked secret, resolves the approval, and
// returns the result. Secret carries the masked apply value (create_ai_model
// persists it as the entry key); only tools declaring secrets read it.
func applyProposal(t *testing.T, d Deps, tool, argsStr, proposal, secret string) string {
	t.Helper()
	var card struct {
		ConfirmID string `json:"confirmId"`
	}
	if err := json.Unmarshal([]byte(proposal), &card); err != nil || card.ConfirmID == "" {
		t.Fatalf("tool %s: not a proposal, cannot apply: %s", tool, proposal)
	}
	a, threadID, err := d.Butler.FindApproval(card.ConfirmID)
	if err != nil {
		t.Fatalf("tool %s: approval missing: %v", tool, err)
	}
	for i := range butlerWriteTable {
		if butlerWriteTable[i].name != a.Tool {
			continue
		}
		out, execErr := butlerWriteTable[i].exec(context.Background(), d, butlerArgs(argsStr), secret)
		if execErr != nil {
			return "ERROR: " + execErr.Error()
		}
		if err := d.Butler.ResolveApproval(threadID, card.ConfirmID, threads.ApprovalApproved); err != nil {
			t.Fatalf("tool %s: resolve: %v", tool, err)
		}
		return out
	}
	t.Fatalf("tool %s: no write-table entry", tool)
	return ""
}

// parseWant converts fresh output to a storable golden: structured JSON
// stays structured, plain text (architecture doc) and errors stay strings.
func parseWant(got string) any {
	if strings.HasPrefix(got, "ERROR:") {
		return got
	}
	if isJSONObject(got) {
		var v any
		_ = json.Unmarshal([]byte(got), &v)
		return v
	}
	return got
}

// refreshWantState fills each declared WantState section from the live
// state file. Authors declare keys with null values; update fills them,
// and review of the diff is the actual assertion authoring.
func refreshWantState(t *testing.T, d Deps, c *toolCase) {
	t.Helper()
	if len(c.WantState) == 0 {
		return
	}
	doc := readStateDoc(t, d)
	for section := range c.WantState {
		normVolatileState(doc[section])
		c.WantState[section] = doc[section]
	}
}

// checkWantState fails unless each declared section matches the live
// state file exactly. Sections compare through canonical JSON so int
// and float encodings compare equal.
func checkWantState(t *testing.T, d Deps, c toolCase) {
	t.Helper()
	if len(c.WantState) == 0 {
		return
	}
	doc := readStateDoc(t, d)
	for section, expected := range c.WantState {
		normVolatileState(doc[section])
		wantRaw, _ := json.Marshal(expected)
		gotRaw, _ := json.Marshal(doc[section])
		if canonicalJSON(wantRaw) != canonicalJSON(gotRaw) {
			t.Fatalf("tool %s: state section %q diverged:\n--- got ---\n%s\n--- want ---\n%s",
				c.Tool, section, prettyJSON(string(gotRaw)), prettyJSON(string(wantRaw)))
		}
	}
}

func readStateDoc(t *testing.T, d Deps) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(d.State.Path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return doc
}
func checkWant(t *testing.T, dir string, c toolCase, got string) {
	t.Helper()
	if wantStr, ok := c.Want.(string); ok {
		if got != strings.TrimSpace(wantStr) {
			t.Fatalf("tool %s:\n--- %s/state.json ---\n%s\n--- got ---\n%s\n--- want ---\n%s",
				c.Tool, dir, readTierState(dir), got, strings.TrimSpace(wantStr))
		}
		return
	}
	wantRaw, _ := json.Marshal(c.Want)
	if want := canonicalJSON(wantRaw); normalizeOutput(want) != got {
		t.Fatalf("tool %s:\n--- %s/state.json ---\n%s\n--- got ---\n%s\n--- want ---\n%s",
			c.Tool, dir, readTierState(dir), prettyJSON(got), prettyJSON(want))
	}
}

func mustRead(t *testing.T, tier, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "butler", tier, name))
	if err != nil {
		t.Fatalf("%s/%s: %v", tier, name, err)
	}
	return raw
}

func mustReadTerminals(t *testing.T, tierName string) []openTerminal {
	t.Helper()
	var config struct {
		OpenedTerminalNames []openTerminal `json:"openedTerminalNames"`
	}
	if err := json.Unmarshal(mustRead(t, tierName, "config.json"), &config); err != nil {
		t.Fatalf("%s/config.json: %v", tierName, err)
	}
	return config.OpenedTerminalNames
}

func mustReadSuite(t *testing.T, tier string) []toolCase {
	t.Helper()
	var suite []toolCase
	if err := json.Unmarshal(mustRead(t, tier, "suite.json"), &suite); err != nil {
		t.Fatalf("%s/suite.json: %v", tier, err)
	}
	return suite
}

func mustWriteSuite(t *testing.T, tier string, suite []toolCase) {
	t.Helper()
	out, _ := json.MarshalIndent(suite, "", "  ")
	out = append(out, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "butler", tier, "suite.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTierState(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return "(missing state.json: " + err.Error() + ")"
	}
	return strings.TrimSpace(string(raw))
}
