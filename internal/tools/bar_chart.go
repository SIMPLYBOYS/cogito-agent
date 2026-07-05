package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// BarChartTool 把一組 (標籤, 數值) 渲染成【確定性】的等寬長條圖——比例精準、標籤對齊（CJK 算 2 格）。
// 交給模型自己畫 ASCII 會比例失真、對齊歪掉；用工具保證正確。輸出包在 ``` 內，聊天平台（Telegram
// <pre> / Slack ```）會 render 成等寬圖，終端也是等寬。
type BarChartTool struct{}

func NewBarChartTool() *BarChartTool { return &BarChartTool{} }

func (t *BarChartTool) Name() string { return "bar_chart" }

func (t *BarChartTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "把一組數據渲染成等寬水平長條圖（比例精準、標籤對齊）。適合呈現排名/比較/分布。" +
			"回傳的是【已排版好】的圖表字串，請【原樣、含 ``` 圍欄】放進你的最終回覆，不要改動或重畫。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "圖表標題（可選）"},
				"data": map[string]any{
					"type":        "array",
					"description": "資料點陣列，各含 label(字串) 與 value(數值，需 >=0)",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{"type": "string"},
							"value": map[string]any{"type": "number"},
						},
						"required": []string{"label", "value"},
					},
				},
				"max_width": map[string]any{"type": "integer", "description": "長條最大字元寬度（可選，預設 24）"},
			},
			"required": []string{"data"},
		},
	}
}

type barChartArgs struct {
	Title    string `json:"title"`
	MaxWidth int    `json:"max_width"`
	Data     []struct {
		Label string  `json:"label"`
		Value float64 `json:"value"`
	} `json:"data"`
}

func (t *BarChartTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in barChartArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("參數解析失敗: %w", err)
	}
	if len(in.Data) == 0 {
		return "", fmt.Errorf("data 不能為空")
	}
	return renderBarChart(in), nil
}

func renderBarChart(in barChartArgs) string {
	width := in.MaxWidth
	if width <= 0 {
		width = 24
	}
	if width < 8 {
		width = 8
	}
	if width > 60 {
		width = 60
	}

	maxVal, labelW, valW := 0.0, 0, 0
	for _, d := range in.Data {
		if d.Value > maxVal {
			maxVal = d.Value
		}
		if w := chartDispWidth(d.Label); w > labelW {
			labelW = w
		}
		if w := len(fmtNum(d.Value)); w > valW {
			valW = w
		}
	}

	var b strings.Builder
	if title := strings.TrimSpace(in.Title); title != "" {
		fmt.Fprintf(&b, "📊 **%s**\n", title)
	}
	b.WriteString("```\n")
	for _, d := range in.Data {
		frac := 0.0
		if maxVal > 0 {
			frac = d.Value / maxVal
		}
		label := d.Label + strings.Repeat(" ", labelW-chartDispWidth(d.Label))
		val := strings.Repeat(" ", valW-len(fmtNum(d.Value))) + fmtNum(d.Value)
		fmt.Fprintf(&b, "%s │ %s %s\n", label, bar(frac, width), val)
	}
	b.WriteString("```")
	return b.String()
}

// bar 用整格 █ 加 1/8 精度的局部方塊畫出 frac（0~1）比例的長條。
func bar(frac float64, width int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	total := frac * float64(width)
	full := int(total)
	s := strings.Repeat("█", full)
	if rem := total - float64(full); full < width && rem > 0 {
		eighths := []rune("▏▎▍▌▋▊▉█")
		if idx := int(rem*8) - 1; idx >= 0 {
			s += string(eighths[idx])
		}
	}
	return s // 值為 0（或相對極小）→ 空長條，如實反映
}

// fmtNum：整數值不帶小數，否則保留 1 位。
func fmtNum(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// chartDispWidth 回傳等寬字型下的顯示寬度（CJK/全形算 2 格），用於標籤對齊。
func chartDispWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && r <= 0x115F,
			r >= 0x2E80 && r <= 0x303E,
			r >= 0x3041 && r <= 0x33FF,
			r >= 0x3400 && r <= 0x4DBF,
			r >= 0x4E00 && r <= 0x9FFF,
			r >= 0xAC00 && r <= 0xD7A3,
			r >= 0xF900 && r <= 0xFAFF,
			r >= 0xFE30 && r <= 0xFE4F,
			r >= 0xFF00 && r <= 0xFF60,
			r >= 0xFFE0 && r <= 0xFFE6,
			r >= 0x20000 && r <= 0x3FFFD:
			w += 2
		default:
			w++
		}
	}
	return w
}
