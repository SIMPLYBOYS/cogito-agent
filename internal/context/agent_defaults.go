package context

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

// defaultAgentFS 是隨 binary 出貨的【功能型】具名 agent。
//
// 【為何要出貨】人設（老徐、小美…）是每個使用者自己的卡司，不該進版控。但這七個不是設定，
// 是【出貨技能的零件】：orchestrate 寫著「就一定要派 spec」「正確性→correctness」，
// 那些名字若在目標機器上不存在，委派會直接失敗（subagent.go 對載入失敗回真錯誤）。
// 版控的技能引用未版控的 agent，等於 clone 下來就是壞的。
//
// 【為何是內建而不是開機寫檔】寫檔要處理併發、要處理「使用者故意刪掉又長回來」、還要挑一個
// 知道 workspace 根目錄的呼叫點。內建 + 磁碟優先沒有這些問題：預設一定在，想改就在
// .claw/agents/ 放同名檔覆蓋掉——覆寫語意由「檔案存不存在」表達，不需要任何開關。
//
//go:embed defaultagents/*.md
var defaultAgentFS embed.FS

const defaultAgentDir = "defaultagents"

// readDefaultAgent 回傳內建 agent 的原始內容；沒有這一個則 ok=false。
func readDefaultAgent(name string) (string, bool) {
	b, err := defaultAgentFS.ReadFile(path.Join(defaultAgentDir, name+".md"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// DefaultAgentNames 列出所有內建 agent 名（去掉 .md）。匯出是給 evolve 的把關用——
// 它要判斷技能寫的 agent_type 派不派得動，而「派得動」的定義包含內建那批。
func DefaultAgentNames() []string { return defaultAgentNames() }

// defaultAgentNames 列出所有內建 agent 名（去掉 .md），供 Index 合併。
func defaultAgentNames() []string {
	entries, err := fs.ReadDir(defaultAgentFS, defaultAgentDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), ".md"); !e.IsDir() && n != e.Name() {
			names = append(names, n)
		}
	}
	return names
}
