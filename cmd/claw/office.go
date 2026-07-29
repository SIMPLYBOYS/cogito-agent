package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/chatbot"
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
	core := chatbot.NewCore("office", rootDir, factory, send)

	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in struct{ Agent, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Agent == "" || in.Text == "" {
			http.Error(w, `need {"agent","text"}`, http.StatusBadRequest)
			return
		}
		core.Dispatch(in.Agent, user, in.Text) // channelID = persona id（p17）→ conv "office:p17"
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	go func() {
		log.Printf("[office] HTTP 派工入口監聽 %s（user=%s，需在 COGITO_ALLOWED_USERS 名單內）", addr, user)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[office] HTTP 入口結束: %v", err)
		}
	}()
}
