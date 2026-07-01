package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// 查無記憶時，recall 必須回【強制不確定性聲明】——對抗幻覺記憶：明說沒有、禁止杜撰。
func TestRecall_NoMatchForcesUncertainty(t *testing.T) {
	tool := NewRecallTool(t.TempDir()) // 空目錄 → 無任何記憶
	out, err := tool.Execute(t.Context(), json.RawMessage(`{"query":"完全不存在的主題鯨魚"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "查無") {
		t.Errorf("應明說查無：%s", out)
	}
	for _, want := range []string{"請勿", "杜撰"} {
		if !strings.Contains(out, want) {
			t.Errorf("應含反編造指示 %q：%s", want, out)
		}
	}
}
