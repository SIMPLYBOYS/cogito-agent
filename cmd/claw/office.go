package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
	"github.com/SIMPLYBOYS/cogito-agent/internal/cmdutil"
)

// startOfficeHTTP：COGITO_HTTP_ADDR 與 COGITO_HTTP_TOKEN 都設定時，開一個 HTTP 派工入口，
// 讓像素辦公室的 Web 外殼（unity_demo 的 /shell）直接交辦任務給 office 平台的 Core。
//
// 授權模型兩層：HTTP 層共享 token 擋外人（token 沒設就整個不開——這個入口能跑任意任務）；
// 進 Core 後沿用 fail-closed allowlist——COGITO_HTTP_USER（預設 office-web）必須列在
// COGITO_ALLOWED_USERS 才會被受理，審批（approve/reject）同一身分走同一入口。
// 出訊（審批卡/完成/失敗訊息）經 rawSend POST 回橋的 /office/chat，顯示在 Web 工作串。
func startOfficeHTTP(factory chatbot.EngineFactory, rootDir string) {
	addr, token := os.Getenv("COGITO_HTTP_ADDR"), os.Getenv("COGITO_HTTP_TOKEN")
	if addr == "" || token == "" {
		return
	}
	user := os.Getenv("COGITO_HTTP_USER")
	if user == "" {
		user = "office-web"
	}
	bridge := os.Getenv("COGITO_OFFICE_URL")
	client := &http.Client{Timeout: 3 * time.Second}
	send := func(channelID, text string) {
		if bridge == "" {
			return
		}
		// agent 帶完整 conv 身分（office:p17）——與 OfficeReporter 事件同一把鍵，橋端同路解析
		b, _ := json.Marshal(map[string]string{"agent": "office:" + channelID, "text": text})
		resp, err := client.Post(bridge+"/office/chat", "application/json", bytes.NewReader(b))
		if err == nil {
			resp.Body.Close()
		}
	}
	// 【fail-closed 綁定守衛】這個入口能執行【任意任務】（bash／寫檔），對外只有一道共享 bearer
	// token、且無 TLS（token 明文過網）。故預設只准 loopback：遠端走 SSH tunnel，真要曝光得顯式表態。
	// 與 dashboard 共用同一把尺（cmdutil.IsLoopback）——新入口漏做這層正是這次補上的原因。
	// 守衛不成立時【只關掉這個入口】，不拖垮 Slack/TG：危險的東西沒開起來即達成 fail-closed。
	if officeBindDenied(addr, os.Getenv("COGITO_HTTP_INSECURE") == "1") {
		log.Printf("⛔ [office] 拒絕在非 loopback 位址 %q 開派工入口——它能執行任意任務，對外曝光僅靠共享 token 且無 TLS。\n"+
			"    ・遠端請用 SSH tunnel（推薦）：ssh -L <port>:127.0.0.1:<port> <host>\n"+
			"    ・真要對外曝光（自負風險）：設 COGITO_HTTP_INSECURE=1\n"+
			"    本次【未啟用】office HTTP 入口，bot 其餘功能不受影響。", addr)
		return
	}

	core := chatbot.NewCore("office", rootDir, factory, send)

	mux := http.NewServeMux()
	mux.HandleFunc("/task", officeTaskHandler(token, user, core.Dispatch))
	// 顯式 timeout：預設的 http.Server 沒有任何讀寫上限，一條慢連線就能長期佔著（Slowloris）。
	// Dispatch 本身很快（任務進背景 goroutine），但指令路徑會同步 POST 回橋，故 write 留寬一點。
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		log.Printf("[office] HTTP 派工入口監聽 %s（user=%s，需在 COGITO_ALLOWED_USERS 名單內）", addr, user)
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("[office] HTTP 入口結束: %v", err)
		}
	}()
}

// officeBindDenied 是綁定政策：非 loopback 且未顯式 insecure ＝ 拒開入口。抽成函式供單測釘住
// ——這條是「能執行任意任務的入口別意外對外曝光」的唯一防線，退回「不守衛」就是把它開給網際網路。
func officeBindDenied(addr string, insecure bool) bool {
	return !cmdutil.IsLoopback(addr) && !insecure
}

// officeTaskHandler 是 /task 的處理器。dispatch 以參數注入（而非直接吃 *Core）純為可單測——
// 這是全系統最強的一道入口（能跑任意 bash／寫檔），auth 與輸入把關值得有測試釘住。
func officeTaskHandler(token, user string, dispatch func(channelID, userID, text string)) http.HandlerFunc {
	wantAuth := []byte("Bearer " + token)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		// 常數時間比較：token 是這個入口唯一的門，別用 != 洩漏逐位元組的比對進度。
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), wantAuth) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in struct{ Agent, Text string }
		// 限制請求體，避免一個大 body 就吃掉記憶體（任務文字 1 MB 綽綽有餘）。
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || in.Agent == "" || in.Text == "" {
			http.Error(w, `need {"agent","text"}`, http.StatusBadRequest)
			return
		}
		dispatch(in.Agent, user, in.Text) // channelID = persona id（p17）→ conv "office:p17"
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}
