package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func seedStore(t *testing.T) ctxpkg.SessionStore {
	t.Helper()
	store, err := ctxpkg.NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save(&ctxpkg.SessionSnapshot{
		ID: "chan-A", UpdatedAt: time.Now().Format(time.RFC3339),
		TotalCostUSD: 1.25, Goal: "修好 redis 連線池",
		History: []schema.Message{{Role: schema.RoleUser, Content: "redis 連線池又爆了"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func run(t *testing.T, tool *SearchSessionsTool, args string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func TestSearchSessionsTool(t *testing.T) {
	tool := NewSearchSessionsTool(seedStore(t), "self")

	t.Run("命中：帶出 id／成本／目標／片段", func(t *testing.T) {
		out := run(t, tool, `{"query":"redis"}`)
		for _, want := range []string{"chan-A", "$1.2500", "修好 redis 連線池", "redis 連線池又爆了"} {
			if !strings.Contains(out, want) {
				t.Errorf("輸出少了 %q:\n%s", want, out)
			}
		}
	})

	t.Run("查無結果講人話", func(t *testing.T) {
		if out := run(t, tool, `{"query":"kubernetes"}`); !strings.Contains(out, "找不到") {
			t.Errorf("want 找不到, got %q", out)
		}
	})

	t.Run("空 query 不當成查無結果", func(t *testing.T) {
		if out := run(t, tool, `{"query":"  "}`); !strings.Contains(out, "請提供檢索關鍵字") {
			t.Errorf("want 提示補參數, got %q", out)
		}
	})

	t.Run("排除自己", func(t *testing.T) {
		self := NewSearchSessionsTool(seedStore(t), "chan-A")
		if out := run(t, self, `{"query":"redis"}`); !strings.Contains(out, "找不到") {
			t.Errorf("Exclude 沒傳下去: %q", out)
		}
	})

	t.Run("壞參數回錯", func(t *testing.T) {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":`)); err == nil {
			t.Error("want error on malformed args")
		}
	})
}

// 沒設 COGITO_SESSION_DIR 時工具照樣註冊——但必須明講「沒開持久化」，
// 不能回「查無結果」讓 agent 誤以為過去真的沒談過。
func TestSearchSessionsToolNoStore(t *testing.T) {
	out := run(t, NewSearchSessionsTool(nil, "self"), `{"query":"redis"}`)
	if !strings.Contains(out, "未啟用") {
		t.Errorf("want 未啟用 提示, got %q", out)
	}
}
