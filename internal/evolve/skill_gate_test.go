package evolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodSkill = `---
name: run-go-tests
description: 當需要驗證 Go 變更時
---
1. 先 go build ./...
2. 再 go test ./...
3. 失敗就讀錯誤訊息逐一修`

func TestGate_GoodSkillPasses(t *testing.T) {
	p := writeSkillFile(t, t.TempDir(), "s.md", goodSkill)
	res, err := Gate(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Errorf("良好技能應通過，issues=%v", res.Issues)
	}
}

func TestGate_DangerousBodyRejected(t *testing.T) {
	bad := "---\nname: clean\ndescription: 清理\n---\n為了清乾淨，執行 `sudo rm -rf /tmp/*` 然後重來。"
	p := writeSkillFile(t, t.TempDir(), "s.md", bad)
	res, _ := Gate(p, "")
	if res.Passed {
		t.Fatal("含 rm -rf / sudo 的技能必須被擋")
	}
	joined := strings.Join(res.Issues, " ")
	if !strings.Contains(joined, "危險") {
		t.Errorf("應標記危險模式，got %v", res.Issues)
	}
}

// 這段正文是【真的】從一份自生成技能抄來的（run-multiparty-async-decision-review）。結構與
// 安全都乾淨，所以它一路通過把關、晉升生效，然後每一輪都重新教一次「派 implementer 去假裝老王」。
func TestGate_RolePlayRejected(t *testing.T) {
	bad := "---\nname: multi\ndescription: 多角色審視\n---\n" +
		"用 spawn_subagent 背景並行啟動三個 implementer：`bg-1: 你扮演老王（UI設計），逐項評視覺密度`"
	p := writeSkillFile(t, t.TempDir(), "s.md", bad)
	res, _ := Gate(p, "")
	if res.Passed {
		t.Fatal("叫子 agent 扮演具名角色必須被擋——人設檔就是為了這件事存在的")
	}
	if !strings.Contains(strings.Join(res.Issues, " "), "扮演") {
		t.Errorf("應標記扮演，got %v", res.Issues)
	}

	// 反面：好技能本來就會把踩過的雷寫進去。擋掉警告句等於逼作者不准提這件事。
	warn := "---\nname: multi\ndescription: 多角色審視\n---\n" +
		"派 `老王` 取得意見。⚠️ 不要派 implementer 再叫它「扮演」老王——人設會漂又要重複付費。"
	if r := GateContent(warn, nil); !r.Passed {
		t.Errorf("警告句不是犯行，不該被擋：%v", r.Issues)
	}
}

// 也是真實案例（parallel-expert-eval-and-merge）：它派 cto/backend/devops，而實際的人設檔叫
// 老徐/阿哲/阿海。三個都載入不到，委派會直接失敗（subagent.go 對載入失敗回真錯誤），整批產出等於沒有。
func TestGateContent_UnknownAgentTypeRejected(t *testing.T) {
	body := "---\nname: panel\ndescription: 多方評估\n---\n" +
		`spawn_subagent {"agent_type":"cto","background":true}` + "\n" +
		`spawn_subagent(agent_type=老徐)`
	res := GateContent(body, []string{"老徐", "阿哲", "implementer"})
	if res.Passed {
		t.Fatal("agent_type 指到不存在的檔案必須被擋")
	}
	joined := strings.Join(res.Issues, " ")
	if !strings.Contains(joined, `"cto"`) {
		t.Errorf("應點名 cto，got %v", res.Issues)
	}
	if strings.Contains(joined, "老徐") {
		t.Errorf("老徐存在，不該被判成錯的：%v", res.Issues)
	}
	// 不知道名冊（傳 nil）就整項略過——寧可不檢查，也不要把每個名字都誤判成錯的。
	if r := GateContent(body, nil); !r.Passed {
		t.Errorf("沒有名冊時不該憑空判錯：%v", r.Issues)
	}
}

func TestGate_MissingFrontmatterAndShortBody(t *testing.T) {
	p := writeSkillFile(t, t.TempDir(), "s.md", "就這樣")
	res, _ := Gate(p, "")
	if res.Passed {
		t.Error("無 frontmatter + 正文過短應不通過")
	}
}

func TestPromote_MovesOnPass(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "skills-proposed", "run-go-tests")
	active := filepath.Join(base, "skills")
	writeSkillFile(t, skillDir, "SKILL.md", goodSkill)

	res, err := Promote(skillDir, active, "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("應通過並晉升，issues=%v", res.Issues)
	}
	// 原資料夾應已移走、新資料夾應在 active
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Error("晉升後提案資料夾應已移走")
	}
	if _, err := os.Stat(filepath.Join(active, "run-go-tests", "SKILL.md")); err != nil {
		t.Error("晉升後應出現在 active/<name>/SKILL.md")
	}
}

func TestPromote_RefusesOnFail(t *testing.T) {
	base := t.TempDir()
	skillDir := filepath.Join(base, "skills-proposed", "x")
	active := filepath.Join(base, "skills")
	bad := "---\nname: x\ndescription: d\n---\nsudo rm -rf /"
	writeSkillFile(t, skillDir, "SKILL.md", bad)

	res, _ := Promote(skillDir, active, "")
	if res.Passed {
		t.Fatal("危險技能不該晉升")
	}
	// 原資料夾應仍在、active 不該有
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Error("把關不過時提案資料夾應保留原處")
	}
	if _, err := os.Stat(filepath.Join(active, "x")); !os.IsNotExist(err) {
		t.Error("把關不過時不該出現在 active")
	}
}

// 內建 agent 不可被把關誤判成「不存在」。
//
// 這條守的是一個實際發生過的誤判：功能型 agent 改成隨 binary 出貨（AgentLoader 磁碟找不到
// 就退回內建）之後，一台只放了人設的機器上，agent_type=spec 明明派得動，把關卻擋下它——
// 於是引用內建 agent 的技能永遠晉升不了。
//
// 同時守反向：退回機制不能把「打錯字」變成靜默成功。
func TestKnownAgents_IncludesBuiltins(t *testing.T) {
	dir := t.TempDir() // 完全沒有 agent 檔
	known := KnownAgents(dir)
	if len(known) == 0 {
		t.Fatal("內建 agent 一定在，名單不該是空的")
	}
	has := map[string]bool{}
	for _, n := range known {
		has[n] = true
	}
	if !has["spec"] {
		t.Errorf("內建的 spec 不在可派名單裡：%v", known)
	}

	const usesSpec = "---\nname: t\ndescription: d\n---\n派審查 agent_type=spec 完成後回報結果給主幹"
	if res := GateContent(usesSpec, known); !res.Passed {
		t.Errorf("引用內建 agent 應通過，實際：%v", res.Issues)
	}

	const typo = "---\nname: t\ndescription: d\n---\n派審查 agent_type=speeec 完成後回報結果給主幹"
	if res := GateContent(typo, known); res.Passed {
		t.Error("打錯的 agent_type 仍要被擋——退回機制不能把錯字變成靜默成功")
	}
}
