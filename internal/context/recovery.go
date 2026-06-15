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
		// 匹配 ch07 fuzzyReplace 手寫的固定報錯
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
			// 匹配 ch06 bash 工具手寫的 30s context.WithTimeout 報錯
			hint = "該命令執行被超時強殺。如果它是一個常駐服務（如 server 或 watch），請將其轉入後臺執行（例如使用 `nohup ... &`），不要阻塞主線程。"
		} else if strings.Contains(lowerError, "syntax error") {
			hint = "Bash 語法錯誤。請檢查引號轉義或特殊字符，確保命令在終端中可直接運行。"
		}
	}

	if hint == "" {
		return rawError
	}

	return fmt.Sprintf("%s\n\n[系統救援指南]: %s", rawError, hint)
}
