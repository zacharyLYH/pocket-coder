package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// scriptedCaller scripts cdpCaller results in call order: results[i] is the
// error for the i-th call (nil = success). It records method/type pairs so
// tests can assert press-before-release ordering and best-effort retries.
type scriptedCaller struct {
	results []error
	calls   []string
}

func (f *scriptedCaller) call(_ context.Context, method string, params any) (json.RawMessage, error) {
	typ := ""
	if m, ok := params.(map[string]any); ok {
		typ, _ = m["type"].(string)
	}
	f.calls = append(f.calls, method+"/"+typ)
	if len(f.results) == 0 {
		return json.RawMessage(`{}`), nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{}`), nil
}

var errBoom = errors.New("boom")

func TestClickAtSuccess(t *testing.T) {
	f := &scriptedCaller{}
	if err := clickAt(context.Background(), f, 10, 20); err != nil {
		t.Fatalf("clickAt = %v, want nil", err)
	}
	want := []string{
		"Input.dispatchMouseEvent/mousePressed",
		"Input.dispatchMouseEvent/mouseReleased",
	}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}

func TestClickAtPressFailureSkipsRelease(t *testing.T) {
	f := &scriptedCaller{results: []error{errBoom}}
	err := clickAt(context.Background(), f, 10, 20)
	if err == nil || !strings.Contains(err.Error(), "press") {
		t.Fatalf("err = %v, want press error", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want exactly one (no release after press failure)", f.calls)
	}
}

// The residual the B1 fix surfaced: press lands, socket dies, release fails.
// The handler must fail loudly (not ok:true) and leave evidence that the
// press may have landed, after one best-effort release to unstick the button.
func TestClickAtReleaseFailureIsLoudAndRetriesRelease(t *testing.T) {
	f := &scriptedCaller{results: []error{nil, errBoom}}
	err := clickAt(context.Background(), f, 10, 20)
	if err == nil || !strings.Contains(err.Error(), "release failed") {
		t.Fatalf("err = %v, want press-succeeded-but-release-failed error", err)
	}
	want := []string{
		"Input.dispatchMouseEvent/mousePressed",
		"Input.dispatchMouseEvent/mouseReleased",
		"Input.dispatchMouseEvent/mouseReleased", // best-effort unstick
	}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}

func TestTypeTextKeyUpFailureIsLoudAndRetriesKeyUp(t *testing.T) {
	f := &scriptedCaller{results: []error{nil, errBoom}}
	err := typeText(context.Background(), f, "a")
	if err == nil || !strings.Contains(err.Error(), "keyUp failed") {
		t.Fatalf("err = %v, want keyDown-succeeded-but-keyUp-failed error", err)
	}
	want := []string{
		"Input.dispatchKeyEvent/keyDown",
		"Input.dispatchKeyEvent/keyUp",
		"Input.dispatchKeyEvent/keyUp", // best-effort unstick
	}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
}

func TestTypeTextKeyDownFailureSkipsKeyUp(t *testing.T) {
	f := &scriptedCaller{results: []error{errBoom}}
	if err := typeText(context.Background(), f, "a"); err == nil {
		t.Fatal("want keyDown error, got nil")
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want exactly one (no keyUp after keyDown failure)", f.calls)
	}
}
