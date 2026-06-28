// cmd/ingest 把一個 markdown 目錄結構式 ingest 成知識圖譜的節點與邊（確定性、不呼叫 LLM、不花錢）。
// 之後 agent 的 recall 子圖檢索就能跨這些文件做多跳關係檢索。LLM typed 關係抽取（Stage 2b）另行。
package main

import (
	"flag"
	"log"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
)

func main() {
	src := flag.String("src", "", "要 ingest 的 markdown 目錄（遞迴所有 .md）")
	root := flag.String("root", ".", "記憶根目錄：記錄寫入 <root>/.claw/memory，邊寫入 <root>/.claw/kg/edges.jsonl")
	flag.Parse()

	if *src == "" {
		log.Fatal("請用 -src 指定要 ingest 的 markdown 目錄")
	}
	nodes, edges, err := ctxpkg.NewMemoryLoader(*root).IngestDir(*src)
	if err != nil {
		log.Fatalf("ingest 失敗: %v", err)
	}
	log.Printf("✅ ingest 完成：%d 個節點、%d 條新邊 → %s/.claw/（用 recall 即可跨文件多跳檢索）", nodes, edges, *root)
}
