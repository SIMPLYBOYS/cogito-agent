package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prediction 必須是 SWE-bench 官方 harness 吃的三個欄位名。
func TestPrediction_OfficialFormat(t *testing.T) {
	b, _ := json.Marshal(Prediction{InstanceID: "demo__x-1", Model: "cogito-agent", Patch: "diff --git ..."})
	s := string(b)
	for _, k := range []string{`"instance_id"`, `"model_name_or_path"`, `"model_patch"`} {
		if !strings.Contains(s, k) {
			t.Errorf("predictions 缺官方欄位 %s：%s", k, s)
		}
	}
}

func TestWritePredictions_JSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.jsonl")
	in := []Prediction{
		{InstanceID: "a", Model: "cogito-agent", Patch: "d1"},
		{InstanceID: "b", Model: "cogito-agent", Patch: "d2"},
	}
	if err := WritePredictions(path, in); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("應 2 行 JSONL，got %d", len(lines))
	}
	var p Prediction
	if json.Unmarshal([]byte(lines[0]), &p) != nil || p.InstanceID != "a" {
		t.Errorf("第一行解析錯誤：%q", lines[0])
	}
}
