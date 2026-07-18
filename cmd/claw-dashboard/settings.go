package main

import (
	"net/http"
	"os"
	"strings"
)

// editableEnv 是允許從面板編輯的【非祕密】操作型 env key（金鑰／token 一律不在此列——見 platform 頁
// 的遮罩檢視）。每個 key 標型別（toggle＝勾選＝"1"/""；否則文字）。這決定了 .env 編輯的白名單：
// updateEnvFile 只會動這些 key，祕密行逐字保留。
var editableEnv = []struct {
	Key, Label, Hint string
	Toggle           bool
}{
	{"COGITO_PROVIDER", "Provider", "claude / openai（空＝claude）", false},
	{"CLAUDE_MODEL", "Claude 模型", "如 claude-opus-4-8（空＝預設）", false},
	{"COGITO_ALLOWED_USERS", "可驅動 agent 的 id", "逗號分隔；空＝fail-closed 拒絕所有", false},
	{"COGITO_ADMIN_USERS", "可審批的 id", "逗號分隔；空＝回退為 ALLOWED_USERS", false},
	{"COGITO_MCP_CONFIG", "MCP 設定檔路徑", "如 ./.mcp.json（空＝不載 MCP）", false},
	{"COGITO_SANDBOX", "沙箱模式", "docker / 空＝host 直跑", false},
	{"COGITO_SUMMARY", "滾動摘要壓縮", "off＝關；空／其他＝開", false},
	{"COGITO_MEMORY_SYNTH", "記憶合成", "", true},
	{"COGITO_SKILL_SYNTH", "技能合成", "", true},
	{"COGITO_KG_SYNTH", "知識圖譜合成", "", true},
}

type envField struct {
	Key, Label, Hint, Value string
	Toggle, On              bool
}

// loadEnvFields 帶入各可編輯 key 的現值（os.Getenv＝dashboard 啟動時從 .env 載入的生效值）。
func loadEnvFields() []envField {
	out := make([]envField, 0, len(editableEnv))
	for _, e := range editableEnv {
		v := os.Getenv(e.Key)
		out = append(out, envField{Key: e.Key, Label: e.Label, Hint: e.Hint, Value: v, Toggle: e.Toggle, On: v == "1"})
	}
	return out
}

// envConfigSave 把可編輯的【非祕密】設定寫回 .env（updateEnvFile 保證祕密行逐字不動）。CSRF 同 chat。
// env 是啟動時讀的，故 bot 需【重啟】才套用；同步 os.Setenv 讓本面板的檢視即時反映（僅視圖，不改 bot）。
func (s *server) envConfigSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	updates := map[string]string{}
	for _, e := range editableEnv {
		if e.Toggle {
			if r.FormValue(e.Key) != "" {
				updates[e.Key] = "1"
			} else {
				updates[e.Key] = ""
			}
		} else {
			updates[e.Key] = strings.TrimSpace(r.FormValue(e.Key))
		}
	}
	if err := updateEnvFile(".env", updates); err != nil {
		http.Error(w, "寫入 .env 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	for k, v := range updates {
		_ = os.Setenv(k, v) // 讓面板檢視即時反映；bot（另一行程）仍需重啟才生效
	}
	s.setFlash("✓ 已寫入 .env——bot 需【重啟】才套用（provider/token 在啟動時讀）。本面板檢視已即時更新。")
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}
