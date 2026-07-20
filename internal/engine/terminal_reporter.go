package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type TerminalReporter struct{}

func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinking(ctx context.Context) {
	fmt.Printf("\n[🤔 思考中] 模型正在推理...\n")
}

func (r *TerminalReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	fmt.Printf("[🛠️ 呼叫工具] %s\n", toolName)
	// 清理參數中的換行符和特殊字元
	displayArgs := strings.ReplaceAll(args, "\n", "\\n")
	displayArgs = strings.ReplaceAll(displayArgs, "\r", "\\r")
	displayArgs = schema.TruncRunes(displayArgs, 150, "... (已截斷)") // 工具參數常含中文，byte 切會壞
	fmt.Printf("   參數: %s\n", displayArgs)
}

func (r *TerminalReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		fmt.Printf("[❌ 執行失敗] %s\n", toolName)
		if result != "" {
			fmt.Printf("   錯誤: %s\n", result)
		}
	} else {
		fmt.Printf("[✅ 執行成功] %s\n", toolName)
	}
}

func (r *TerminalReporter) OnTurn(ctx context.Context, turn int) {
	fmt.Printf("\n========== Turn %d ==========\n", turn)
}

func (r *TerminalReporter) OnMessage(ctx context.Context, content string) {
	if content == "" {
		return
	}
	fmt.Printf("\n🤖 Agent 回覆:\n%s\n\n", content)
}

var _ Reporter = (*TerminalReporter)(nil)
