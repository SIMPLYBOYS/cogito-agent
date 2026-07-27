package main

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/authz"
)

// 面板端的配對授權：批准／否決待審請求、撤銷既有授權。
//
// 【為何面板可以改授權】與技能／MCP／cron 同一個信任模型：綁 loopback、有 CSRF，操作者是【人】。
// 而且授權清單是這幾個裡風險最低的一個——它不執行任何東西，只決定誰能發問。
//
// 【與 bot 的關係】兩邊寫同一個檔（.claw/authorized-users.json）。bot 的 Store 以 mtime 失效
// 快取，所以面板這裡一按下去，bot 那邊下一次查詢就生效——不必重啟、不必通知。

// authzStore 建一個綁在本 workspace 的授權 Store。
//
// env 集合傳 nil：面板【不做】授權判定，只做增刪與呈現，bootstrap 名單另外從 env 直接讀來顯示。
// 傳 nil 的副作用是 Revoke 不會擋 env 條目——故 authzRevoke 另外自己擋，見該處。
func (s *server) authzStore() *authz.Store {
	return authz.New(filepath.Join(s.workspace, ".claw"), nil, nil)
}

// fillAuthz 把授權資料填進頁面模型。壞檔【不靜默】：記進 AuthzErr 攤在頁面上。
func (d *governanceData) fillAuthz(store *authz.Store) {
	pending, err := store.Pending()
	if err != nil {
		d.AuthzErr = err.Error()
		return
	}
	recs, err := store.Records()
	if err != nil {
		d.AuthzErr = err.Error()
		return
	}
	d.Pending = pending
	for _, r := range recs {
		if r.Status == authz.StatusApproved {
			d.Granted = append(d.Granted, r)
		} else {
			d.Revoked = append(d.Revoked, r)
		}
	}
	sort.Slice(d.Granted, func(i, j int) bool { return d.Granted[i].Entry < d.Granted[j].Entry })
	// 撤銷記錄按時間【新到舊】：稽核時想看的是最近發生什麼。
	sort.Slice(d.Revoked, func(i, j int) bool { return d.Revoked[i].RevokedAt > d.Revoked[j].RevokedAt })
}

// operatorID 是面板操作者寫進稽核軌跡的【預設】身分。面板無認證（綁 loopback＝操作者即機器主人），
// 只能記到「這是從面板做的」這個粒度——誠實標示，不假裝知道是誰。
const operatorID = "dashboard(operator)"

// operatorIDFrom 回傳寫進稽核軌跡的操作者身分：若前面有【可信反向代理】注入 X-Forwarded-User
// （oauth2-proxy / tailscale serve 之類），就把粒度從 "dashboard(operator)" 升級成具體的人
// （aaron@example.com）；沒有就退回預設。
//
// ⚠️ 這【不是認證】：只綁 loopback、前面沒有代理時，直連者可自行偽造這個標頭——但能直連 loopback 的
// 本來就是機器主人，偽造只是改自己稽核裡的署名，風險可接受。要讓它可信，前提是「面板唯一入口是會
// 覆寫此標頭的可信代理」。標頭來自信任邊界，故截長 + 濾掉控制字元，免污染稽核記錄（log/JSON 注入）。
func operatorIDFrom(r *http.Request) string {
	u := strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
	if u == "" {
		return operatorID
	}
	if len(u) > 120 {
		u = u[:120]
	}
	u = strings.Map(func(rn rune) rune {
		if rn < 0x20 || rn == 0x7f {
			return -1 // 丟掉換行/控制字元
		}
		return rn
	}, u)
	if u == "" {
		return operatorID
	}
	return u
}

func (s *server) authzApprove(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	// 角色預設 user；授予審批權必須是【顯式】勾選，不能手滑點出一個 admin。
	role := authz.RoleUser
	if r.FormValue("role") == authz.RoleAdmin {
		role = authz.RoleAdmin
	}
	req, err := s.authzStore().ApprovePair(r.FormValue("code"), role, operatorIDFrom(r))
	if err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else {
		s.setFlash("✓ 已授權 " + req.Entry + "（角色 " + role + "）——bot 下次查詢即生效，免重啟。")
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

func (s *server) authzReject(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	req, err := s.authzStore().RejectPair(r.FormValue("code"))
	if err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else {
		s.setFlash("🗑️ 已否決 " + req.Entry + " 的配對請求。")
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

func (s *server) authzRevoke(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	entry := r.FormValue("entry")
	// 自己擋 env bootstrap：本頁的 Store 沒帶 env 集合（見 authzStore），Revoke 擋不到它。
	// 不擋的話會產生一筆「撤銷了但重啟後又活過來」的記錄——比擋下來更難理解。
	if parseCSVEnvSet("COGITO_ALLOWED_USERS")[entry] || parseCSVEnvSet("COGITO_ADMIN_USERS")[entry] {
		s.setFlash("⚠️ " + entry + " 來自環境變數（bootstrap），無法從此處撤銷——請改 .env 後重啟。")
		http.Redirect(w, r, "/governance", http.StatusSeeOther)
		return
	}
	if err := s.authzStore().Revoke(entry, operatorIDFrom(r)); err != nil {
		s.setFlash("⚠️ " + err.Error())
	} else {
		s.setFlash("🚫 已撤銷 " + entry + "——bot 下次查詢即失效。")
	}
	http.Redirect(w, r, "/governance", http.StatusSeeOther)
}

// parseCSVEnvSet 同 parseCSVEnv，但回集合供 O(1) 比對。
func parseCSVEnvSet(key string) map[string]bool {
	set := map[string]bool{}
	for _, v := range parseCSVEnv(key) {
		set[v] = true
	}
	return set
}
