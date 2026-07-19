package main

import (
	"net/http"
	"os"
	"strings"
)

// 可編輯的【非祕密】操作型 env，依主題分組——每組在 platform 頁是一個獨立的小表單（就地看+改）。
// 金鑰／token 一律不在此（維持遮罩唯讀）。provider/模型另在 Provider 區的表單。
type envKeyDef struct {
	Key, Label, Hint string
	Toggle           bool // true＝勾選（"1"/""）
}

var (
	accessEnv = []envKeyDef{
		{"COGITO_ALLOWED_USERS", "可驅動 agent 的 id", "逗號分隔；空＝拒絕所有", false},
		{"COGITO_ADMIN_USERS", "可審批的 id", "逗號分隔；空＝同 ALLOWED", false},
	}
	runtimeEnv = []envKeyDef{
		{"COGITO_SANDBOX", "沙箱模式", "docker / 空＝host", false},
		{"COGITO_MCP_CONFIG", "MCP 設定檔路徑", "如 ./.mcp.json", false},
		{"COGITO_SESSION_DIR", "session 目錄", "空＝純記憶體", false},
		{"COGITO_SUMMARY", "滾動摘要壓縮", "off＝關", false},
	}
	evolveEnv = []envKeyDef{
		{"COGITO_MEMORY_SYNTH", "記憶合成", "", true},
		{"COGITO_SKILL_SYNTH", "技能合成", "", true},
		{"COGITO_KG_SYNTH", "知識圖譜合成", "", true},
	}
	obsEnv = []envKeyDef{
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTel endpoint", "空＝不上報 trace", false},
	}
	// Provider 表單直接寫死這些欄位（不走 loadEnvGroup），但需在白名單內。含 embedder（非祕密：
	// model / base url；embedder 金鑰仍是祕密、不在此）。
	providerEnvKeys = []string{"COGITO_PROVIDER", "CLAUDE_MODEL", "OPENAI_MODEL", "OPENAI_BASE_URL", "COGITO_EMBED_MODEL", "COGITO_EMBED_BASE_URL"}
	// cron 結果推播設定（非祕密：只是頻道 id 與開關；token 走「金鑰／祕密」區）。表單在 cron 頁。
	cronEnvKeys = []string{cronTZKey, notifyTargetKey, notifyErrOnlyKey}
)

// allowedEnvKeys 是【可經面板寫入的白名單】：任一表單只能改這些 key（金鑰/token 不在內）。updateEnvFile
// 亦只碰傳進去的 key，雙保險。
var allowedEnvKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range providerEnvKeys {
		m[k] = true
	}
	for _, k := range cronEnvKeys {
		m[k] = true
	}
	for _, g := range [][]envKeyDef{accessEnv, runtimeEnv, evolveEnv, obsEnv} {
		for _, e := range g {
			m[e.Key] = true
		}
	}
	return m
}()

type envField struct {
	Key, Label, Hint, Value string
	Toggle, On              bool
}

// loadEnvGroup 帶入一組 key 的現值（os.Getenv＝dashboard 啟動時從 .env 載入的生效值）。
func loadEnvGroup(defs []envKeyDef) []envField {
	out := make([]envField, 0, len(defs))
	for _, e := range defs {
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
	// _return 讓非 platform 的頁（如 /cron）也能用這個表單，改完導回原頁。只收站內絕對路徑，
	// 防開放重導。
	back := "/platform"
	if v := r.FormValue("_return"); strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
		back = v
	}
	updates := map[string]string{}
	for _, k := range strings.Fields(r.FormValue("_fields")) {
		if allowedEnvKeys[k] { // 白名單外一律忽略（防被塞入祕密 key）
			updates[k] = strings.TrimSpace(r.FormValue(k)) // toggle 未勾＝FormValue ""＝關
		}
	}
	if len(updates) == 0 {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	// 推播目標會明碼顯示在頁面上：存檔就擋掉誤貼的 token，別等到執行才失敗（那時憑證已經寫進 .env）。
	if v := updates[notifyTargetKey]; v != "" {
		if _, _, err := parseNotifyTarget(v); err != nil {
			s.setFlash("⚠️ 推播目標無效：" + err.Error())
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
	}
	if err := updateEnvFile(".env", updates); err != nil {
		http.Error(w, "寫入 .env 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	for k, v := range updates {
		_ = os.Setenv(k, v) // 面板檢視即時反映；bot 仍需重啟
	}
	s.setFlash("✓ 已寫入 .env——bot 需【重啟】才套用。本面板檢視已即時更新。")
	http.Redirect(w, r, back, http.StatusSeeOther)
}
