# Spec: Asynchronous Butler Turns

**Goal**: Make Butler chat feel like a "normal" chatbot by displaying the user's message immediately and showing a loading indicator while the AI works in the background.

## Current Architecture (Synchronous)
1.  **Frontend**: `sendTurn` calls `POST /api/butler/turn` and waits.
2.  **Server**: `handleButlerTurn` takes a global lock, reserves a turn (writes `N.json`), and blocks while running the AI agent loop.
3.  **Server**: Returns final answer in the POST response.
4.  **Frontend**: `inFlight` state clears, UI refreshes and shows the bubble.

## New Architecture (Asynchronous)
1.  **Frontend**: `sendTurn` calls `POST /api/butler/turn`.
2.  **Server**: `handleButlerTurn` takes the global lock and calls `reserveTurn` (persists `N.json` immediately).
3.  **Server**: Starts a background goroutine to execute the AI agent and **returns `200 OK` immediately** with `threadId`.
4.  **Frontend**: `sendTurn` finishes quickly. The `useThread` hook sees the thread status as `running` and starts short-polling `GET /api/butler/threads/:id`.
5.  **Server (Background)**: AI agent finishes, updates `N.json` via `CompleteTurn`, and releases the global lock.
6.  **Frontend (Poll)**: Next poll sees the completed turn. `running` status clears, polling stops.

---

## Implementation Details

### 1. Server: Asynchronous execution in `server/internal/httpapi/butler.go`
*   Refactored `handleButlerTurn` and `handleButlerRetry` to spawn a goroutine for the agent work.
*   Used `context.WithoutCancel` (via `detached(r)`) for the background agent loop.
*   Moved `butlerRuns.done` into the goroutine to ensure the global lock is held for the duration of the run.
*   The POST response now returns `threadId`, `turnId`, and `time` immediately after reservation.

### 2. Frontend: Remove hacky optimistic echo in `web/src/components/ButlerSheet.tsx`
*   Removed `pendingPrompt` and its `useEffect`.
*   Rely on the fact that the server now persists the turn (including the prompt) immediately before returning from the POST.
*   The `useThread` hook's `usePollWhileUnsettled` logic naturally handles the transition from "running" to "ready".

### 3. Thread Status
*   The `butlerStatus` function in `server/internal/httpapi/thread_status.go` correctly reflects the `running` state by checking the global `butlerRuns` tracker.
*   This ensures that any client (including those that reload the page) sees the "Butler is typing" state while the background goroutine is active.
