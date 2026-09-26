package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com/v1":                  "https://api.openai.com/v1",
		"https://api.openai.com/v1/":                 "https://api.openai.com/v1",
		"https://api.openai.com/v1/chat/completions": "https://api.openai.com/v1",
		"  https://x.test/v1/  ":                     "https://x.test/v1",
		"http://localhost:8080":                      "http://localhost:8080",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigValid(t *testing.T) {
	if (Config{}).Valid() {
		t.Errorf("empty config must be invalid")
	}
	if (!Config{BaseURL: "http://x", APIKey: "k", Model: "m"}.Valid()) {
		t.Errorf("full config must be valid")
	}
	if (Config{BaseURL: "http://x", Model: "m"}).Valid() {
		t.Errorf("missing key must be invalid")
	}
}

func TestConnectionRequiresFields(t *testing.T) {
	if err := TestConnection(context.Background(), Config{}); err == nil {
		t.Fatalf("empty config must fail without a model call")
	}
}

func TestConnectionMapsWeakEndpoints(t *testing.T) {
	// A proxy that rejects tools must surface as a tools error, so the
	// settings form blames capability, not the key.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "this model does not support tools"},
		})
	}))
	defer srv.Close()
	err := TestConnection(context.Background(), Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("err = %v, want tools-support error", err)
	}
}

func TestConnectionNoRetry(t *testing.T) {
	// A 429 must surface at once: the SDK sleeps uncancellable backoffs
	// between attempts (honoring Retry-After), which once held a probe
	// for minutes past its ctx deadline on a rate-limited free tier.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "58")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limited"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": `{"ok":true}`}}},
		})
	}))
	defer srv.Close()
	start := time.Now()
	err := TestConnection(context.Background(), Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatalf("429 probe must fail, not retry into success")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 (no retry)", calls)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("probe took %v, want fast fail without Retry-After sleep", time.Since(start))
	}
}

func TestRunRejectsUnconfigured(t *testing.T) {
	if _, err := Run(context.Background(), Config{}, "sys", "hi", nil, nil, "s", nil, 2, nil, nil); err == nil {
		t.Fatalf("unconfigured run must fail")
	}
}

func TestRunReplaysToolSteps(t *testing.T) {
	// Fake model asserts the replayed history then answers directly.
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, &gotBody)
		msgs, _ := body["messages"].([]any)
		if len(msgs) < 5 {
			t.Errorf("messages = %d, want >= 5 (system+user+assistant_tools+tool+user)", len(msgs))
		}
		rawMsgs, _ := json.Marshal(msgs)
		s := string(rawMsgs)
		for _, want := range []string{`"role":"user"`, `"role":"assistant"`, `prior prompt`, `search_code`, `"role":"tool"`, `grep-output`, `prior answer`, `new q`} {
			if !strings.Contains(s, want) {
				t.Errorf("replayed request missing %q: %s", want, s)
			}
		}
		// Global call ids: assistant tool_calls id must match the tool reply.
		if !strings.Contains(s, "call_0") {
			t.Errorf("missing global call_0 id: %s", s)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "done"}}},
		})
	}))
	defer srv.Close()
	history := []map[string]any{
		{"role": "user", "content": "prior prompt"},
		{"role": "assistant", "content": "(calling tools)", "toolSteps": []any{
			map[string]any{"tool": "search_code", "args": `{"pattern":"x"}`, "output": "grep-output", "error": ""},
		}},
		{"role": "assistant", "content": "prior answer"},
	}
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "new q", history, nil, "s", nil, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	if gotBody == nil {
		t.Fatalf("model never called")
	}
}

func TestRunNoChoices(t *testing.T) {
	// Free-tier gateways answer HTTP 200 with choices:null and no SDK
	// error. The loop must surface the raw payload, not a bare string.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-test","object":"chat.completion","created":0,"model":"","choices":null}`))
	}))
	defer srv.Close()
	var traced []string
	_, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, nil, "s", nil, 4,
		func(ev TraceEvent) {
			if ev.Kind == "model_error" {
				traced = append(traced, ev.Err)
			}
		}, nil)
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("err = %v, want no-choices error", err)
	}
	if !strings.Contains(err.Error(), "choices") {
		t.Fatalf("err hides provider payload: %v", err)
	}
	// Two retry traces plus the final error trace.
	if len(traced) != 3 || !strings.Contains(traced[0], "retrying") || !strings.Contains(traced[2], "no choices") {
		t.Fatalf("model_error trace = %v, want 2 retries + no-choices entries", traced)
	}
}

func TestRunRetriesEmptyChoice(t *testing.T) {
	// Flaky gateway: first call nulls, retry succeeds.
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{"id":"gen-test","object":"chat.completion","created":0,"model":"","choices":null}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "recovered"}}},
		})
	}))
	defer srv.Close()
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, nil, "s", nil, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial + 1 retry)", calls)
	}
}

func TestRunFormatsWithSchema(t *testing.T) {
	// Loop calls carry no response_format (schema+tools breaks providers);
	// the tools-free format call enforces the schema exactly once.
	var bodies []map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		n := len(bodies)
		bodies = append(bodies, cp)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		content := `{"sections":[]}`
		if n == 0 {
			content = "final answer text"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content}}},
		})
	}))
	defer srv.Close()
	schema := map[string]any{"type": "object"}
	lineage := &Lineage{}
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, nil, "s", schema, 4, nil, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"sections":[]}` {
		t.Fatalf("out = %q, want formatted JSON", out)
	}
	if len(bodies) != 2 {
		t.Fatalf("calls = %d, want 2 (loop + format)", len(bodies))
	}
	if _, has := bodies[0]["response_format"]; has {
		t.Fatalf("loop call must not carry response_format")
	}
	if _, has := bodies[1]["response_format"]; !has {
		t.Fatalf("format call must carry response_format")
	}
	if tools, _ := bodies[1]["tools"].([]any); len(tools) != 0 {
		t.Fatalf("format call must not carry tools")
	}
	if lineage.InitialRequest == nil || lineage.PrunedTier1Data == nil {
		t.Fatalf("lineage missing tier boundaries: %+v", lineage)
	}
	if len(lineage.Events) < 4 {
		t.Fatalf("lineage events = %d, want request/response/format sequence", len(lineage.Events))
	}
}

func TestRunFinalAnswerAfterMaxSteps(t *testing.T) {
	// A model that only ever calls tools still yields an answer: after
	// maxSteps the loop nudges once, tools-free.
	var bodies []map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		bodies = append(bodies, cp)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		msgs, _ := body["messages"].([]any)
		last, _ := json.Marshal(msgs[len(msgs)-1])
		var resp string
		if strings.Contains(string(last), "no more tool calls") {
			resp = `{"id":"x","object":"chat.completion","created":1,"model":"fake","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"final from grounding"}}]}`
		} else {
			resp = `{"id":"x","object":"chat.completion","created":1,"model":"fake","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"ping","arguments":"{}"}}]}}]}`
		}
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()
	ping := Tool{Name: "ping", Description: "p", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ string) (string, error) { return "pong", nil }}
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, []Tool{ping}, "s", nil, 2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "final from grounding" {
		t.Fatalf("out = %q, want final from grounding", out)
	}
	if len(bodies) != 3 {
		t.Fatalf("calls = %d, want 3 (2 tool steps + final nudge)", len(bodies))
	}
	if tools, _ := bodies[2]["tools"].([]any); len(tools) != 0 {
		t.Fatalf("final nudge must not carry tools")
	}
}

func TestRunRetriesTransportError(t *testing.T) {
	// First call fails at transport level, retry succeeds.
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "recovered"}}},
		})
	}))
	defer srv.Close()
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, nil, "s", nil, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "recovered" {
		t.Fatalf("out = %q, want recovered", out)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestRunRepeatCallHint(t *testing.T) {
	// Same tool+args twice: the second tool message fed to the model
	// carries a repeat note, while the run still succeeds.
	var bodies []map[string]any
	var mu sync.Mutex
	toolResp := func(id string) map[string]any {
		return map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "tool_calls",
				"message": map[string]any{"role": "assistant", "content": "",
					"tool_calls": []map[string]any{{
						"id": id, "type": "function",
						"function": map[string]any{"name": "search", "arguments": `{"pattern":"x"}`},
					}}}}},
		}
	}
	doneResp := map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": "done"}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		n := len(bodies)
		bodies = append(bodies, cp)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n < 2 {
			_ = json.NewEncoder(w).Encode(toolResp("c1"))
		} else {
			_ = json.NewEncoder(w).Encode(doneResp)
		}
	}))
	defer srv.Close()
	search := Tool{Name: "search", Description: "s", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ string) (string, error) { return "hit", nil }}
	var doneOutputs []string
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, []Tool{search}, "s", nil, 4,
		func(ev TraceEvent) {
			if ev.Kind == "tool_done" {
				doneOutputs = append(doneOutputs, ev.Result)
			}
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want done", out)
	}
	// Persisted outputs stay raw (no hint).
	for _, o := range doneOutputs {
		if strings.Contains(o, "already ran") {
			t.Fatalf("persisted output polluted with hint: %q", o)
		}
	}
	// The model's third request carries the hint on the repeated result.
	raw, _ := json.Marshal(bodies[2])
	if !strings.Contains(string(raw), "already ran") {
		t.Fatalf("repeat hint missing from model context: %s", string(raw))
	}
}

func TestRunGroundingNudge(t *testing.T) {
	// Zero-round final answer with tools offered: one structural nudge,
	// then the post-nudge answer is accepted.
	var bodies []map[string]any
	var mu sync.Mutex
	toolResp := map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
		"choices": []map[string]any{{"index": 0, "finish_reason": "tool_calls",
			"message": map[string]any{"role": "assistant", "content": "",
				"tool_calls": []map[string]any{{
					"id": "c1", "type": "function",
					"function": map[string]any{"name": "search", "arguments": `{}`},
				}}}}},
	}
	textResp := func(s string) map[string]any {
		return map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": s}}},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		raw, _ := json.Marshal(body)
		var cp map[string]any
		_ = json.Unmarshal(raw, &cp)
		n := len(bodies)
		bodies = append(bodies, cp)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 0 {
			_ = json.NewEncoder(w).Encode(textResp("unguarded answer"))
		} else if n == 1 {
			_ = json.NewEncoder(w).Encode(toolResp)
		} else {
			_ = json.NewEncoder(w).Encode(textResp("grounded answer"))
		}
	}))
	defer srv.Close()
	search := Tool{Name: "search", Description: "s", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ string) (string, error) { return "hit", nil }}
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, []Tool{search}, "s", nil, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "grounded answer" {
		t.Fatalf("out = %q, want grounded answer", out)
	}
	if len(bodies) != 3 {
		t.Fatalf("calls = %d, want 3 (final, nudge-round, final)", len(bodies))
	}
	raw, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(raw), "ground your answer") {
		t.Fatalf("nudge missing from second request: %s", string(raw))
	}
}

func TestRunGroundingNudgeSkipsEscape(t *testing.T) {
	// A zero-round answer that already declares the question
	// repo-irrelevant is accepted without a nudge.
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "fake",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": `{"sections":[{"title":"Not a codebase question","summary":"x","refs":[]}]}`}}},
		})
	}))
	defer srv.Close()
	search := Tool{Name: "search", Description: "s", Schema: map[string]any{"type": "object"},
		Run: func(_ context.Context, _ string) (string, error) { return "hit", nil }}
	out, err := Run(context.Background(),
		Config{BaseURL: srv.URL, APIKey: "k", Model: "m"},
		"sys", "hi", nil, []Tool{search}, "s", nil, 4, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Not a codebase question") {
		t.Fatalf("out = %q", out)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no nudge for escape hatch)", calls)
	}
}
