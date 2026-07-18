// cmd/claw-dashboard 是 cogito 的【operator dashboard】：維運者用的機器級控制面（與 cmd/dashboard 那個
// 只看 bench 跑分的輕量儀表板分開）。已實作 C0（loopback 守衛）+ C1（執行樹）+ C2（治理檢視）+
// C3（平台/設定檢視）+ C4（內嵌 operator chat）+ M2（子 agent 內部深度），皆綁 loopback。
//
// 唯讀為預設；C4 是【寫入】能力（就地驅動 agent 跑 bash/寫檔），opt-in（COGITO_DASH_CHAT=1）、受
// loopback + CSRF 保護。remote-auth（雲端存取）仍延後——只能上雲實測驗收，見 vault：
// cogito-agent-Operator-Dashboard-C-Spec。
//
//	go run ./cmd/claw-dashboard                            # → http://127.0.0.1:8091（唯讀，僅本機）
//	COGITO_DASH_CHAT=1 go run ./cmd/claw-dashboard         # 額外啟用內嵌 operator chat（寫入）
//	遠端存取請用 SSH tunnel：ssh -L 8091:127.0.0.1:8091 <host>
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func main() {
	_ = godotenv.Load() // 讀 .env，讓 dashboard 與 bot 看到同一份設定（COGITO_SESSION_DIR / ALLOWED_USERS…）。須先於 flag 預設取值。

	// 預設綁 loopback。非 loopback 綁定會被 fail-closed 守衛擋下（remote-auth 尚未實作，見 guard.go）。
	addr := flag.String("addr", "127.0.0.1:8091", "監聽位址（預設僅本機 loopback；remote 存取需 auth，尚未實作）")
	sessions := flag.String("sessions", os.Getenv("COGITO_SESSION_DIR"), "session 目錄（預設取自 COGITO_SESSION_DIR）")
	workspace := flag.String("workspace", "./workspace", "workspace 根目錄（找 .claw/ 的提案佇列用）")
	flag.Parse()

	insecure := os.Getenv("COGITO_DASH_INSECURE") == "1"
	if deny := checkBindSafety(*addr, insecure); deny != "" {
		log.Fatalf("[claw-dashboard] %s", deny)
	}
	if insecure {
		log.Printf("[claw-dashboard] ⚠️ COGITO_DASH_INSECURE=1：綁定 %q 且【無認證】對外曝光，自負風險。", *addr)
	}

	// 唯讀：只讀既有 session 檔。未設目錄 → store 為 nil，/runs 顯示提示。
	var store ctxpkg.SessionStore
	if *sessions != "" {
		if fs, err := ctxpkg.NewFileSessionStore(*sessions); err != nil {
			log.Printf("[claw-dashboard] ⚠️ 開啟 session 目錄 %q 失敗：%v（runs 將為空）", *sessions, err)
		} else {
			store = fs
		}
	}

	// 內嵌 operator chat（opt-in）：這是【寫入】能力（會跑 bash/寫檔），故預設關。開了才在本行程內
	// 建 agent。需 session 目錄（transcript 從落地的 operator session 讀）；provider 缺 key 則保留唯讀面板。
	var chat *chatRunner
	if os.Getenv("COGITO_DASH_CHAT") == "1" {
		switch {
		case store == nil:
			log.Printf("[claw-dashboard] ⚠️ COGITO_DASH_CHAT=1 但未設 session 目錄——chat 停用（設 COGITO_SESSION_DIR 或 -sessions）")
		default:
			ctxpkg.GlobalSessionMgr.SetStore(store) // operator session 落地 + 唯讀視圖看得到
			wsAbs, _ := filepath.Abs(*workspace)
			if c, err := newChatRunner(wsAbs); err != nil {
				log.Printf("[claw-dashboard] ⚠️ operator chat 停用（保留唯讀面板）：%v", err)
			} else {
				chat = c
				log.Printf("[claw-dashboard] 🗣️  operator chat 已啟用（寫入模式；session=%q，workspace=%q）", "operator", wsAbs)
			}
		}
	}

	disp := *addr
	if strings.HasPrefix(disp, ":") {
		disp = "localhost" + disp
	}
	mode := "唯讀"
	if chat != nil {
		mode = "唯讀 + operator chat（寫入）"
	}
	srv := newServer(store, *sessions, *workspace, chat)
	log.Printf("🛠️  cogito operator dashboard 已啟動（%s）：http://%s（sessions：%q，workspace：%q）", mode, disp, *sessions, *workspace)
	log.Fatal(http.ListenAndServe(*addr, srv))
}
