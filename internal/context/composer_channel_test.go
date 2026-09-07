package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 辦公室的人設寫在各頻道工作目錄的 AGENTS.md。composer 先前只讀共享根，那些檔從來沒進過 prompt
// （2026-09-07 才發現：走 cogito 的員工跟走 CLI 的一樣，全是無人設）。這裡釘死：兩份都要在、順序根在前、
// 同目錄不重複、沒設 ChannelDir 行為照舊。
func TestComposerLayersChannelAgentsMD(t *testing.T) {
	root := t.TempDir()
	ch := filepath.Join(root, "channels", "office_p19")
	if err := os.MkdirAll(ch, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT-GUIDE 全 bot 共用慣例"), 0o644)
	os.WriteFile(filepath.Join(ch, "AGENTS.md"), []byte("CHANNEL-GUIDE 你是老徐（CTO）"), 0o644)

	c := NewPromptComposer(root, "", false)
	c.ChannelDir = ch
	got := c.Build().Content
	ri, ci := strings.Index(got, "ROOT-GUIDE"), strings.Index(got, "CHANNEL-GUIDE")
	if ri < 0 || ci < 0 {
		t.Fatalf("兩份 AGENTS.md 都該進 prompt：root=%d channel=%d", ri, ci)
	}
	if ci < ri {
		t.Fatalf("共享根的慣例該在頻道人設之前：root=%d channel=%d", ri, ci)
	}

	// 沒設 ChannelDir：只有根，行為照舊
	if got := NewPromptComposer(root, "", false).Build().Content; strings.Contains(got, "CHANNEL-GUIDE") {
		t.Fatal("沒設 ChannelDir 不該讀頻道的 AGENTS.md")
	}
	// ChannelDir 就是根（CLI／demo 單一目錄）：不重複
	same := NewPromptComposer(root, "", false)
	same.ChannelDir = root + string(filepath.Separator) + "."
	if n := strings.Count(same.Build().Content, "ROOT-GUIDE"); n != 1 {
		t.Fatalf("同目錄不該重複讀，出現 %d 次", n)
	}
}
