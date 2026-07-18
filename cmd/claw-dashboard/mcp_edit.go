package main

import (
	"net/http"
	"strings"
)

// MCP server 編輯皆【寫入】.mcp.json：CSRF 同 chat。.mcp.json 在 bot／chat 啟動時 dial，故增刪/切換
// 需【重啟】才生效（與 env 同）。env/headers 可能含祕密，故 addMCPServer 刻意不收；列表也遮罩其值。

func (s *server) mcpAdd(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	err := addMCPServer(mcpConfigPath(), name, r.FormValue("type"), r.FormValue("target"), r.FormValue("args"))
	if err != nil {
		s.setFlash("⚠️ 新增 MCP server 失敗：" + err.Error())
	} else {
		s.setFlash("✓ 已新增 MCP server：" + name + "——bot／chat 需【重啟】才連上。")
	}
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

func (s *server) mcpEdit(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	err := editMCPServer(mcpConfigPath(), name, r.FormValue("type"), r.FormValue("target"), r.FormValue("args"))
	if err != nil {
		s.setFlash("⚠️ 編輯 MCP server 失敗：" + err.Error())
	} else {
		s.setFlash("✓ 已更新 MCP server：" + name + "（env/headers 保留不動）——重啟後生效。")
	}
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

func (s *server) mcpRemove(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := removeMCPServer(mcpConfigPath(), name); err != nil {
		s.setFlash("⚠️ 移除 MCP server 失敗：" + err.Error())
	} else {
		s.setFlash("✓ 已移除 MCP server：" + name)
	}
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

func (s *server) mcpToggle(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := toggleMCPServer(mcpConfigPath(), name); err != nil {
		s.setFlash("⚠️ 切換 MCP server 失敗：" + err.Error())
	} else {
		s.setFlash("✓ 已切換 MCP server：" + name + "——重啟後生效。")
	}
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}
