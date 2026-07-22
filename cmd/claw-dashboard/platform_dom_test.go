package main

import (
	"bytes"
	"strings"
	"testing"
)

// 模板改壞最常見的形式是【標籤沒配對】——Go 模板不會報錯，build 也綠，只有瀏覽器版面歪掉時
// 才發現。實況：把 MCP 的動作搬進新容器時漏了一個 </div>，每個項目少一個，五個項目就歪五次。
// 這裡不做完整 HTML 剖析，只檢查開關標籤數量對稱——夠抓到這類錯，且零依賴。
func TestPlatformTmpl_TagsBalanced(t *testing.T) {
	d := platformData{
		MCPServers: []mcpServerRow{
			{Name: "alpha", Type: "stdio", Command: "npx", ArgsStr: "-y x"},
			{Name: "beta", Type: "http", URL: "https://e.example/mcp", Disabled: true,
				HasSecrets: true, HeaderKeys: []string{"Authorization"}},
			{Name: "gamma", Type: "stdio", Command: "uvx", EnvKeys: []string{"TOKEN"}, HasSecrets: true},
		},
		SecretsAllowed: true,
		Secrets:        []secretRow{{Key: "ANTHROPIC_API_KEY", Set: true}},
	}
	var b bytes.Buffer
	if err := platformTmpl.Execute(&b, d); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := b.String()

	for _, tag := range []string{"div", "details", "form", "span"} {
		open := strings.Count(out, "<"+tag)
		close := strings.Count(out, "</"+tag)
		if open != close {
			t.Errorf("<%s> 開 %d 個、關 %d 個——標籤未配對", tag, open, close)
		}
	}

	// 每個 MCP 項目應各有一組動作容器與三個動作
	for _, want := range []struct {
		frag string
		n    int
	}{
		{`class="mcpacts"`, 3},
		{`class="soft"`, 3},             // 啟用/停用（可逆，中性樣式）
		{`class="danger"`, 3},           // 移除（不可逆）
		{`class="mcpitem" id="mcp-`, 3}, // 每列錨點：POST 後捲回原位
	} {
		if got := strings.Count(out, want.frag); got != want.n {
			t.Errorf("%s 應出現 %d 次，got %d", want.frag, want.n, got)
		}
	}

	// 區塊標題錨（供 remove／secret／knobs 的 redirect 落點用）
	for _, id := range []string{`id="mcp-list"`, `id="secrets"`, `id="knobs"`} {
		if !strings.Contains(out, id) {
			t.Errorf("缺少區塊錨點 %s——redirect 會靜默失效（捲不回去也不報錯）", id)
		}
	}

	// 可逆與不可逆的確認【必須】用不同樣式：兩者長得一樣的話，danger 就不再代表「不可逆」。
	if !strings.Contains(out, `<details class="soft"><summary>停用</summary>`) {
		t.Error("停用應走 .soft（中性）兩段式確認")
	}
	if !strings.Contains(out, `<details class="danger"><summary>移除</summary>`) {
		t.Error("移除應維持 .danger（強調）兩段式確認")
	}
	// 確認訊息要講【後果】，不是只問「確定嗎」
	if !strings.Contains(out, "重啟後才生效") {
		t.Error("toggle 確認應說明延遲生效這個看不見的後果")
	}
}
