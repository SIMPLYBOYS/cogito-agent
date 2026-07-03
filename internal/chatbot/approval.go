package chatbot

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ApprovalManager 是 channel-based 的全局審批單例：危險工具調用在 WaitForApproval 處阻塞，
// 直到人類通過 Slack 回覆 approve/reject，由 ResolveApproval / ResolveByChannel 喂入結果喚醒；
// 超過 Timeout 無人響應則自動拒絕，避免 goroutine 永久洩漏。
type ApprovalResult struct {
	Allowed bool
	Reason  string
}

// pendingTask 記錄一個等待中的審批：結果 channel + 發起它的頻道（用於裸 approve 的按頻道解析）。
type pendingTask struct {
	ch        chan ApprovalResult
	channelID string
}

const defaultApprovalTimeout = 5 * time.Minute

type ApprovalManager struct {
	mu           sync.Mutex
	pendingTasks map[string]*pendingTask
	Timeout      time.Duration // <=0 時回退到 defaultApprovalTimeout
}

var GlobalApprovalMgr = &ApprovalManager{
	pendingTasks: make(map[string]*pendingTask),
	Timeout:      defaultApprovalTimeout,
}

// WaitForApproval 註冊審批請求、通過 notify 推送給人類，然後阻塞當前 goroutine 等待結果。
// 弱點修補③：超過 Timeout 無響應自動拒絕並清理，杜絕 goroutine 永久掛起。
func (m *ApprovalManager) WaitForApproval(taskID, channelID, toolName, args string, notify func(text string)) (bool, string) {
	ch := make(chan ApprovalResult, 1) // 容量 1，避免 sender 阻塞

	m.mu.Lock()
	m.pendingTasks[taskID] = &pendingTask{ch: ch, channelID: channelID}
	m.mu.Unlock()

	timeout := m.Timeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}

	notice := fmt.Sprintf("⚠️ *高危操作審批請求*\nAgent 試圖執行：\n• 工具: `%s`\n• 參數: `%s`\n任務 ID: `%s`\n👉 直接回復 `approve` / `reject` 即可（也可帶 ID：`approve %s`）。%.0f 分鐘內無響應將自動拒絕。",
		toolName, args, taskID, taskID, timeout.Minutes())
	if notify != nil {
		notify(notice)
	} else {
		fmt.Printf("\n[需要審批 TaskID: %s]\n%s\n", taskID, notice)
	}

	log.Printf("[Approval] 發送審批請求 (TaskID: %s, 頻道: %s)，協程掛起等待...\n", taskID, channelID)

	select {
	case result := <-ch:
		m.remove(taskID)
		return result.Allowed, result.Reason
	case <-time.After(timeout):
		m.remove(taskID)
		log.Printf("[Approval] ⏰ 審批超時 (TaskID: %s)，自動拒絕。\n", taskID)
		return false, fmt.Sprintf("審批超時（%v 無人響應），已自動拒絕該操作。", timeout)
	}
}

func (m *ApprovalManager) remove(taskID string) {
	m.mu.Lock()
	delete(m.pendingTasks, taskID)
	m.mu.Unlock()
}

// ResolveApproval 按 taskID 精確喚醒一個等待中的審批。返回是否命中。
func (m *ApprovalManager) ResolveApproval(taskID string, allowed bool, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	pt, ok := m.pendingTasks[taskID]
	if !ok {
		return false
	}
	delete(m.pendingTasks, taskID)
	pt.ch <- ApprovalResult{Allowed: allowed, Reason: reason} // ch 有緩衝，持鎖發送不阻塞
	return true
}

// ResolveByChannel 喚醒某頻道下所有等待中的審批（弱點修補①：裸 approve/reject 無需手打長 taskID）。
// 返回處理的數量。
func (m *ApprovalManager) ResolveByChannel(channelID string, allowed bool, reason string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for id, pt := range m.pendingTasks {
		if pt.channelID == channelID {
			delete(m.pendingTasks, id)
			pt.ch <- ApprovalResult{Allowed: allowed, Reason: reason}
			count++
		}
	}
	return count
}

// bashDangerPatterns 是 bash（前景/背景）與 MCP 工具參數共用的危險指令黑名單。
// 比對前一律 lowercase，故模式用小寫即可涵蓋大小寫變體（DROP TABLE / RM -RF…）。
// ponytail: 黑名單是防禦縱深，列不完；真正邊界應是「低信任來源預設走 Docker sandbox（--network none）」。
var bashDangerPatterns = []string{
	`rm\s+-\S*[rf]`,            // rm 帶 r/f 旗標的任意組合：-rf/-fr/-r/-f/-Rf…（原 rm\s+-r 漏掉 -fr）
	`rm\s+--(recursive|force)`, // 長旗標形式
	`find\s+.*-delete`,         // find … -delete 遞歸刪除
	`sudo\s+`,                  // 提權
	`drop\s+`,                  // SQL DROP
	`truncate\s+table`,         // SQL TRUNCATE
	`>.*\.go`,                  // 重定向覆蓋 .go 文件（防 LLM 絕望時清空源碼）
	`nginx\s+-s`,               // 重啟/停止 nginx（會中斷線上服務）
	`systemctl\s+`,             // 系統服務管理（start/stop/restart）
	`kill\s+`,                  // 殺進程
	`printenv`,                 // 傾印環境變數（含金鑰）
}

// secretSegments 是機密/憑證路徑片段：bash 或 MCP 工具參數命中即要求審批，擋 cat .env / curl -d @.env
// / 讀 id_rsa 這類外洩。比對前一律 lowercase。與寫檔工具的敏感目標判斷（IsDangerousCommand 內）互補。
var secretSegments = []string{".env", "id_rsa", "credentials", ".ssh", ".aws", "/etc/passwd", "/etc/shadow"}

// mcpDangerVerbs 是遠端 MCP 工具名裡的「破壞性/高風險」動詞——語意未知時據此攔下要審批。
var mcpDangerVerbs = []string{
	"delete", "remove", "drop", "destroy", "truncate", "overwrite",
	"rmdir", "unlink", "kill", "terminate", "shutdown", "reboot", "format",
	"exec", "deploy", "install", "write", "upload",
}

// IsDangerousCommand 判斷工具調用是否命中高危黑名單，命中則觸發人工審批。
//   - bash：命中危險指令模式（遞歸刪除/提權/重啟服務/殺進程/覆蓋源碼…）。
//   - write_file / edit_file：寫入路徑試圖逃出工作區（絕對路徑 / .. 穿越）或觸及敏感目標
//     （.env 機密、.git/.ssh/.aws 憑證、.claw 自身配置）。正常的工作區內源碼寫入不攔，保留 UX。
//   - mcp_call_tool：遠端 MCP 工具語意未知，用啟發式——工具名像破壞性、或參數命中危險指令/
//     憑證路徑 → 審批。讀類（query/list/get/search/read…）不攔，保留 UX。
func IsDangerousCommand(toolName string, args string) bool {
	switch toolName {
	case "bash", "bash_background":
		// 背景 bash 與前景 bash 走同一危險黑名單——否則 `bash_background` 會成為審批繞道
		// （長駐的 rm -rf / kill 更危險）。對應 memory「拉起需審批」的重啟條件。
		low := strings.ToLower(args)
		for _, p := range bashDangerPatterns {
			if matched, _ := regexp.MatchString(p, low); matched {
				return true
			}
		}
		for _, seg := range secretSegments {
			if strings.Contains(low, seg) {
				return true // 機密/憑證外洩（cat .env / 讀 id_rsa …）
			}
		}
	case "mcp_call_tool":
		// gateway 以 {"name":<遠端工具>,"arguments":{...}} 調用遠端 MCP 工具。解析不出 → 保守審批。
		var a struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(args), &a) != nil {
			return true
		}
		lname := strings.ToLower(a.Name)
		for _, verb := range mcpDangerVerbs {
			if strings.Contains(lname, verb) {
				return true
			}
		}
		scan := lname + " " + strings.ToLower(string(a.Arguments))
		for _, p := range bashDangerPatterns {
			if matched, _ := regexp.MatchString(p, scan); matched {
				return true
			}
		}
		for _, seg := range secretSegments {
			if strings.Contains(scan, seg) {
				return true
			}
		}
	case "write_file", "edit_file":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
			return false
		}
		// 逃出工作區：絕對路徑或 .. 穿越
		if filepath.IsAbs(a.Path) {
			return true
		}
		cleaned := filepath.Clean(a.Path)
		if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return true
		}
		// 敏感目標：機密文件與憑證/版控/自身配置目錄
		if base := filepath.Base(cleaned); base == ".env" || strings.HasPrefix(base, ".env.") {
			return true
		}
		for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
			switch seg {
			case ".git", ".ssh", ".aws", ".claw":
				return true
			}
		}
	}
	return false
}
