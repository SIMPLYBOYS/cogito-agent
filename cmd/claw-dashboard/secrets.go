package main

import (
	"net/http"
	"os"
	"strings"
)

// secretKeys 是可經面板【顯示／輪替】的祕密 env（金鑰/token）。與 allowedEnvKeys（非祕密）分開。
var secretKeys = []string{
	"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "COGITO_EMBED_API_KEY",
	"SLACK_BOT_TOKEN", "SLACK_APP_TOKEN", "TELEGRAM_BOT_TOKEN",
	"LANGFUSE_PUBLIC_KEY", "LANGFUSE_SECRET_KEY",
}

var secretKeySet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range secretKeys {
		m[k] = true
	}
	return m
}()

type secretRow struct {
	Key string
	Set bool
}

func loadSecrets() []secretRow {
	out := make([]secretRow, 0, len(secretKeys))
	for _, k := range secretKeys {
		out = append(out, secretRow{Key: k, Set: strings.TrimSpace(os.Getenv(k)) != ""})
	}
	return out
}

// secretsAllowed 是硬性護欄：只有【綁 loopback】的部署才允許顯示/輪替祕密。非 loopback 綁定必然設了
// COGITO_DASH_INSECURE（guard 否則拒絕啟動），故此旗標為真時一律不碰祕密——金鑰永不經過對外網路。
func secretsAllowed() bool { return os.Getenv("COGITO_DASH_INSECURE") != "1" }

// secretReveal 即時回傳單一祕密現值（供眼睛圖示按需顯示，不預先嵌進頁面）。loopback-only + 同源 +
// 白名單 + no-store。跨站雖因同源政策讀不到回應，仍多擋一層。
func (s *server) secretReveal(w http.ResponseWriter, r *http.Request) {
	if !secretsAllowed() {
		http.Error(w, "非 loopback 部署不提供祕密顯示", http.StatusForbidden)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	key := r.URL.Query().Get("key")
	if !secretKeySet[key] {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(os.Getenv(key)))
}

// secretSave 輪替一個祕密：寫回 .env（updateEnvFile 只碰這個 key、其餘逐字保留）。loopback-only + CSRF。
func (s *server) secretSave(w http.ResponseWriter, r *http.Request) {
	if !secretsAllowed() {
		http.Error(w, "非 loopback 部署不提供祕密編輯", http.StatusForbidden)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	key := r.FormValue("key")
	if !secretKeySet[key] {
		http.Error(w, "未知的祕密 key", http.StatusBadRequest)
		return
	}
	val := strings.TrimSpace(r.FormValue("value")) // 去掉貼上時的換行/空白
	if val == "" {
		s.setFlash("⚠️ 新值為空，未更動 " + key + "。")
		http.Redirect(w, r, "/platform", http.StatusSeeOther)
		return
	}
	if err := updateEnvFile(".env", map[string]string{key: val}); err != nil {
		http.Error(w, "寫入 .env 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = os.Setenv(key, val)
	s.setFlash("✓ 已輪替 " + key + "——bot 需【重啟】才套用。")
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

// mcpSecretReveal 即時回傳某 MCP server 的 env/headers 單一 key 現值。同 /secret/reveal 的護欄
// （loopback-only + 同源 + no-store）。
func (s *server) mcpSecretReveal(w http.ResponseWriter, r *http.Request) {
	if !secretsAllowed() {
		http.Error(w, "非 loopback 部署不提供祕密顯示", http.StatusForbidden)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "env" && kind != "headers" {
		http.NotFound(w, r)
		return
	}
	v, ok := readMCPSecretValue(mcpConfigPath(), q.Get("server"), kind, q.Get("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(v))
}

// mcpSecretSave 輪替某 MCP server 的 env/headers 單一【既有】key。loopback-only + CSRF；只改既有 key。
func (s *server) mcpSecretSave(w http.ResponseWriter, r *http.Request) {
	if !secretsAllowed() {
		http.Error(w, "非 loopback 部署不提供祕密編輯", http.StatusForbidden)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	server, kind, key := r.FormValue("server"), r.FormValue("kind"), r.FormValue("key")
	if kind != "env" && kind != "headers" {
		http.Error(w, "kind 需為 env / headers", http.StatusBadRequest)
		return
	}
	if _, ok := readMCPSecretValue(mcpConfigPath(), server, kind, key); !ok {
		http.Error(w, "該 server 無此 "+kind+" key（只能輪替既有值）", http.StatusBadRequest)
		return
	}
	val := strings.TrimSpace(r.FormValue("value"))
	if val == "" {
		s.setFlash("⚠️ 新值為空，未更動。")
		http.Redirect(w, r, "/platform", http.StatusSeeOther)
		return
	}
	if err := setMCPSecretValue(mcpConfigPath(), server, kind, key, val); err != nil {
		http.Error(w, "寫入 .mcp.json 失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	s.setFlash("✓ 已輪替 " + server + " 的 " + kind + ":" + key + "——bot 需【重啟】才套用。")
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

// platformJS：眼睛圖示的最小客戶端。點眼睛→fetch /secret/reveal 顯示現值；再點→遮回。textContent
// 塞入→免 XSS。獨立檔以走 script-src 'self'（不放 inline script）。
func (s *server) platformJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write([]byte(platformJSSrc))
}

// 統一眼睛：按鈕帶 data-url（reveal 端點），值放在同一 .secfield 內的 .sv。頂層祕密與 MCP env/headers
// 共用。textContent 塞入→免 XSS。
const platformJSSrc = `document.querySelectorAll('.eye').forEach(function (btn) {
  btn.addEventListener('click', function () {
    var wrap = btn.closest('.secfield');
    var sv = wrap && wrap.querySelector('.sv');
    if (!sv) return;
    if (btn.getAttribute('data-shown') === '1') {
      sv.textContent = '••••••••'; btn.setAttribute('data-shown', '0'); btn.textContent = '👁';
      return;
    }
    fetch(btn.getAttribute('data-url'))
      .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
      .then(function (v) { sv.textContent = v; btn.setAttribute('data-shown', '1'); btn.textContent = '🙈'; })
      .catch(function () { sv.textContent = '（讀取失敗）'; });
  });
});`
