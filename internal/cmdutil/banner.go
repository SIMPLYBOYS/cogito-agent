package cmdutil

import (
	"fmt"
	"os"
	"strings"
)

// bannerFont：5×7 點陣字（'1'＝亮像素），只含 COGITO-AGENT 需要的字元（與 web logo 同一套）。
var bannerFont = map[rune][7]string{
	'C': {"11111", "10000", "10000", "10000", "10000", "10000", "11111"},
	'O': {"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01111"},
	'I': {"11111", "00100", "00100", "00100", "00100", "00100", "11111"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'N': {"10001", "11001", "10101", "10101", "10011", "10001", "10001"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
}

// bannerColors：7 列由上到下的漸層色（琥珀金 → 鏽紅），對齊 web logo 的漸層。
var bannerColors = [7][3]int{
	{247, 208, 96}, {243, 181, 85}, {239, 155, 74}, {236, 135, 74},
	{232, 115, 74}, {205, 95, 62}, {178, 74, 50},
}

const bannerText = "COGITO-AGENT"

// bannerLines 組出 7 列 █/空白 的 ASCII 方塊字（純文字、可測；不含顏色）。
func bannerLines() []string {
	lines := make([]string, 7)
	for r := range 7 {
		var sb strings.Builder
		for _, ch := range bannerText {
			g, ok := bannerFont[ch]
			if !ok {
				sb.WriteString("      ")
				continue
			}
			for _, bit := range g[r] {
				if bit == '1' {
					sb.WriteString("█")
				} else {
					sb.WriteString(" ")
				}
			}
			sb.WriteString(" ") // 字間 1 空欄
		}
		lines[r] = strings.TrimRight(sb.String(), " ")
	}
	return lines
}

// renderBanner 組出完整 banner 字串。color=true 加 24-bit ANSI 真彩色（逐列上漸層）。
func renderBanner(color bool) string {
	var b strings.Builder
	b.WriteString("\n")
	for i, ln := range bannerLines() {
		if color {
			c := bannerColors[i]
			fmt.Fprintf(&b, "  \x1b[1;38;2;%d;%d;%dm%s\x1b[0m\n", c[0], c[1], c[2], ln)
		} else {
			b.WriteString("  ")
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if color {
		fmt.Fprintf(&b, "\n  \x1b[38;2;239;154;74mcogito, ergo ago\x1b[0m   \x1b[38;2;138;117;102mreason · act · observe · self-hosted ReAct agent\x1b[0m\n\n")
	} else {
		b.WriteString("\n  cogito, ergo ago   reason · act · observe · self-hosted ReAct agent\n\n")
	}
	return b.String()
}

// PrintBanner 在啟動時印出 logo。非終端（管線 / 重導向到日誌檔）自動不印，避免污染輸出；
// 遵循 NO_COLOR 慣例（設了則印無色版）。
func PrintBanner() {
	if !isTTY() {
		return
	}
	fmt.Print(renderBanner(os.Getenv("NO_COLOR") == ""))
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
