package context

import (
	"fmt"
	"strings"
)

// RecoveryManager 在工具執行失敗時，按工具名 + 錯誤特徵匹配，給原始報錯拼接一段
// 可執行的"救援指南"，把冰冷的錯誤字符串升級為"下一步該怎麼做"的指示。純規則、無狀態。
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject 接收原始報錯，匹配已知特徵模式，返回增強後的報錯信息。
// 命中則在末尾追加 [系統救援指南]；未命中則原樣返回。
func (rm *RecoveryManager) AnalyzeAndInject(toolName string, rawError string) string {
	var hint string

	// rawError 原樣匹配我們自己工具裡寫的中文錯誤；lowerError 匹配 Go/OS 拋出的英文 POSIX 錯誤。
	lowerError := strings.ToLower(rawError)

	switch toolName {
	case "edit_file":
		// 匹配 fuzzyReplace 手寫的固定報錯
		if strings.Contains(rawError, "在文件中未找到 old_text") || strings.Contains(rawError, "找不到該代碼片段") {
			hint = "你提供的 old_text 與文件當前內容不一致，或者缺少必要的縮進。請先使用 `read_file` 工具重新讀取該文件，獲取最新、準確的內容後，再重新發起編輯。"
		} else if strings.Contains(rawError, "匹配到了多處") || strings.Contains(rawError, "提供更多上下文") {
			hint = "你的 old_text 不夠具體，命中了多個相同代碼塊。請在 old_text 中增加上下相鄰的幾行代碼，以確保替換的唯一性。"
		}

	case "read_file", "write_file":
		// 匹配 Go os 包拋出的 POSIX 標準錯誤
		if strings.Contains(lowerError, "no such file or directory") {
			hint = "路徑似乎不正確。請不要憑空猜測，先使用 `bash` 執行 `ls -la` 或 `find . -name` 命令查找正確的目錄結構和文件名。"
		} else if strings.Contains(lowerError, "permission denied") {
			hint = "你沒有權限操作該文件。請檢查工作區限制，或者思考是否需要修改其他文件。"
		}

	case "bash":
		if strings.Contains(lowerError, "command not found") {
			hint = "系統中未安裝該命令。請先思考：是否有替代命令？或者你需要先編寫腳本進行安裝？"
		} else if strings.Contains(rawError, "超時") || strings.Contains(rawError, "DeadlineExceeded") {
			// 匹配 bash 工具手寫的 30s context.WithTimeout 報錯
			hint = "該命令執行被超時強殺。如果它是一個常駐服務（如 server 或 watch），請將其轉入後臺執行（例如使用 `nohup ... &`），不要阻塞主線程。"
		} else if strings.Contains(lowerError, "syntax error") {
			hint = "Bash 語法錯誤。請檢查引號轉義或特殊字符，確保命令在終端中可直接運行。"
		} else if strings.Contains(lowerError, "imported and not used") || strings.Contains(lowerError, "declared and not used") {
			hint = "Go 編譯錯誤：有未使用的 import 或變數。移除它（或必要時以 `_` 接收）後重新 build。"
		} else if strings.Contains(lowerError, "undefined:") || strings.Contains(lowerError, "undeclared name") {
			hint = "引用了不存在的符號。檢查拼字、是否漏 import、或它是否定義在別處——用 `grep -rn` 找出正確名稱再修，不要憑空猜。"
		} else if strings.Contains(lowerError, "no required module provides package") || strings.Contains(lowerError, "cannot find package") || strings.Contains(lowerError, "go.mod file not found") {
			hint = "Go 模組／套件問題。確認 import 路徑正確；缺依賴跑 `go mod tidy`；若不在模組內則先 `go mod init` 或 cd 到含 go.mod 的目錄。"
		} else if strings.Contains(lowerError, "modulenotfounderror") || strings.Contains(lowerError, "importerror") {
			hint = "Python 找不到模組。確認 import 名稱、是否需 `pip install <套件>`、或是否在正確的虛擬環境（venv）。"
		}

	case "mcp_call_tool", "mcp_describe_tool":
		// 遠端 MCP 工具最常見的失敗是「工具名／參數對不上 schema」。僅在像 name/arg 錯時才注入，
		// 避免對「查無資料」之類的正常結果亂提示。
		if strings.Contains(lowerError, "validation") || strings.Contains(lowerError, "unknown tool") ||
			strings.Contains(lowerError, "not found") || strings.Contains(lowerError, "invalid") ||
			strings.Contains(lowerError, "required") {
			hint = "外部 MCP 工具調用失敗，多半是工具名或參數對不上。請用 `mcp_describe_tool` 確認該工具的【精確名稱與參數 schema】（對照 System Prompt 的工具目錄）；遠端工具名格式為 `<server>__<tool>`，arguments 必須符合其 schema。"
		}
	}

	if hint == "" {
		return rawError
	}

	return fmt.Sprintf("%s\n\n[系統救援指南]: %s", rawError, hint)
}
