package main

import (
	"net/http"
	"os"
	"strings"
)

// editableEnv 是「一般設定」表單的【非祕密】操作型 env key（金鑰／token 一律不在此列——見 Provider 區的
// 遮罩狀態）。provider/模型另放在 Provider 區的表單（就地編輯）。每個 key 標型別（toggle＝勾選＝"1"/""）。
var editableEnv = []struct {
	Key, Label, Hint string
	Toggle           bool
}{
	{"COGITO_ALLOWED_USERS", "可驅動 agent 的 id", "逗號分隔；空＝拒絕所有", false},
	{"COGITO_ADMIN_USERS", "可審批的 id", "逗號分隔；空＝同 ALLOWED", false},
	{"COGITO_MCP_CONFIG", "MCP 設定檔路徑", "如 ./.mcp.json", false},
	{"COGITO_SANDBOX", "沙箱模式", "docker / 空＝host", false},
	{"COGITO_SUMMARY", "滾動摘要壓縮", "off＝關", false},
	{"COGITO_MEMORY_SYNTH", "記憶合成", "", true},
	{"COGITO_SKILL_SYNTH", "技能合成", "", true},
	{"COGITO_KG_SYNTH", "知識圖譜合成", "", true},
}

// allowedEnvKeys 是【可經面板寫入的白名單】：任一表單只能改這些 key（金鑰/token 不在內）。updateEnvFile
// 亦只碰傳進去的 key，雙保險。含 Provider 區表單的 key + editableEnv。
var allowedEnvKeys = func() map[string]bool {
	m := map[string]bool{
		"COGITO_PROVIDER": true, "CLAUDE_MODEL": true, "OPENAI_MODEL": true, "OPENAI_BASE_URL": true,
	}
	for _, e := range editableEnv {
		m[e.Key] = true
	}
	return m
}()

type envField struct {
	Key, Label, Hint, Value string
	Toggle, On              bool
}

// loadEnvFields 帶入「一般設定」各 key 的現值。
func loadEnvFields() []envField {
	out := make([]envField, 0, len(editableEnv))
	for _, e := range editableEnv {
		v := os.Getenv(e.Key)
		out = append(out, envField{Key: e.Key, Label: e.Label, Hint: e.Hint, Value: v, Toggle: e.Toggle, On: v == "1"})
	}
	return out
}

// envConfigSave 只更新表單 _fields 列出、且在白名單內的 key（分區小表單各改各的、不互相清空）；祕密
// 不在白名單、永不經手。寫 .env 後 os.Setenv 讓面板檢視即時反映（bot 另一行程仍需重啟）。CSRF 同 chat。
func (s *server) envConfigSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	updates := map[string]string{}
	for _, k := range strings.Fields(r.FormValue("_fields")) {
		if allowedEnvKeys[k] { // 白名單外一律忽略（防被塞入祕密 key）
			updates[k] = strings.TrimSpace(r.FormValue(k)) // toggle 未勾＝FormValue ""＝關
		}
	}
	if len(updates) == 0 {
		http.Redirect(w, r, "/platform", http.StatusSeeOther)
		return
	}
	if err := updateEnvFile(".env", updates); err != nil {
		http.Error(w, "寫入 .env 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	for k, v := range updates {
		_ = os.Setenv(k, v) // 面板檢視即時反映；bot 仍需重啟
	}
	s.setFlash("✓ 已寫入 .env——bot 需【重啟】才套用。本面板檢視已即時更新。")
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}
