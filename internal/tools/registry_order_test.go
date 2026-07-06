package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// stubTool 是只為測試排序用的最小工具。
type stubTool struct{ name string }

func (s stubTool) Name() string { return s.name }
func (s stubTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: s.name}
}
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) { return "", nil }

// Subset 只保留指定工具（供具名子 agent 限縮能力），不存在的名稱略過。
func TestRegistry_Subset(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"read_file", "bash", "write_file"} {
		r.Register(stubTool{name: n})
	}
	sub := r.Subset([]string{"read_file", "nonexistent"})
	defs := sub.GetAvailableTools()
	if len(defs) != 1 || defs[0].Name != "read_file" {
		t.Fatalf("Subset 應只留 read_file，got %+v", defs)
	}
	// 原註冊表不受影響
	if len(r.GetAvailableTools()) != 3 {
		t.Error("Subset 不應改動原註冊表")
	}
}

// GetAvailableTools 必須回傳穩定（按名稱排序）的順序——這是 prompt cache 前綴位元組一致的前提。
func TestRegistry_GetAvailableTools_StableSorted(t *testing.T) {
	r := NewRegistry()
	// 故意以非字典序註冊
	for _, n := range []string{"write_file", "bash", "read_file", "edit_file", "read_skill"} {
		r.Register(stubTool{name: n})
	}

	want := []string{"bash", "edit_file", "read_file", "read_skill", "write_file"}

	// 多次呼叫都應得到相同且已排序的順序（若仍迭代 map，順序會在多次間抖動）。
	for iter := range 20 {
		defs := r.GetAvailableTools()
		if len(defs) != len(want) {
			t.Fatalf("工具數不對: got %d want %d", len(defs), len(want))
		}
		for i, w := range want {
			if defs[i].Name != w {
				t.Fatalf("第 %d 次呼叫順序不穩/未排序: pos %d = %q，want %q", iter, i, defs[i].Name, w)
			}
		}
	}
}
