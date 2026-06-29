package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// ABRun 是 A/B 其中一邊（有記憶 / 無記憶）的單次跑分。
type ABRun struct {
	Label          string  `json:"label"`
	Passed         bool    `json:"passed"`
	TurnCount      int     `json:"turn_count"`
	ToolErrorCount int     `json:"tool_error_count"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	DurationMs     int64   `json:"duration_ms"`
}

// MemoryABReport 比較同一任務「無記憶 vs 有記憶」——量化記憶對任務的影響（Level 2）。
type MemoryABReport struct {
	Model string `json:"model"`
	Off   ABRun  `json:"off"`
	On    ABRun  `json:"on"`
}

// RunMemoryAB 在「無相關記憶」與「有相關記憶」兩種條件下各跑同一任務一次，回傳兩邊的指標。
// 記憶以一筆記錄注入 ON 那邊的 .claw/memory（其索引會進 System Prompt、recall 也可取）。
func RunMemoryAB(ctx context.Context, tc TestCase, memoryRecord, model string) *MemoryABReport {
	return &MemoryABReport{
		Model: model,
		Off:   runWithMemory(ctx, tc, "", model, "off"),
		On:    runWithMemory(ctx, tc, memoryRecord, model, "on"),
	}
}

func runWithMemory(ctx context.Context, tc TestCase, memoryRecord, model, label string) ABRun {
	start := time.Now()
	cwd, _ := os.Getwd()
	workDir := fmt.Sprintf("%s/workspace/memab_%s_%d", cwd, label, time.Now().UnixNano())
	_ = os.MkdirAll(workDir, 0o755)

	// ON：把相關記憶注入工作區（其索引會被 composer 載進 System Prompt）。
	if memoryRecord != "" {
		memDir := filepath.Join(workDir, ".claw", "memory")
		_ = os.MkdirAll(memDir, 0o755)
		_ = os.WriteFile(filepath.Join(memDir, "seed.md"), []byte(memoryRecord), 0o644)
	}

	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if err := cmd.Run(); err != nil {
			return ABRun{Label: label, Passed: false, DurationMs: time.Since(start).Milliseconds()}
		}
	}

	p := provider.NewClaudeProvider(model)
	session := ctxpkg.NewSession("memab-"+label, workDir)
	tracked := observability.NewCostTracker(p, model, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewRecallTool(workDir)) // 讓 agent 能 recall（ON 才有東西可取）

	eng := engine.NewAgentEngine(tracked, registry, false, false)
	eng.AssetsDir = workDir // 關鍵：composer 從這裡讀 .claw/memory 的索引進 System Prompt
	if tc.MaxTurns > 0 {
		eng.MaxTurns = tc.MaxTurns
	}

	rep := &benchReporter{}
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	runErr := eng.Run(ctx, session, rep)

	passed := false
	if runErr == nil && tc.ValidateScript != "" {
		cmd := exec.Command("bash", "-c", tc.ValidateScript)
		cmd.Dir = workDir
		passed = cmd.Run() == nil
	}
	return ABRun{
		Label:          label,
		Passed:         passed,
		TurnCount:      rep.turns,
		ToolErrorCount: rep.toolErrors,
		TotalCostUSD:   session.TotalCostUSD,
		DurationMs:     time.Since(start).Milliseconds(),
	}
}

// MemoryABScenario 是內建的示範情境：目標設定檔埋在巢狀目錄裡，相關記憶直接點出它的位置。
// 預期：無記憶要先到處找（多回合），有記憶直接命中（少回合）；兩邊通常都會過，差別在回合/成本。
func MemoryABScenario() (TestCase, string) {
	memoryRecord := "---\nname: settings-location\n" +
		"description: 專案設定在 conf/app/settings.ini，改版本號就編這個檔\n" +
		"tags: [專案結構]\n---\n" +
		"本專案的設定檔在 conf/app/settings.ini。要改 version 就編輯這個檔，不必到處找。\n"
	tc := TestCase{
		ID:   "mem_ab_settings",
		Name: "記憶影響 A/B：埋藏的設定檔位置",
		// 巢狀結構 + 誘餌目錄，讓「找檔」在無記憶時需要額外探索回合。
		SetupScript: `mkdir -p conf/app src lib docs internal/cmd pkg/util && ` +
			`printf 'version = 1.0\nname = demo\n' > conf/app/settings.ini && ` +
			`touch src/main.go lib/util.go docs/readme.md internal/cmd/run.go pkg/util/x.go`,
		TaskPrompt:     "把專案設定裡的 version 從 1.0 改成 2.0。改完即可。",
		ValidateScript: `grep -q 'version = 2.0' conf/app/settings.ini`,
		MaxTurns:       15,
	}
	return tc, memoryRecord
}

// Render 印出 A/B 對照與 delta。
func (r *MemoryABReport) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "記憶任務影響 A/B（Level 2） | 模型 %s\n", r.Model)
	fmt.Fprintf(&b, "%-6s %6s %8s %10s %10s\n", "條件", "通過", "回合", "工具報錯", "成本USD")
	row := func(x ABRun) {
		fmt.Fprintf(&b, "%-6s %6v %8d %10d %10.4f\n", x.Label, x.Passed, x.TurnCount, x.ToolErrorCount, x.TotalCostUSD)
	}
	row(r.Off)
	row(r.On)
	fmt.Fprintf(&b, "Δ(on−off)：回合 %+d、工具報錯 %+d、成本 %+.4f\n",
		r.On.TurnCount-r.Off.TurnCount, r.On.ToolErrorCount-r.Off.ToolErrorCount, r.On.TotalCostUSD-r.Off.TotalCostUSD)
	return b.String()
}
