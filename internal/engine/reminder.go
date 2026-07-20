package engine

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// 無窮迴圈探測雙閾值：
//   - fingerprintThreshold：完全相同 (工具名+參數) 連續失敗——典型的原地踏步無窮迴圈。
//   - sameToolThreshold：同一工具(任意參數)連續失敗——攔截「每次微調參數盲目試錯」這種
//     會繞過指紋探測的失控重試（閾值略高，給正常的多檔案探索留餘量）。
const (
	fingerprintThreshold = 3
	sameToolThreshold    = 5
)

// ReminderInjector 是無窮迴圈探測器：同時按 (工具名+參數) 指紋與 (工具名) 兩個維度統計連續
// 失敗次數，任一維度達閾值就注入一條強力"跳出迴圈、換策略"的提醒，避免模型盯著同一個錯誤
// （或不斷換參數）盲目重試燒 API。任意一次成功即全部清零。
type ReminderInjector struct {
	consecutiveFailures map[string]int // key: 指紋(工具名+參數)
	toolFailures        map[string]int // key: 工具名(任意參數)
}

func NewReminderInjector() *ReminderInjector {
	return &ReminderInjector{
		consecutiveFailures: make(map[string]int),
		toolFailures:        make(map[string]int),
	}
}

// fingerprintPathKeys 是會被當成「路徑」做 filepath.Clean 規範化的參數名。
var fingerprintPathKeys = map[string]bool{
	"path": true, "file": true, "filepath": true,
	"filename": true, "dir": true, "dirpath": true,
}

// normalizeArgs 把工具參數正規化成 canonical 形式再供雜湊，讓「本質相同、只差微小寫法」的
// 重試塌縮成同一指紋：
//   - 解析 JSON 後重新序列化（Go 對 map key 排序）→ 消掉 key 順序與 JSON 內空白差異。
//   - 所有字串值 TrimSpace → 吃掉尾/首空格（如 "/tmp/a.txt " ≡ "/tmp/a.txt"）。
//   - 路徑類參數再 filepath.Clean → 消掉 "./"、"//"、"a/../b" 等冗餘寫法。
//
// 刻意「保守」：不做大小寫折疊（Linux 路徑大小寫敏感），也不把相對路徑解析成絕對路徑
// （需 workDir 上下文且語意依 cwd 而定，易誤併本質不同的目標）——那類殘留交給 sameToolThreshold 兜底。
func normalizeArgs(args []byte) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return strings.TrimSpace(string(args)) // 非 JSON：退化成 trim 原始字串
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" && fingerprintPathKeys[k] {
			s = filepath.Clean(s)
		}
		m[k] = s
	}
	b, err := json.Marshal(m) // Marshal 對 map key 排序 → canonical
	if err != nil {
		return strings.TrimSpace(string(args))
	}
	return string(b)
}

func generateFingerprint(toolName string, args []byte) string {
	hasher := md5.New()
	hasher.Write([]byte(toolName))
	hasher.Write([]byte(normalizeArgs(args)))
	return hex.EncodeToString(hasher.Sum(nil))
}

// CheckTurn 對本輪所有工具的呼叫與結果按順序更新計數，任一達閾值即回傳一條提醒
// （否則 nil）。並行工具全部納入，而非只看第一個。
func (r *ReminderInjector) CheckTurn(calls []schema.ToolCall, results []schema.ToolResult) *schema.Message {
	var nudge *schema.Message
	for i := range calls {
		if i >= len(results) {
			break
		}
		if m := r.CheckAndInject(calls[i], results[i]); m != nil {
			nudge = m
		}
	}
	return nudge
}

// CheckAndInject 根據單個工具的執行結果更新失敗計數。任意一次成功即清零兩個維度；
// 同一指紋連續失敗 ≥fingerprintThreshold 次，或同一工具(任意參數)連續失敗
// ≥sameToolThreshold 次，則回傳一條強提醒消息（否則回傳 nil）。
func (r *ReminderInjector) CheckAndInject(lastToolCall schema.ToolCall, lastResult schema.ToolResult) *schema.Message {
	if !lastResult.IsError {
		r.consecutiveFailures = make(map[string]int)
		r.toolFailures = make(map[string]int)
		return nil
	}

	fingerprint := generateFingerprint(lastToolCall.Name, lastToolCall.Arguments)
	r.consecutiveFailures[fingerprint]++
	r.toolFailures[lastToolCall.Name]++

	fpCount := r.consecutiveFailures[fingerprint]
	toolCount := r.toolFailures[lastToolCall.Name]

	log.Printf("[Reminder] 監控到工具 %s 執行失敗，相同參數連續 %d 次／該工具任意參數連續 %d 次\n",
		lastToolCall.Name, fpCount, toolCount)

	sameArgsLoop := fpCount >= fingerprintThreshold
	varyArgsLoop := toolCount >= sameToolThreshold
	if !sameArgsLoop && !varyArgsLoop {
		return nil
	}

	log.Println("[Reminder] ⚠️ 觸發無窮迴圈干預！注入強力修正指令。")

	var diagnosis string
	if sameArgsLoop {
		diagnosis = fmt.Sprintf("你剛剛連續 %d 次使用完全相同的參數呼叫了 '%s' 工具，並且都失敗了。", fpCount, lastToolCall.Name)
	} else {
		diagnosis = fmt.Sprintf("你已經連續 %d 次呼叫 '%s' 工具（即便每次都更換了參數）並且全部失敗——不斷微調參數的盲目試錯同樣是無窮迴圈。", toolCount, lastToolCall.Name)
	}

	nudgeMsg := fmt.Sprintf(`[SYSTEM REMINDER 警告]
你似乎陷入了無窮迴圈。%s
請立即停止這種無效的重試！你的注意力被當前的報錯過度吸引了。
你需要：
1. 停止猜測參數。跳出當前的局部思維。
2. 徹底改變你的策略（例如先用 read_file / ls / find 等手段重新蒐集事實，而不是繼續猜）。
3. 如果你確實無法通過系統工具解決當前問題，請直接結束任務並向使用者說明你需要什麼人工幫助，而不是繼續盲目消耗 API 資源嘗試。`, diagnosis)

	return &schema.Message{
		Role:    schema.RoleUser,
		Content: nudgeMsg,
	}
}
