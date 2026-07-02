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

// SkillABReport 比較同一任務「無技能 vs 綁定技能」——量化技能的【行為】價值（技能版的 Level 2 A/B）。
// 對稱於 MemoryABReport：結構把關（skillgate）只驗「安全/格式」，這裡驗「技能到底有沒有讓任務更好」。
type SkillABReport struct {
	Model string `json:"model"`
	Off   ABRun  `json:"off"`
	On    ABRun  `json:"on"`
}

// RunSkillAB 在「無技能」與「有技能」兩條件下各跑同一任務一次。技能以一份 SKILL.md 注入 ON 那邊的
// .claw/skills/<name>/（其索引進 System Prompt、read_skill 可載正文）。
func RunSkillAB(ctx context.Context, tc TestCase, skillDoc, skillName, model string) *SkillABReport {
	return &SkillABReport{
		Model: model,
		Off:   runWithSkill(ctx, tc, "", "", model, "off"),
		On:    runWithSkill(ctx, tc, skillDoc, skillName, model, "on"),
	}
}

func runWithSkill(ctx context.Context, tc TestCase, skillDoc, skillName, model, label string) ABRun {
	start := time.Now()
	cwd, _ := os.Getwd()
	workDir := fmt.Sprintf("%s/workspace/skillab_%s_%d", cwd, label, time.Now().UnixNano())
	_ = os.MkdirAll(workDir, 0o755)

	// ON：把技能注入工作區（其索引會被 composer 載進 System Prompt，read_skill 可載正文）。
	if skillDoc != "" && skillName != "" {
		skDir := filepath.Join(workDir, ".claw", "skills", skillName)
		_ = os.MkdirAll(skDir, 0o755)
		_ = os.WriteFile(filepath.Join(skDir, "SKILL.md"), []byte(skillDoc), 0o644)
	}

	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if err := cmd.Run(); err != nil {
			return ABRun{Label: label, Passed: false, DurationMs: time.Since(start).Milliseconds()}
		}
	}

	p := provider.NewClaudeProvider(model)
	session := ctxpkg.NewSession("skillab-"+label, workDir)
	tracked := observability.NewCostTracker(p, model, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewReadSkillTool(workDir)) // 讓 agent 能 read_skill（ON 才有技能可載）

	eng := engine.NewAgentEngine(tracked, registry, false, false)
	eng.AssetsDir = workDir // 關鍵：composer 從這裡讀 .claw/skills 的索引進 System Prompt
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

// SkillABScenario 是內建示範情境：升版本號的專案慣例是「兩個檔都要改、且一致」，但這條慣例【無法從
// 檔案內容推得】——無技能時 agent 常只改明顯的 package.json 而漏了 VERSION（驗證要求兩者皆對），
// 或需 grep 探索多回合才發現；有技能則直接照做。預期：ON 更少回合/更常通過。
func SkillABScenario() (tc TestCase, skillDoc, skillName string) {
	skillName = "version-bump"
	skillDoc = "---\nname: version-bump\n" +
		"description: 升版本號的專案慣例——版本號分散兩處，升版時兩個都要改且保持一致\n---\n" +
		"# 升版本\n本專案版本號分散在兩個檔，升版時【兩個都要改、且保持一致】，否則 CI 會擋：\n" +
		"1. 根目錄 `VERSION`（純版本號一行）。\n2. `package.json` 的 `version` 欄。\n改完確認兩者相同。\n"
	tc = TestCase{
		ID:   "skill_ab_version_bump",
		Name: "技能影響 A/B：升版本的隱藏雙檔慣例",
		SetupScript: `printf '1.0.0\n' > VERSION && ` +
			`printf '{\n  "name": "demo",\n  "version": "1.0.0"\n}\n' > package.json && ` +
			`mkdir -p src docs config && touch src/a.js docs/x.md config/y.yaml`,
		TaskPrompt:     "把專案版本從 1.0.0 升到 1.1.0。改完即可。",
		ValidateScript: `grep -q '^1.1.0' VERSION && grep -q '"version": "1.1.0"' package.json`,
		MaxTurns:       15,
	}
	return tc, skillDoc, skillName
}

// Render 印出 A/B 對照與 delta。
func (r *SkillABReport) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "技能任務影響 A/B（Level 2） | 模型 %s\n", r.Model)
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
