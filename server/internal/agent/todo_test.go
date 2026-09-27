package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTodoToolReplacesAndValidatesLineage(t *testing.T) {
	lin := &Lineage{}
	tool := TodoTool(lin)
	got, err := tool.Run(context.Background(), `{"todos":[{"content":"Inspect the flow","status":"in_progress","priority":"high"},{"content":"Run tests","status":"pending","priority":"medium"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(lin.Todos) != 2 || lin.Todos[0].Status != "in_progress" {
		t.Fatalf("lineage todos = %+v", lin.Todos)
	}
	var encoded []Todo
	if err := json.Unmarshal([]byte(got), &encoded); err != nil || len(encoded) != 2 {
		t.Fatalf("tool result = %q, err=%v", got, err)
	}

	for _, raw := range []string{
		`{"todos":[{"content":"a","status":"in_progress","priority":"low"},{"content":"b","status":"in_progress","priority":"low"}]}`,
		`{"todos":[{"content":"a","status":"blocked","priority":"low"}]}`,
	} {
		if _, err := tool.Run(context.Background(), raw); err == nil {
			t.Errorf("%s must fail", raw)
		}
	}
	if _, err := tool.Run(context.Background(), `{"todos":[{"content":" ","status":"pending","priority":"low"}]}`); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("blank content error = %v", err)
	}
}

func TestLineageTodosJSONField(t *testing.T) {
	raw, err := json.Marshal(Lineage{Todos: []Todo{{Content: "x", Status: "completed", Priority: "low"}}})
	if err != nil || !strings.Contains(string(raw), `"todos"`) {
		t.Fatalf("lineage = %s, err=%v", raw, err)
	}
}
