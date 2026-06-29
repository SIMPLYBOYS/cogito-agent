// cmd/bench 是自動化評測入口：把累積的全部能力當黑盒，用
// SetupScript→TaskPrompt→ValidateScript 三段式定義測試，以 bash exit code 判對錯，
// 跑完輸出通過率 + 總花費。與 cmd/claw 等入口並存。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/eval"
	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	model := flag.String("model", "claude-haiku-4-5", "跑分使用的模型（便宜起見預設 haiku）")
	outDir := flag.String("out", "", "輸出 JSON 報告的目錄（空＝不輸出）；檔名為 bench-<unixtime>.json")
	minPassRate := flag.Float64("min-pass-rate", 0, "通過率門檻 0..1；低於則以非 0 退出碼結束（CI 用，0＝不檢查）")
	reflexion := flag.Int("reflexion", 1, "Reflexion 重試上限：>1 時用例失敗會反思出教訓、帶教訓重試（每次重試多花 API）")
	tune := flag.Bool("tune", false, "依跑分結果產出調參提案（寫入 workspace/.claw/config.proposed.json，不自動套用）")
	swebench := flag.String("swebench", "", "SWE-bench 資料檔(JSONL/JSON)路徑；設了就改跑 SWE-bench 實例而非內建用例")
	limit := flag.Int("limit", 0, "只取前 N 個 SWE-bench 實例（0=全部）")
	sweEnvSetup := flag.String("swe-env-setup", "", "每個 SWE-bench 實例的環境安裝 bash（各 repo 不同；正式跑常用官方 Docker 映像，依賴已備時可留空）")
	sweRepoPrefix := flag.String("swe-repo-prefix", "", "clone 來源前綴，覆蓋預設 https://github.com/（可指向本地鏡像快取或本地 repo 加速/離線）")
	sweTestRunner := flag.String("swe-test-runner", "", "覆蓋驗證階段的測試命令前綴（預設 python -m pytest -q；如 django 用 tests/runtests.py、或指向 venv 內的 python）")
	dryRun := flag.Bool("dry-run", false, "只載入並印出將執行的用例計畫（Setup/Task/Validate），不呼叫 LLM、不 clone、不花錢")
	memAB := flag.Bool("mem-ab", false, "記憶任務影響 A/B：同一任務在『無/有相關記憶』下各跑一次，比較回合/成本（需 ANTHROPIC_API_KEY）")
	predictions := flag.String("predictions", "", "SWE-bench 生成模式：對 -swebench 的實例跑 agent → 輸出官方 predictions JSONL 到此路徑（交給官方 harness 評測）")
	flag.Parse()

	// SWE-bench 生成模式：產出官方 predictions（model_patch=agent 的 git diff），不自己評測。
	if *predictions != "" {
		if *swebench == "" {
			log.Fatal("-predictions 需搭配 -swebench <資料檔>")
		}
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			log.Fatal("生成 predictions 需 ANTHROPIC_API_KEY")
		}
		instances, err := eval.LoadSWEBench(*swebench)
		if err != nil {
			log.Fatalf("載入 SWE-bench 失敗: %v", err)
		}
		if *limit > 0 && *limit < len(instances) {
			instances = instances[:*limit]
		}
		opts := eval.SWEOptions{RepoURLPrefix: *sweRepoPrefix}
		var preds []eval.Prediction
		for i, ins := range instances {
			log.Printf("[swebench-gen] (%d/%d) 生成 %s …", i+1, len(instances), ins.InstanceID)
			p, err := eval.GeneratePrediction(context.Background(), ins, opts, *model)
			if err != nil {
				log.Printf("[swebench-gen] %s 失敗（仍續）: %v", ins.InstanceID, err)
				continue
			}
			preds = append(preds, p)
		}
		if err := eval.WritePredictions(*predictions, preds); err != nil {
			log.Fatalf("寫 predictions 失敗: %v", err)
		}
		log.Printf("✅ 已生成 %d 筆 predictions → %s（交給官方 harness：見 docs/swebench-runbook.md）", len(preds), *predictions)
		return
	}

	// 記憶 Level 2 A/B：用內建情境跑「無記憶 vs 有記憶」，量化記憶對任務的影響。
	if *memAB {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			log.Fatal("記憶 A/B 需 ANTHROPIC_API_KEY")
		}
		tc, mem := eval.MemoryABScenario()
		fmt.Print(eval.RunMemoryAB(context.Background(), tc, mem, *model).Render())
		return
	}

	testcases, err := loadTestCases(*swebench, *sweEnvSetup, *sweRepoPrefix, *sweTestRunner, *limit)
	if err != nil {
		log.Fatalf("載入測試用例失敗: %v", err)
	}

	// dry-run：離線檢視 harness 接線（不需 API key、不 clone、不花錢）。
	if *dryRun {
		printPlan(testcases)
		return
	}

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY 進行跑分測試")
	}

	// 跑分是真實 API 調用、要花錢：默認選最便宜的 Claude 模型（對應書本"省點錢"的取捨）。
	// 想測更強能力可換 -model claude-opus-4-8。
	runner := eval.NewBenchmarkRunner(*model)
	runner.MaxAttempts = *reflexion // >1 啟用 Reflexion 反思重試
	report := runner.RunSuite(context.Background(), testcases)

	// 輸出 JSON 報告（供儀表板/CI artifact）。
	if *outDir != "" {
		if err := writeReport(*outDir, report); err != nil {
			log.Printf("[bench] 寫入報告失敗: %v", err)
		}
	}

	// 參數自調（Tier 4 #15）：依跑分聚合指標產出【提案參數】到暫存區，不自動套用。
	if *tune {
		emitTuningProposals(report)
	}

	// CI 門檻：通過率低於 -min-pass-rate 即非 0 退出，讓 CI job 失敗。
	if *minPassRate > 0 && report.PassRate < *minPassRate {
		log.Printf("[bench] ❌ 通過率 %.2f%% 低於門檻 %.2f%%", report.PassRate*100, *minPassRate*100)
		os.Exit(1)
	}
}

// loadTestCases 回傳要跑的用例：未指定 -swebench 時用內建煙霧用例；指定時載入 SWE-bench 實例並轉換。
func loadTestCases(swebenchPath, envSetup, repoPrefix, testRunner string, limit int) ([]eval.TestCase, error) {
	if swebenchPath == "" {
		return builtinTestCases(), nil
	}
	instances, err := eval.LoadSWEBench(swebenchPath)
	if err != nil {
		return nil, err
	}
	cases := eval.SWEToTestCases(instances, eval.SWEOptions{EnvSetup: envSetup, RepoURLPrefix: repoPrefix, TestRunner: testRunner}, limit)
	log.Printf("[bench] 已從 %s 載入 %d 個 SWE-bench 實例（取 %d 個）", swebenchPath, len(instances), len(cases))
	return cases, nil
}

// builtinTestCases 是不指定 -swebench 時的內建煙霧測試（驗工具/代碼生成的基本能力）。
func builtinTestCases() []eval.TestCase {
	return []eval.TestCase{
		{
			ID:             "test_001_edit",
			Name:           "測試模糊替換工具的準確性",
			SetupScript:    `echo '{"name": "tiny-claw", "version": "v1.0.0"}' > config.json`,
			TaskPrompt:     `當前目錄下有一個 config.json。請你使用 edit_file 工具，將其中的 version 從 v1.0.0 改為 v2.0.0。不要做其他多餘操作。`,
			ValidateScript: `grep '"version": "v2.0.0"' config.json`,
		},
		{
			ID:   "test_002_code_gen",
			Name: "測試代碼閱讀與創建新文件的綜合能力",
			// 用 printf 而非 echo：macOS 的 bash echo 默認不解釋 \n，會寫出字面量導致 math.go 損壞
			SetupScript:    `printf 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}\n' > math.go`,
			TaskPrompt:     `當前目錄下有一個 math.go。請你仔細閱讀它，然後在同級目錄下，幫我寫一個規範的單元測試文件 math_test.go，用來測試 Multiply 函數。請務必包含正常的測試用例。`,
			ValidateScript: `go mod init bench && go test -v ./...`,
		},
	}
}

// printPlan 離線印出每個用例的三段式計畫，供檢視 harness 接線（不執行、不花錢）。
func printPlan(cases []eval.TestCase) {
	fmt.Printf("== Dry-run：%d 個用例（不呼叫 LLM、不 clone、不花錢）==\n", len(cases))
	for i, tc := range cases {
		fmt.Printf("\n[%d] %s — %s\n", i+1, tc.ID, tc.Name)
		fmt.Printf("--- Setup ---\n%s\n", tc.SetupScript)
		fmt.Printf("--- Task（前 400 字）---\n%s\n", truncatePlan(tc.TaskPrompt, 400))
		fmt.Printf("--- Validate ---\n%s\n", tc.ValidateScript)
	}
}

func truncatePlan(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// emitTuningProposals 從跑分報告算出聚合指標，產出有界的調參提案到暫存區（不自動套用）。
func emitTuningProposals(report *eval.SuiteReport) {
	// current = 引擎預設（internal/engine/loop.go：defaultMaxTurns/ConcurrentTools/CostUSD）。
	cur := evolve.Knobs{MaxTurns: 40, MaxConcurrentTools: 5, MaxCostUSD: 1.0}

	n := len(report.Results)
	if n == 0 {
		return
	}
	var sumTurns, sumErr, maxCost float64
	ceilingHits := 0
	for _, r := range report.Results {
		sumTurns += float64(r.TurnCount)
		sumErr += float64(r.ToolErrorCount)
		if r.TotalCostUSD > maxCost {
			maxCost = r.TotalCostUSD
		}
		if !r.Passed && r.TurnCount >= cur.MaxTurns {
			ceilingHits++
		}
	}
	stats := evolve.RunStats{
		N: n, PassRate: report.PassRate,
		MeanTurns: sumTurns / float64(n), MeanToolErrors: sumErr / float64(n),
		MaxCaseCostUSD: maxCost, CeilingHitRate: float64(ceilingHits) / float64(n),
	}

	proposals := evolve.Advise(stats, cur)
	if len(proposals) == 0 {
		log.Printf("[tune] 目前參數看起來合適，無調參提案")
		return
	}
	for _, p := range proposals {
		tag := ""
		if p.Sensitive {
			tag = "  ⚠️敏感（放寬安全旋鈕）"
		}
		log.Printf("[tune] %s: %v → %v%s（%s）", p.Knob, p.Current, p.Proposed, tag, p.Reason)
	}
	if path, err := evolve.WriteProposedConfig(filepath.Join("workspace", ".claw"), cur, proposals); err != nil {
		log.Printf("[tune] 寫入提案失敗: %v", err)
	} else {
		log.Printf("[tune] 📄 調參提案已寫入 %s（須人工 review 後手動套用，不會自動生效）", path)
	}
}

func writeReport(dir string, report *eval.SuiteReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("bench-%d.json", time.Now().Unix()))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	log.Printf("[bench] 📄 報告已寫入 %s", path)
	return nil
}
