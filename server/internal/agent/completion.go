package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/openai/openai-go/v2"
)

const (
	// stepTimeout bounds one model call. Reasoning tiers routinely take
	// 30-60s per call; the turn budget (handler ctx) bounds the whole run.
	stepTimeout = 90 * time.Second
	// stepAttempts bounds tries per call (initial + retries).
	stepAttempts = 3
	// retryBackoff spaces retries of a failed call.
	retryBackoff = 2 * time.Second
)

// Completion issues one chat completion, logging under the given
// request/response message names. It retries (up to maxAttempts total)
// when the provider errors at the transport level or answers HTTP 200
// with an empty payload (choices:null / empty message — verified
// intermittent on free reasoning tiers); where describes the call for
// retry traces ("step 3", "final-answer call", "format call"). It returns
// the final response and raw JSON; callers still validate content. A
// fatal context error returns ctx.Err().
func Completion(ctx context.Context, client openai.Client, params openai.ChatCompletionNewParams, onTrace func(TraceEvent), reqMsg, resMsg, where string, maxAttempts int) (*openai.ChatCompletion, []byte, error) {
	call := func() (*openai.ChatCompletion, []byte, error) {
		reqRaw, _ := json.Marshal(params)
		slog.Info(reqMsg, "payload", string(reqRaw))
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		res, err := client.Chat.Completions.New(stepCtx, params)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		resRaw, _ := json.Marshal(res)
		slog.Info(resMsg, "payload", string(resRaw))
		return res, resRaw, nil
	}
	describe := func(res *openai.ChatCompletion) string {
		if res == nil || len(res.Choices) == 0 {
			return "no choices"
		}
		if len(res.Choices[0].Message.ToolCalls) == 0 && strings.TrimSpace(res.Choices[0].Message.Content) == "" {
			return "empty answer"
		}
		return ""
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	res, resRaw, err := call()
	for attempt := 1; attempt < maxAttempts && (err != nil || describe(res) != ""); attempt++ {
		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			preview := strings.TrimSpace(string(resRaw))
			const max = 500
			if len(preview) > max {
				preview = preview[:max] + "…"
			}
			reason = "model returned " + describe(res) + " on " + where + " (response: " + preview + ")"
		}
		if onTrace != nil {
			onTrace(TraceEvent{Kind: "model_error", Err: reason + ", retrying"})
		}
		timer := time.NewTimer(retryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
		res, resRaw, err = call()
	}
	return res, resRaw, err
}
