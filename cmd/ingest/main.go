// cmd/ingest 把 markdown 目錄 ingest 成知識圖譜，並可選地用 LLM 抽 typed 關係（gated）。
//
//	-src X            結構式 ingest（確定性、不花錢）：md → 節點 + edges.jsonl
//	-llm             對 root 內節點跑 LLM typed 關係抽取 → 寫【提案邊】（需 ANTHROPIC_API_KEY）
//	-review-edges    印出待審的提案邊
//	-apply-edges     提案邊過 gate（信心/幻覺/去重/封頂）後併入生效的 edges.jsonl
//
// 之後 agent 的 recall 子圖檢索即可跨文件、沿 typed 關係做多跳推理。
package main

import (
	"context"
	"flag"
	"log"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/joho/godotenv"
)

func main() {
	src := flag.String("src", "", "結構式 ingest 的 markdown 目錄（遞迴所有 .md）")
	root := flag.String("root", ".", "記憶根目錄：記錄→<root>/.claw/memory，邊→<root>/.claw/kg/edges.jsonl")
	llm := flag.Bool("llm", false, "對 root 內記憶節點跑 LLM typed 關係抽取 → 寫提案邊（需 ANTHROPIC_API_KEY）")
	reviewEdges := flag.Bool("review-edges", false, "印出待審的提案邊")
	applyEdges := flag.Bool("apply-edges", false, "提案邊過 gate 後併入 edges.jsonl")
	model := flag.String("model", "claude-haiku-4-5", "LLM 抽取用的模型")
	flag.Parse()

	switch {
	case *reviewEdges:
		edges := ctxpkg.ReadProposedEdges(*root)
		if len(edges) == 0 {
			log.Println("（目前沒有提案邊）")
			return
		}
		log.Printf("待審提案邊（%d 條）：", len(edges))
		for _, e := range edges {
			log.Printf("  %s —%s→ %s  (conf %.2f, %s)", e.From, e.Type, e.To, e.Confidence, e.Source)
		}

	case *applyEdges:
		applied, rejected, err := ctxpkg.ApplyProposedEdges(*root)
		if err != nil {
			log.Fatalf("套用提案邊失敗: %v", err)
		}
		log.Printf("✅ 提案邊過 gate：併入 %d 條、拒絕 %d 條 → %s/.claw/kg/edges.jsonl", applied, rejected, *root)

	case *llm:
		_ = godotenv.Load()
		n, err := evolve.NewRelationExtractor(provider.NewClaudeProvider(*model), *root).Extract(context.Background())
		if err != nil {
			log.Fatalf("LLM 關係抽取失敗: %v", err)
		}
		log.Printf("✅ LLM 抽出 %d 條提案邊 → 用 -review-edges 檢視、-apply-edges 過 gate 併入", n)

	case *src != "":
		nodes, edges, err := ctxpkg.NewMemoryLoader(*root).IngestDir(*src)
		if err != nil {
			log.Fatalf("ingest 失敗: %v", err)
		}
		log.Printf("✅ ingest 完成：%d 個節點、%d 條新邊 → %s/.claw/（用 recall 即可跨文件多跳檢索）", nodes, edges, *root)

	default:
		log.Fatal("請指定 -src（結構 ingest）/ -llm / -review-edges / -apply-edges 其一")
	}
}
