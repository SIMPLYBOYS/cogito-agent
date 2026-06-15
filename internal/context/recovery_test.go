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
		{"edit_file 多處匹配", "edit_file", "old_text 匹配到了多處，請提供更多上下文", true, "唯一性"},
		{"read_file 文件不存在", "read_file", "open /x/y: no such file or directory", true, "ls -la"},
		{"write_file 權限不足", "write_file", "open /etc/x: Permission denied", true, "權限"},
		{"bash 命令不存在", "bash", "bash: foo: command not found", true, "替代命令"},
		{"bash 超時", "bash", "命令執行超時(30s)，已被系統強制終止", true, "nohup"},
		{"bash 語法錯誤", "bash", "bash: -c: line 1: syntax error near unexpected token", true, "語法"},
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
