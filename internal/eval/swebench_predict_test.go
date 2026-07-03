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

// captureDiff 在臨時 git repo 抓「含新檔」的 patch——不需 API key、不需 clone。
func TestCaptureDiff_NewFile(t *testing.T) {
	dir := t.TempDir()
	if err := runBashIn(dir, "git init -q"); err != nil {
		t.Skipf("git 不可用，略過: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := captureDiff(dir)
	if err != nil {
		t.Fatalf("captureDiff 失敗: %v", err)
	}
	if !strings.Contains(patch, "new.txt") || !strings.Contains(patch, "hello") {
		t.Errorf("patch 應含新檔名與內容，got:\n%s", patch)
	}
}

// runBashIn 非零退出應回 error（供 captureDiff 的 git add 失敗路徑）。
func TestRunBashIn_NonZeroExit(t *testing.T) {
	if err := runBashIn(t.TempDir(), "exit 3"); err == nil {
		t.Error("非零退出碼應回 error")
	}
	if err := runBashIn(t.TempDir(), "true"); err != nil {
		t.Errorf("退出碼 0 不應回 error: %v", err)
	}
}
