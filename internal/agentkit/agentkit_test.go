package agentkit

import (
	"context"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

func toolNames(r tools.Registry) map[string]bool {
	m := map[string]bool{}
	for _, d := range r.GetAvailableTools() {
		m[d.Name] = true
	}
	return m
}

func TestRegisterCoreTools(t *testing.T) {
	r := tools.NewRegistry()
	RegisterCoreTools(r, t.TempDir(), t.TempDir(), sandbox.FromEnv())
	names := toolNames(r)
	for _, want := range []string{"read_file", "write_file", "bash", "edit_file", "read_skill", "recall", "bar_chart"} {
		if !names[want] {
			t.Errorf("核心工具集缺 %q（有：%v）", want, names)
		}
	}
}

type stubRunner struct{}

func (stubRunner) RunSub(context.Context, tools.SubTask) (string, error) { return "", nil }

func TestWireSubagent(t *testing.T) {
	dir := t.TempDir()
	main := tools.NewRegistry()

	var extraCalled bool
	WireSubagent(main, stubRunner{}, dir, SubagentOpts{
		Executor:      sandbox.FromEnv(),
		SkillsBaseDir: dir,
		ExtraSubTools: func(r tools.Registry) { extraCalled = true }, // 子 registry 加料鉤子有被呼叫
	})
	names := toolNames(main)
	if !names["spawn_subagent"] {
		t.Error("WireSubagent 應把 spawn_subagent 註冊進主 registry")
	}
	if !names["subagent_result"] || !names["subagent_list"] {
		t.Error("應註冊背景委派查詢工具 subagent_result / subagent_list")
	}
	if !extraCalled {
		t.Error("ExtraSubTools 應被套用到子 registry（建 baseWorkDir 子池時）")
	}
}

func TestRegisterMCPTools_NilSafe(t *testing.T) {
	r := tools.NewRegistry()
	RegisterMCPTools(r, nil) // 無 gateway → no-op、不炸
	if len(r.GetAvailableTools()) != 0 {
		t.Error("nil gateway 不該註冊任何工具")
	}
}
