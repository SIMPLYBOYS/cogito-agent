package chatbot

import (
	"fmt"
	"log"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/authz"
)

// 配對指令：未授權者用 `pair` 自助發起，管理員用 `pair approve|reject|list|revoke` 回應。
//
// 【一律不帶斜線】Slack 把 / 開頭的訊息當成自家 slash command 攔下（回「/pair 是無效指令」），
// 訊息根本到不了 bot。裸字兩個平台都通，故對外提示一律用裸字；/pair 仍收，因為 Telegram 使用者
// 習慣加斜線、且那邊送得到——與既有的 stop/"/stop" 同慣例。
//
// 【為何不沿用裸 approve】approve/reject 已被【高危操作審批】佔用（見 tryResolveApproval）。
// 若配對也用它，`approve 7F2K` 就有兩種可能語意——是批准配對碼還是批准 taskID？加 "pair"
// 前綴讓兩者永不相撞，代價只是多打四個字。

// tryPairRequest 處理未授權者的 /pair。命中即消費（回 true）。
//
// 【刻意不碰共享狀態】只寫待審檔、只回覆 conv。它跑在授權閘【之前】，任何副作用都會變成
// 未授權者可觸發的攻擊面——故不寫 lastRoute、不開 session、不建工作目錄。
func (c *Core) tryPairRequest(conv, userID, text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/pair", "pair":
	default:
		return false
	}
	if c.authz == nil {
		SendMessage(conv, "⚠️ 本部署未啟用配對功能，請管理員將你加進 COGITO_ALLOWED_USERS。")
		return true
	}

	req, err := c.authz.RequestPair(c.platform, userID, "")
	if err != nil {
		SendMessage(conv, "⚠️ "+err.Error())
		return true
	}
	log.Printf("[%s] 🔑 %s 發起配對，碼 %s\n", c.platform, userID, req.Code)
	// 【措辭要準】這則多半發在【私聊】裡，管理員根本不在這個對話——寫「請管理員在此輸入」
	// 等於要他走到你電腦前打字。批准只認碼、不綁對話（見 ApprovePair），所以管理員在他自己
	// 跟 bot 的任何對話下指令都算數。這則的任務是：給碼、說明要把碼轉給誰、承諾會回來通知。
	SendMessage(conv, fmt.Sprintf(
		"🔑 配對碼：`%s`（%d 分鐘內有效）\n"+
			"把這組碼交給管理員，請他對我說 `pair approve %s`（他在自己的對話裡說就行），"+
			"或由他到面板的 governance 頁批准。\n"+
			"通過後我會在這裡通知你。",
		req.Code, int(authz.PairTTL.Minutes()), req.Code))
	return true
}

// tryPairAdminCommand 處理管理員的配對管理指令。命中即消費。
//
// 非管理員命中【也消費掉】並回拒——與 tryResolveApproval 同樣的理由：別讓一般使用者
// 把配對指令當成一般任務丟給 agent 執行（那會變成用 prompt 繞過授權）。
func (c *Core) tryPairAdminCommand(conv, userID, text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "pair") {
		return false
	}
	// 裸 "pair" 是使用者端的請求動詞，已在授權閘前處理過；能走到這裡代表發話者已授權，
	// 對他而言那是無意義輸入——回提示而非靜默。
	if len(fields) == 1 {
		SendMessage(conv, "用法：`pair list` / `pair approve <碼> [admin]` / `pair reject <碼>` / `pair revoke <條目>`")
		return true
	}
	if c.authz == nil {
		SendMessage(conv, "⚠️ 本部署未啟用配對功能。")
		return true
	}
	if !c.isAdmin(userID) {
		log.Printf("[%s] 🚫 非管理員 %s 嘗試 pair %s，已拒絕\n", c.platform, userID, fields[1])
		SendMessage(conv, "🚫 只有管理員可以管理配對授權。")
		return true
	}

	by := c.platform + ":" + userID
	switch strings.ToLower(fields[1]) {
	case "list":
		c.pairList(conv)
	case "approve":
		c.pairApprove(conv, by, fields[2:])
	case "reject":
		c.pairReject(conv, fields[2:])
	case "revoke":
		c.pairRevoke(conv, by, fields[2:])
	default:
		SendMessage(conv, "用法：`pair list` / `pair approve <碼> [admin]` / `pair reject <碼>` / `pair revoke <條目>`")
	}
	return true
}

func (c *Core) pairList(conv string) {
	pending, err := c.authz.Pending()
	if err != nil {
		SendMessage(conv, "⚠️ 讀取待審失敗："+err.Error())
		return
	}
	recs, err := c.authz.Records()
	if err != nil {
		SendMessage(conv, "⚠️ 讀取授權記錄失敗："+err.Error())
		return
	}

	var b strings.Builder
	b.WriteString("*待審配對*\n")
	if len(pending) == 0 {
		b.WriteString("（無）\n")
	}
	for _, p := range pending {
		b.WriteString(fmt.Sprintf("• `%s` → %s（%s）\n", p.Code, p.Entry, p.Created))
	}

	b.WriteString("\n*生效中的授權*（env bootstrap 不列，撤不掉）\n")
	live := 0
	for _, r := range recs {
		if r.Status != authz.StatusApproved {
			continue
		}
		live++
		b.WriteString(fmt.Sprintf("• `%s` [%s] 由 %s 於 %s 批准\n", r.Entry, r.Role, r.ApprovedBy, r.ApprovedAt))
	}
	if live == 0 {
		b.WriteString("（無）\n")
	}
	SendMessage(conv, b.String())
}

func (c *Core) pairApprove(conv, by string, args []string) {
	if len(args) == 0 {
		SendMessage(conv, "用法：`pair approve <碼> [admin]`（加 admin 才給審批權）")
		return
	}
	// 角色預設 user——授予審批權必須是【顯式】動作，不能手滑打出一個 admin。
	role := authz.RoleUser
	if len(args) > 1 && strings.EqualFold(args[1], "admin") {
		role = authz.RoleAdmin
	}
	req, err := c.authz.ApprovePair(args[0], role, by)
	if err != nil {
		SendMessage(conv, "⚠️ "+err.Error())
		return
	}
	log.Printf("[%s] ✅ %s 批准配對 %s → %s [%s]\n", c.platform, by, args[0], req.Entry, role)
	SendMessage(conv, fmt.Sprintf("✅ 已授權 `%s`（角色 %s）。立即生效，免重啟。", req.Entry, role))
	// 通知當事人：他不知道自己被批准了，否則得靠猜的一直重試。
	if _, raw, ok := strings.Cut(req.Entry, ":"); ok {
		SendMessage(req.Platform+":"+raw, "✅ 你的配對已通過，現在可以直接對我下任務了。")
	}
}

func (c *Core) pairReject(conv string, args []string) {
	if len(args) == 0 {
		SendMessage(conv, "用法：`pair reject <碼>`")
		return
	}
	req, err := c.authz.RejectPair(args[0])
	if err != nil {
		SendMessage(conv, "⚠️ "+err.Error())
		return
	}
	// 刻意【不】通知被否決者：否則這個介面就成了「猜誰是管理員」的探測器，
	// 且被拒者收到訊息只會反覆重試。他的碼會自然過期。
	SendMessage(conv, fmt.Sprintf("🗑️ 已否決 `%s` 的配對請求。", req.Entry))
}

func (c *Core) pairRevoke(conv, by string, args []string) {
	if len(args) == 0 {
		SendMessage(conv, "用法：`pair revoke <條目>`（條目形如 `telegram:12345`，見 `pair list`）")
		return
	}
	entry := args[0]
	if err := c.authz.Revoke(entry, by); err != nil {
		SendMessage(conv, "⚠️ "+err.Error())
		return
	}
	log.Printf("[%s] 🚫 %s 撤銷授權 %s\n", c.platform, by, entry)
	SendMessage(conv, fmt.Sprintf("🚫 已撤銷 `%s`。立即失效。", entry))
}
