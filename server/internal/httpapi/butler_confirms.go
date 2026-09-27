// Butler confirm cards: writes never run in the turn loop. A write tool
// call only proposes: it stores the real work and returns a confirm id.
// The UI renders the card; Confirm/Discard hit the endpoints below, which
// run or drop the stored work. The secret value (env fix) travels only in
// the apply body and is never logged or echoed.
package httpapi

import (
	"net/http"
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

// butlerPropose stores the approval on the owning thread and returns its
// confirm id. created collects the public card for the turn response.
func butlerPropose(st *butlerthreads.Store, threadID, tool, args, summary, blast string, created *[]butlerCard) (string, error) {
	id := butlerthreads.MintID()
	card := butlerCard{ID: id, Tool: tool, Summary: summary, BlastRadius: blast}
	err := st.AddApproval(threadID, butlerthreads.Approval{ID: id, Tool: tool, Args: []byte(args), Summary: summary, BlastRadius: blast, CreatedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	if created != nil {
		*created = append(*created, card)
	}
	return id, nil
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
		// Hold the same slot as turns for the entire apply, so a turn cannot
		// start between the busy check and the mutation.
		if !butlerBusy.CompareAndSwap(false, true) {
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		defer butlerBusy.Store(false)
		if d.Butler == nil {
			writeErr(w, http.StatusNotFound, "unknown confirm — propose it again from chat")
			return
		}
		a, threadID, err := d.Butler.FindApproval(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "unknown confirm — propose it again from chat")
			return
		}
		var def *butlerWriteDef
		for i := range butlerWriteTable {
			if butlerWriteTable[i].name == a.Tool {
				def = &butlerWriteTable[i]
				break
			}
		}
		if def == nil {
			writeErr(w, http.StatusBadRequest, "unknown confirm tool")
			return
		}
		// The secret never enters obs/events: only the tool name is logged.
		obs.Info(r.Context(), obs.ButlerTurn, "butler confirm apply: "+a.Tool, map[string]any{"tool": a.Tool})
		out, err := def.exec(r.Context(), d, butlerArgs(string(a.Args)), body.Value)
		if err != nil {
			obsFail(r, obs.ButlerTurn, "butler apply failed", err, map[string]any{"tool": a.Tool})
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_ = d.Butler.DeleteApproval(threadID, id)
		_, _ = d.Events.Append("butler.apply", map[string]any{"tool": a.Tool})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tool": a.Tool, "result": out})
	}
}

func handleButlerConfirmDiscard(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if d.Butler == nil {
			writeErr(w, http.StatusNotFound, "unknown confirm — propose it again from chat")
			return
		}
		a, threadID, err := d.Butler.FindApproval(id)
		if err != nil || d.Butler.DeleteApproval(threadID, a.ID) != nil {
			writeErr(w, http.StatusNotFound, "unknown confirm — propose it again from chat")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
