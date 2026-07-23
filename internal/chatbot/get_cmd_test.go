package chatbot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTryGetCommand 驗證 `get` 檔案取回：命中/未命中、路徑逃逸拒絕、目錄拒絕、缺檔、
// 平台無 fileSender 的明確錯誤、成功路由到註冊的 sender。
func TestTryGetCommand(t *testing.T) {
	c := NewCore("gettest", t.TempDir(), nil, func(string, string) {})
	conv := "gettest:chanA"
	wd := c.channelWorkDir(conv)
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "report.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 出訊收集：get 的回饋都走 SendMessage → senders
	var replies []string
	senders.Store("gettest", func(_, text string) { replies = append(replies, text) })
	last := func() string {
		if len(replies) == 0 {
			return ""
		}
		return replies[len(replies)-1]
	}

	if c.tryGetCommand(conv, "幫我看看 report.md") {
		t.Error("一般任務文字不該被當成 get 指令消費")
	}
	if !c.tryGetCommand(conv, "get") || !strings.Contains(last(), "用法") {
		t.Errorf("裸 `get` 應回用法，得到 %q", last())
	}
	if !c.tryGetCommand(conv, "get ../../../etc/passwd") || !strings.Contains(last(), "逃出") {
		t.Errorf("路徑逃逸應被拒絕，得到 %q", last())
	}
	if !c.tryGetCommand(conv, "get .") || !strings.Contains(last(), "目錄") {
		t.Errorf("目錄應被拒絕，得到 %q", last())
	}
	if !c.tryGetCommand(conv, "get nope.txt") || !strings.Contains(last(), "找不到") {
		t.Errorf("缺檔應回找不到，得到 %q", last())
	}
	// 尚未註冊 fileSender → 明確「不支援」
	if !c.tryGetCommand(conv, "get report.md") || !strings.Contains(last(), "不支援檔案取回") {
		t.Errorf("無 fileSender 應回不支援，得到 %q", last())
	}
	// 註冊 fake sender → 成功路由，收到解析後的絕對路徑
	var gotRaw, gotPath string
	RegisterFileSender("gettest", func(raw, path string) error { gotRaw, gotPath = raw, path; return nil })
	if !c.tryGetCommand(conv, "get report.md") {
		t.Fatal("`get report.md` 應被消費")
	}
	if gotRaw != "chanA" || filepath.Base(gotPath) != "report.md" || !filepath.IsAbs(gotPath) {
		t.Errorf("sender 收到 raw=%q path=%q，期望 chanA / 絕對路徑的 report.md", gotRaw, gotPath)
	}
}
