package replay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// SubRun 是一個 subagent 的內部執行紀錄，落地成 <sessionWorkDir>/subagents/<callID>.json。
// callID＝主 session 裡那個 spawn_subagent 的 ToolCall.ID——renderer 用它把子 agent 內部掛回委派節點。
type SubRun struct {
	Prompt    string           `json:"prompt"`
	History   []schema.Message `json:"history"`
	UpdatedAt string           `json:"updated_at"`
	Model     string           `json:"model,omitempty"` // 子 agent 實際用的模型——算逐步成本要（子 agent 常用與主 agent 不同的模型）
}

func subDir(sessWorkDir string) string { return filepath.Join(sessWorkDir, "subagents") }

// WriteSubRun 原子寫一個 subagent 內部紀錄。best-effort——呼叫端（引擎）應忽略回傳的 error，
// 不因落地 trace 失敗而影響 subagent 主流程。sessWorkDir 或 callID 為空則不寫（背景子 agent 的情形）。
func WriteSubRun(sessWorkDir, callID string, sr SubRun) error {
	if sessWorkDir == "" || callID == "" {
		return nil
	}
	dir := subDir(sessWorkDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(sr)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, safeName(callID)+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSubRun 讀一個 subagent 內部紀錄；缺檔/壞檔一律回 (zero, false)。
func LoadSubRun(sessWorkDir, callID string) (SubRun, bool) {
	if sessWorkDir == "" || callID == "" {
		return SubRun{}, false
	}
	data, err := os.ReadFile(filepath.Join(subDir(sessWorkDir), safeName(callID)+".json"))
	if err != nil {
		return SubRun{}, false
	}
	var sr SubRun
	if json.Unmarshal(data, &sr) != nil {
		return SubRun{}, false
	}
	return sr, true
}

// safeName 把 callID 洗成安全檔名（call id 通常是模型產的 toolu_*/call_* 短 id，已安全；保險起見再洗）。
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}
