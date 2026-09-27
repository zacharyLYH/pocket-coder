// Butler architecture notes, served verbatim by the architecture tool.
// One copy on purpose: a docs/ mirror would drift, and the server binary
// cannot embed root docs/ (docker builds with context ./server).
package httpapi

import (
	"sort"
	"strings"
)

const butlerArchDoc = `# Butler architecture

How Pocket Coder is built and why: read this when a question needs design context — what survives a stop or delete, where state lives, why a tool behaves the way it does. Facts here outrank guesses.

## State

Desired state lives in state.json (projects, harnesses, keys, model and git lists, shortcuts). Live state lives in Docker (containers, volumes) and tmux (sessions). The server reconciles live toward desired; never confuse the two.
Example: a missing container with intact volumes is recreated from those volumes — code and harness binaries intact, no re-clone.

## Projects

One project is one repo checkout in one container (pcoder-owner-repo), with two volumes: repo (code) and home (harness binaries, CLI configs). The id is owner/repo and doubles as the display name.
Example: deleting with scope=container removes the container and the home volume; the repo volume stays, so recreating keeps the code.

## Sessions

Sessions are tmux sessions inside the project container: plain shells or harness launches. state.json records which harness each name runs; a recorded name outliving its container relaunches on entry. Killing a gone session succeeds (idempotent).
Example: after a container rebuild, a recorded opencode-1 relaunches as opencode instead of 404ing.

## Previews

Each project gets a preview sidecar: a real Chromium sharing the project's network namespace, token-gated through the server. Slots read stopped (no worker), degraded (worker unreachable), or ready (browser answers). Listening ports are probed with ss, minus the sidecar's own ports.
Example: a blank preview with status degraded means the sidecar lives but the app port answers nothing — check preview_state ports first.

## Harnesses

Harnesses are CLI plugins in one registry (builtins ship seeded and editable). Installing is explicit per project from the home page; launching an uninstalled harness fails and says to install it. A --version probe rejects non-CLIs such as IDEs and GUIs.
Example: install_harness into picked projects downloads once per container; later sessions just launch.

## Butler

Only Butler writes, and only through confirm cards: every write proposes a card stating the blast radius; Confirm applies, Discard drops. Nothing runs in the turn itself. Secret values never enter chat: keys and tokens render as booleans, env values arrive through a masked field.
Example: "Delete project api? This removes the container and 3 sessions. Volumes stay." — Confirm deletes, Discard is silent.
Butler chats persist per thread under data/butler/<threadID>/ (manifest.json plus N.json turns), one global list across projects. events.log is the separate audit trail: types and times, never bodies.
Example: "brief me" chains list_projects plus events_tail, then writes one summary and stops.`

// butlerArchSections splits the doc on ## headers. Text before the first
// header reads as "overview".
func butlerArchSections() map[string]string {
	out := map[string]string{}
	chunks := strings.Split(butlerArchDoc, "\n## ")
	out["overview"] = strings.TrimSpace(chunks[0])
	for _, c := range chunks[1:] {
		name := strings.ToLower(strings.Fields(c)[0])
		out[name] = strings.TrimSpace("## " + c)
	}
	return out
}

// butlerArchSectionNames lists section keys, sorted.
func butlerArchSectionNames() []string {
	sections := butlerArchSections()
	names := make([]string, 0, len(sections))
	for n := range sections {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
