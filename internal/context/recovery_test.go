package context

import (
	"strings"
	"testing"
)

func TestRecoveryManager_AnalyzeAndInject(t *testing.T) {
	rm := NewRecoveryManager()

	const guideMarker = "[系統救援指南]"

	cases := []struct {
		name     string
		tool     string
		rawError string
		wantHint bool   // 是否應注入救援指南
		wantKey  string // wantHint 時，指南里應包含的關鍵詞
	}{
		{"edit_file 未找到 old_text", "edit_file", "edit_file 失敗: 在文件中未找到 old_text", true, "read_file"},
		{"edit_file 找不到片段", "edit_file", "找不到該代碼片段", true, "read_file"},
		// 退化情形（old_text 比檔案還長）的實際訊息帶了行數事實——前綴仍須命中救援規則。
		// 這條鎖住 edit_file.go 與本檔的字串耦合：改了那邊的用詞、忘了這邊，這裡會紅。
		{"edit_file 片段超過檔案行數", "edit_file",
			"找不到該代碼片段：old_text 共 9 行，多於檔案的 3 行，不可能匹配——你可能改錯了檔案，或 old_text 是憑記憶構造而非來自檔案實際內容",
			true, "read_file"},
		{"edit_file 多處匹配", "edit_file", "old_text 匹配到了多處，請提供更多上下文", true, "唯一性"},
		{"read_file 文件不存在", "read_file", "open /x/y: no such file or directory", true, "ls -la"},
		{"write_file 權限不足", "write_file", "open /etc/x: Permission denied", true, "權限"},
		{"bash 命令不存在", "bash", "bash: foo: command not found", true, "替代命令"},
		{"bash 超時", "bash", "命令執行超時(30s)，已被系統強制終止", true, "nohup"},
		{"bash 語法錯誤", "bash", "bash: -c: line 1: syntax error near unexpected token", true, "語法"},
		{"Go 未使用 import", "bash", "./main.go:5:2: \"fmt\" imported and not used", true, "import"},
		{"Go 未定義符號", "bash", "./x.go:10:6: undefined: Foo", true, "grep"},
		{"Go 缺套件", "bash", "no required module provides package github.com/x/y", true, "go mod"},
		{"Python 缺模組", "bash", "ModuleNotFoundError: No module named 'requests'", true, "pip install"},
		{"MCP 參數驗證錯", "mcp_call_tool", "Error executing tool query_rows: 1 validation error", true, "mcp_describe_tool"},
		{"MCP 一般錯不亂提示", "mcp_call_tool", "internal server error 500", false, ""},
		{"未命中規則原樣返回", "edit_file", "某種我們沒見過的奇怪錯誤", false, ""},
		{"未知工具原樣返回", "search_web", "open /x: no such file or directory", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rm.AnalyzeAndInject(tc.tool, tc.rawError)

			if !tc.wantHint {
				if got != tc.rawError {
					t.Fatalf("期望原樣返回，卻被修改:\n原始: %q\n返回: %q", tc.rawError, got)
				}
				return
			}

			// 命中規則：必須保留原始錯誤 + 含指南標記 + 含關鍵詞
			if !strings.Contains(got, tc.rawError) {
				t.Errorf("注入後丟失了原始錯誤:\n%q", got)
			}
			if !strings.Contains(got, guideMarker) {
				t.Errorf("缺少救援指南標記 %q:\n%q", guideMarker, got)
			}
			if tc.wantKey != "" && !strings.Contains(got, tc.wantKey) {
				t.Errorf("救援指南缺少關鍵詞 %q:\n%q", tc.wantKey, got)
			}
		})
	}
}
