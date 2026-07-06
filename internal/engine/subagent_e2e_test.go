//go:build llm_e2e

// LLM 端到端整合測試——【預設不跑】。因走真實 LLM（不確定性 + 花費），用 build tag 排除在
// 一般 go test ./... 之外，只在明確要驗收 subagent-team 能力時手動跑：
//
//	go test -tags llm_e2e -run TestSubagentTeamE2E ./internal/engine/
//
// 無 ANTHROPIC_API_KEY 則 skip（CI 沒 key 也不會紅）。
package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
	"github.com/joho/godotenv"
)

// subagentE2ERig 在一個【git repo】臨時工作區裡裝好：實作型 implementer agent 定義
// （model + effort:high + isolation:worktree + 可寫工具）、主引擎、接了 worktree 隔離的 spawn_subagent。
// 回傳主引擎、session 與 base 目錄。
func subagentE2ERig(t *testing.T) (*AgentEngine, *ctxpkg.Session, string) {
	t.Helper()
	base := t.TempDir()
	git := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", base}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")

	agentsDir := filepath.Join(base, ".claw", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "implementer.md"),
		[]byte("---\nname: implementer\ndescription: 實作\nmodel: claude-haiku-4-5\neffort: high\ntools: [read_file, bash, write_file, edit_file]\nisolation: worktree\n---\n你是實作工程師。只做被交辦的一件事：用 write_file 建立指定檔案，完成後回報。"), 0o644); err != nil {
		t.Fatal(err)
	}

	buildSubReg := func(wd string) tools.Registry {
		r := tools.NewRegistry()
		r.Register(tools.NewReadFileTool(wd))
		r.Register(tools.NewBashTool(wd))
		r.Register(tools.NewWriteFileTool(wd))
		r.Register(tools.NewEditFileTool(wd))
		return r
	}
	main := tools.NewRegistry()
	main.Register(tools.NewReadFileTool(base))
	main.Register(tools.NewBashTool(base))
	eng := NewAgentEngine(provider.NewClaudeProvider("claude-haiku-4-5"), main, false, false)
	main.Register(tools.NewSubagentTool(eng, buildSubReg(base), NewTerminalReporter(), base).
		WithWorktreeIsolation(base, buildSubReg))

	sess := ctxpkg.NewSession("subagent-e2e", base)
	return eng, sess, base
}

// 驗收：並行派兩個 isolation:worktree 的 implementer，各寫一個不同檔——兩個檔都應完整 apply 回 base，
// 無相互覆蓋。這一個用例即涵蓋：具名派發、可寫、worktree 隔離、並行無覆蓋、diff 回寫。
// （選模型/effort 的參數傳遞由 tools 套件的單元測試 TestSubagent_ModelAndEffort 覆蓋。）
func TestSubagentTeamE2E_ParallelIsolatedWrites(t *testing.T) {
	_ = godotenv.Load("../../.env")
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("需 ANTHROPIC_API_KEY（LLM 整合測試）")
	}

	eng, sess, base := subagentE2ERig(t)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: `請在同一則回覆中【同時發起兩個】spawn_subagent，都用 agent_type="implementer"：` +
		"\n第一個：建立 alpha.txt，內容正好是 AAA。" +
		"\n第二個：建立 beta.txt，內容正好是 BBB。" +
		"\n一定要委派兩個，不要自己直接寫。"})

	if err := eng.Run(context.Background(), sess, NewTerminalReporter()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 關鍵斷言：兩個檔都在 base 且內容正確＝並行隔離無覆蓋 + 兩份 diff 都序列化回寫成功。
	a, aerr := os.ReadFile(filepath.Join(base, "alpha.txt"))
	if aerr != nil || !strings.Contains(string(a), "AAA") {
		t.Fatalf("alpha.txt 應存在且含 AAA（隔離/回寫失敗或被覆蓋）: err=%v 內容=%q", aerr, string(a))
	}
	b, berr := os.ReadFile(filepath.Join(base, "beta.txt"))
	if berr != nil || !strings.Contains(string(b), "BBB") {
		t.Fatalf("beta.txt 應存在且含 BBB（隔離/回寫失敗或被覆蓋）: err=%v 內容=%q", berr, string(b))
	}
}
