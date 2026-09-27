// Butler confirm cards: writes never run in the turn loop. A write tool
// call only proposes: it stores the real work and returns a confirm id.
// The UI renders the card; Confirm/Discard hit the endpoints below, which
// run or drop the stored work. The secret value (env fix) travels only in
// the apply body and is never logged or echoed.
package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"pcoder/internal/butlerthreads"
	"pcoder/internal/obs"
)

// butlerCard is the public confirm card: what the UI renders.
type butlerCard struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Summary     string `json:"summary"`
	BlastRadius string `json:"blastRadius"`
}

// butlerPending is the stored work behind a card. Run takes the masked
// value from the apply body (empty for every tool but propose_env_fix).
type butlerPending struct {
	card butlerCard
	args string
	run  func(ctx context.Context, secret string) (string, error)
	time time.Time
}

var butlerPendings = struct {
	sync.Mutex
	m map[string]butlerPending
}{m: map[string]butlerPending{}}

// butlerPropose stores run and returns its confirm id. created collects
// the public card when non-nil (the turn's confirms array).
func butlerPropose(tool, args, summary, blast string, run func(ctx context.Context, secret string) (string, error), created *[]butlerCard) string {
	id := butlerthreads.MintID()
	card := butlerCard{ID: id, Tool: tool, Summary: summary, BlastRadius: blast}
	butlerPendings.Lock()
	butlerPendings.m[id] = butlerPending{card: card, args: args, run: run, time: time.Now().UTC()}
	butlerPendings.Unlock()
	if created != nil {
		*created = append(*created, card)
	}
	return id
}

// butlerTake removes and returns a pending. False when unknown (or already
// applied/discarded): destructive tools need this explicit id, so a wrong
// id never runs anything.
func butlerTake(id string) (butlerPending, bool) {
	butlerPendings.Lock()
	defer butlerPendings.Unlock()
	p, ok := butlerPendings.m[id]
	if ok {
		delete(butlerPendings.m, id)
	}
	return p, ok
}

func handleButlerConfirmApply(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Value string `json:"value"` // masked field (env fix only); never logged
		}
		if !decodeBody(w, r, &body, true) {
			return
		}
		p, ok := butlerTake(id)
		if !ok {
			writeErr(w, http.StatusNotFound, "unknown confirm — it may have expired")
			return
		}
		// The secret never enters obs/events: only the tool name is logged.
		obs.Info(r.Context(), obs.ButlerTurn, "butler confirm apply: "+p.card.Tool, map[string]any{"tool": p.card.Tool})
		out, err := p.run(r.Context(), body.Value)
		if err != nil {
			obsFail(r, obs.ButlerTurn, "butler apply failed", err, map[string]any{"tool": p.card.Tool})
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, _ = d.Events.Append("butler.apply", map[string]any{"tool": p.card.Tool})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": p.card.Tool, "result": out})
	}
}

func handleButlerConfirmDiscard(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := butlerTake(id); !ok {
			writeErr(w, http.StatusNotFound, "unknown confirm — it may have expired")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// butlerPendingCount is test-only: how many unapplied cards exist.
func butlerPendingCount() int {
	butlerPendings.Lock()
	defer butlerPendings.Unlock()
	return len(butlerPendings.m)
}

// butlerResetPendings is test-only.
func butlerResetPendings() {
	butlerPendings.Lock()
	defer butlerPendings.Unlock()
	butlerPendings.m = map[string]butlerPending{}
}
