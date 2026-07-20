package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type EditFileTool struct {
	workDir string
}

func NewEditFileTool(workDir string) *EditFileTool {
	return &EditFileTool{workDir: workDir}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "對現有文件進行局部的字符串替換。這比重寫整個文件更安全、更快速。請提供足夠的 old_text 上下文以確保匹配的唯一性。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要修改的文件路徑",
				},
				"old_text": map[string]interface{}{
					"type":        "string",
					"description": "文件中原有的文本。必須包含足夠的上下文，以確保在文件中的唯一性。",
				},
				"new_text": map[string]interface{}{
					"type":        "string",
					"description": "要替換成的新文本",
				},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

type editFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input editFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}

	fullPath, err := resolveForWrite(t.workDir, input.Path)
	if err != nil {
		return "", err
	}

	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("讀取文件失敗，請確認路徑是否正確: %w", err)
	}
	originalContent := string(contentBytes)

	newContent, err := fuzzyReplace(originalContent, input.OldText, input.NewText)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("寫回文件失敗: %w", err)
	}

	return fmt.Sprintf("✅ 成功修改文件: %s", input.Path), nil
}

func fuzzyReplace(originalContent, oldText, newText string) (string, error) {
	// L1: 精確匹配
	count := strings.Count(originalContent, oldText)
	if count == 1 {
		return strings.Replace(originalContent, oldText, newText, 1), nil
	}
	if count > 1 {
		return "", fmt.Errorf("old_text 匹配到了 %d 處，請提供更多的上下文代碼以確保唯一性", count)
	}

	// L2: 換行符歸一化
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")

	count = strings.Count(normalizedContent, normalizedOld)
	if count == 1 {
		return strings.Replace(normalizedContent, normalizedOld, newText, 1), nil
	}

	// L3: Trim Space 匹配
	trimmedOld := strings.TrimSpace(normalizedOld)
	if trimmedOld != "" {
		count = strings.Count(normalizedContent, trimmedOld)
		if count == 1 {
			return strings.Replace(normalizedContent, trimmedOld, newText, 1), nil
		}
	}

	// L4: 逐行去縮進匹配
	return lineByLineReplace(normalizedContent, normalizedOld, newText)
}

func lineByLineReplace(content, oldText, newText string) (string, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimSpace(oldText), "\n")

	// 注意：前綴「找不到該代碼片段」被 context.RecoveryManager 以 strings.Contains 匹配以注入
	// 「先 read_file 重讀」的救援指南——改字串前先看 internal/context/recovery.go。
	// 這裡是【還沒搜就放棄】的退化情形（old_text 比整個檔案還長），與 matchCount==0 不同：
	// 只說「找不到」會誤導模型去微調縮排重試，故把實際事實（行數對比）講出來。
	if len(contentLines) < len(oldLines) { // strings.Split 永不回空 slice，故不必檢查 len(oldLines)==0
		return "", fmt.Errorf("找不到該代碼片段：old_text 共 %d 行，多於檔案的 %d 行，不可能匹配——你可能改錯了檔案，或 old_text 是憑記憶構造而非來自檔案實際內容",
			len(oldLines), len(contentLines))
	}

	for i := range oldLines {
		oldLines[i] = strings.TrimSpace(oldLines[i])
	}

	matchCount := 0
	matchStartIndex := -1
	matchEndIndex := -1

	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		isMatch := true
		for j := 0; j < len(oldLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != oldLines[j] {
				isMatch = false
				break
			}
		}

		if isMatch {
			matchCount++
			matchStartIndex = i
			matchEndIndex = i + len(oldLines)
		}
	}

	if matchCount == 0 {
		// 走到這裡代表 L1~L4（精確 → 換行正規化 → TrimSpace → 逐行去縮排）全部沒中，所以【不是】
		// 縮排或空白問題——把這件事說出來，才不會誘導模型微調空白盲目重試（非同構重試死循環的燃料）。
		return "", fmt.Errorf("在文件中未找到 old_text：已嘗試精確比對、換行正規化、去縮排逐行比對，皆無匹配——調整縮排/空白再試沒有用，是內容本身與檔案現況不符")
	}
	if matchCount > 1 {
		return "", fmt.Errorf("模糊匹配到了 %d 處代碼，請提供更多上下文以定位", matchCount)
	}

	// 提取匹配塊首行的「基礎縮進前綴」，把 newText 重新對齊到該縮進層級，避免深層巢狀塊
	// 被替換成模型給的淺縮進、格式走樣。
	baseIndent := leadingWhitespace(contentLines[matchStartIndex])

	var newContentLines []string
	newContentLines = append(newContentLines, contentLines[:matchStartIndex]...)
	newContentLines = append(newContentLines, reindent(newText, baseIndent))
	newContentLines = append(newContentLines, contentLines[matchEndIndex:]...)

	return strings.Join(newContentLines, "\n"), nil
}

// leadingWhitespace 返回一行開頭的空白前綴（空格/Tab）。
func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// reindent 把 newText 重新對齊到 baseIndent：先把整段 dedent 到 flush-left（扣掉所有非空行
// 的最小共同縮進，保留行間相對結構），再給每個非空行補上 baseIndent。空白行歸零、不留尾隨
// 空白。如此 newText 不論模型給 0/4/8 空格縮進，最終都正確貼合目標塊的縮進層級。
func reindent(newText, baseIndent string) string {
	lines := strings.Split(newText, "\n")

	// 1. 求所有非空行的最小共同前導空白寬度
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue // 空白行不參與 dedent 計算
		}
		indent := len(leadingWhitespace(l))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent < 0 {
		minIndent = 0 // 全部是空白行
	}

	// 2. 逐行 dedent 掉 minIndent 後補 baseIndent
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = "" // 空白行歸零，避免遺留原縮進
			continue
		}
		lines[i] = baseIndent + l[minIndent:]
	}

	return strings.Join(lines, "\n")
}
