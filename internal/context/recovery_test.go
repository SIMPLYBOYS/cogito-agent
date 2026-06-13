package context

import (
	"strings"
	"testing"
)

func TestRecoveryManager_AnalyzeAndInject(t *testing.T) {
	rm := NewRecoveryManager()

	const guideMarker = "[系统救援指南]"

	cases := []struct {
		name     string
		tool     string
		rawError string
		wantHint bool   // 是否应注入救援指南
		wantKey  string // wantHint 时，指南里应包含的关键词
	}{
		{"edit_file 未找到 old_text", "edit_file", "edit_file 失败: 在文件中未找到 old_text", true, "read_file"},
		{"edit_file 找不到片段", "edit_file", "找不到该代码片段", true, "read_file"},
		{"edit_file 多处匹配", "edit_file", "old_text 匹配到了多处，请提供更多上下文", true, "唯一性"},
		{"read_file 文件不存在", "read_file", "open /x/y: no such file or directory", true, "ls -la"},
		{"write_file 权限不足", "write_file", "open /etc/x: Permission denied", true, "权限"},
		{"bash 命令不存在", "bash", "bash: foo: command not found", true, "替代命令"},
		{"bash 超时", "bash", "命令执行超时(30s)，已被系统强制终止", true, "nohup"},
		{"bash 语法错误", "bash", "bash: -c: line 1: syntax error near unexpected token", true, "语法"},
		{"未命中规则原样返回", "edit_file", "某种我们没见过的奇怪错误", false, ""},
		{"未知工具原样返回", "search_web", "open /x: no such file or directory", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rm.AnalyzeAndInject(tc.tool, tc.rawError)

			if !tc.wantHint {
				if got != tc.rawError {
					t.Fatalf("期望原样返回，却被修改:\n原始: %q\n返回: %q", tc.rawError, got)
				}
				return
			}

			// 命中规则：必须保留原始错误 + 含指南标记 + 含关键词
			if !strings.Contains(got, tc.rawError) {
				t.Errorf("注入后丢失了原始错误:\n%q", got)
			}
			if !strings.Contains(got, guideMarker) {
				t.Errorf("缺少救援指南标记 %q:\n%q", guideMarker, got)
			}
			if tc.wantKey != "" && !strings.Contains(got, tc.wantKey) {
				t.Errorf("救援指南缺少关键词 %q:\n%q", tc.wantKey, got)
			}
		})
	}
}
