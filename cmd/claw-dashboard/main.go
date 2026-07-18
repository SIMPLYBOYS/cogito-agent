// cmd/claw-dashboard 是 cogito 的【operator dashboard】：維運者用的機器級控制面（與 cmd/dashboard 那個
// 只看 bench 跑分的輕量儀表板分開）。本階段（C0+C1+C2-檢視）只做【唯讀 + 綁 loopback】：看執行樹
// （run-tree）、各頻道成本、治理狀態。寫入動作仍留 chat（沿用 IM 身分）；remote-auth / web 寫入 / 嵌入
// chat 皆延後——理由與分階段見 vault：cogito-agent-Operator-Dashboard-C-Spec。
//
//	go run ./cmd/claw-dashboard                 # → http://127.0.0.1:8091（僅本機）
//	遠端存取請用 SSH tunnel：ssh -L 8091:127.0.0.1:8091 <host>
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func main() {
	// 預設綁 loopback。非 loopback 綁定會被 fail-closed 守衛擋下（remote-auth 尚未實作，見 guard.go）。
	addr := flag.String("addr", "127.0.0.1:8091", "監聽位址（預設僅本機 loopback；remote 存取需 auth，尚未實作）")
	sessions := flag.String("sessions", os.Getenv("COGITO_SESSION_DIR"), "session 目錄（預設取自 COGITO_SESSION_DIR）")
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

	disp := *addr
	if strings.HasPrefix(disp, ":") {
		disp = "localhost" + disp
	}
	srv := newServer(store, *sessions)
	log.Printf("🛠️  cogito operator dashboard 已啟動（唯讀）：http://%s（sessions：%q）", disp, *sessions)
	log.Fatal(http.ListenAndServe(*addr, srv))
}
