// cmd/bench 是 ch20 的自动化评测入口：把 ch05-ch19 累积的全部能力当黑盒，用
// SetupScript→TaskPrompt→ValidateScript 三段式定义测试，以 bash exit code 判对错，
// 跑完输出通过率 + 总花费。与 cmd/claw 等入口并存。
package main

import (
	"context"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/yourname/go-tiny-claw/internal/eval"
)

func main() {
	_ = godotenv.Load()
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Fatal("请先在 .env 或环境变量中设置 ANTHROPIC_API_KEY 进行跑分测试")
	}

	testcases := []eval.TestCase{
		{
			ID:             "test_001_edit",
			Name:           "测试模糊替换工具的准确性",
			SetupScript:    `echo '{"name": "tiny-claw", "version": "v1.0.0"}' > config.json`,
			TaskPrompt:     `当前目录下有一个 config.json。请你使用 edit_file 工具，将其中的 version 从 v1.0.0 改为 v2.0.0。不要做其他多余操作。`,
			ValidateScript: `grep '"version": "v2.0.0"' config.json`,
		},
		{
			ID:   "test_002_code_gen",
			Name: "测试代码阅读与创建新文件的综合能力",
			// 用 printf 而非 echo：macOS 的 bash echo 默认不解释 \n，会写出字面量导致 math.go 损坏
			SetupScript:    `printf 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}\n' > math.go`,
			TaskPrompt:     `当前目录下有一个 math.go。请你仔细阅读它，然后在同级目录下，帮我写一个规范的单元测试文件 math_test.go，用来测试 Multiply 函数。请务必包含正常的测试用例。`,
			ValidateScript: `go mod init bench && go test -v ./...`,
		},
	}

	// 跑分是真实 API 调用、要花钱：默认选最便宜的 Claude 模型（对应书本"省点钱"的取舍）。
	// 想测更强能力可换 claude-opus-4-8。
	runner := eval.NewBenchmarkRunner("claude-haiku-4-5")
	runner.RunSuite(context.Background(), testcases)
}
