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
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	model := flag.String("model", "claude-haiku-4-5", "跑分使用的模型（便宜起見預設 haiku）")
	outDir := flag.String("out", "", "輸出 JSON 報告的目錄（空＝不輸出）；檔名為 bench-<unixtime>.json")
	minPassRate := flag.Float64("min-pass-rate", 0, "通過率門檻 0..1；低於則以非 0 退出碼結束（CI 用，0＝不檢查）")
	flag.Parse()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("請先在 .env 或環境變量中設置 ANTHROPIC_API_KEY 進行跑分測試")
	}

	testcases := []eval.TestCase{
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

	// 跑分是真實 API 調用、要花錢：默認選最便宜的 Claude 模型（對應書本"省點錢"的取捨）。
	// 想測更強能力可換 -model claude-opus-4-8。
	runner := eval.NewBenchmarkRunner(*model)
	report := runner.RunSuite(context.Background(), testcases)

	// 輸出 JSON 報告（供儀表板/CI artifact）。
	if *outDir != "" {
		if err := writeReport(*outDir, report); err != nil {
			log.Printf("[bench] 寫入報告失敗: %v", err)
		}
	}

	// CI 門檻：通過率低於 -min-pass-rate 即非 0 退出，讓 CI job 失敗。
	if *minPassRate > 0 && report.PassRate < *minPassRate {
		log.Printf("[bench] ❌ 通過率 %.2f%% 低於門檻 %.2f%%", report.PassRate*100, *minPassRate*100)
		os.Exit(1)
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
