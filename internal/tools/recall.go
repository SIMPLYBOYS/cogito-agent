package tools

import (
	"context"
	"encoding/json"
	"fmt"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// RecallTool 是長期記憶的按需檢索端（對齊 read_skill）：System Prompt 只放記憶索引（名稱+描述），
// 模型判定當前任務與某條記憶相關時調用本工具。回傳的是「連通子圖」（種子節點 + k 跳鄰域 + 它們之間
// 的明確關係），讓模型能做多跳關係推理，而非孤立片段。
type RecallTool struct {
	loader *ctxpkg.MemoryLoader
}

// NewRecallTool 的 memoryBaseDir 是含 .claw/memory 的目錄（須與 composer 取記憶索引的目錄一致）。
func NewRecallTool(memoryBaseDir string) *RecallTool {
	return &RecallTool{loader: ctxpkg.NewMemoryLoader(memoryBaseDir)}
}

func (t *RecallTool) Name() string { return "recall" }

func (t *RecallTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "依關鍵字檢索你的長期記憶，回傳相關的『連通子圖』——命中的記憶 + 與它直接相關（透過 [[連結]]）的鄰近記憶 + 它們之間的關係。當任務與 System Prompt『長期記憶索引』某條相關，或你需要過往沉澱的慣例/教訓/事實、或想看某主題如何牽連其他主題時調用（支援中英關鍵字）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "檢索關鍵字，可多詞（中英皆可）",
				},
				"hops": map[string]interface{}{
					"type":        "integer",
					"description": "沿關係擴張的跳數（預設 1：種子 + 直接鄰居）。需要更廣的關聯脈絡時可設 2。",
				},
			},
			"required": []string{"query"},
		},
	}
}

type recallArgs struct {
	Query string `json:"query"`
	Hops  int    `json:"hops"`
}

func (t *RecallTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in recallArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	out := t.loader.RecallGraph(in.Query, in.Hops)
	if out == "" {
		// 找不到不是錯誤——回觀察讓模型據此繼續（error-as-observation）。
		return fmt.Sprintf("（長期記憶中沒有與 %q 相關的內容）", in.Query), nil
	}
	return out, nil
}
