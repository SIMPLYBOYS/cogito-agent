package evolve

import (
	"fmt"
	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ActiveSkillsDirName 是「生效中」技能的子目錄（SkillLoader 只讀這個）。提案技能晉升即移到此處。
const ActiveSkillsDirName = "skills"

// GateResult 是把關結果。Passed=false 時 Issues 列出所有不通過原因。
type GateResult struct {
	Passed bool
	Issues []string
}

// dangerousSkillPatterns 是技能正文的安全黑名單。自生成技能是「未來行為來源」，正文若指示危險操作
// （遞迴刪除/提權/管道執行遠端腳本/fork bomb/覆寫磁碟…）或觸及憑證，一律擋下不准晉升。
// 與 slackbot.approval 的精神一致，但針對「技能正文（散文+命令）」做正則掃描。
var dangerousSkillPatterns = []struct {
	re   *regexp.Regexp
	desc string
}{
	{regexp.MustCompile(`(?i)rm\s+-[rf]`), "遞迴/強制刪除 rm -r/-f"},
	{regexp.MustCompile(`(?i)\bsudo\b`), "提權 sudo"},
	{regexp.MustCompile(`(?i)(curl|wget)[^\n|]*\|\s*(sudo\s+)?(ba)?sh`), "管道執行遠端腳本 curl|sh"},
	{regexp.MustCompile(`(?i)chmod\s+-?R?\s*777`), "過度開放權限 chmod 777"},
	{regexp.MustCompile(`:\(\)\s*\{`), "fork bomb"},
	{regexp.MustCompile(`(?i)\b(mkfs|dd\s+if=)`), "磁碟覆寫 mkfs/dd"},
	{regexp.MustCompile(`(?i)>\s*/dev/sd`), "寫入裸磁碟裝置"},
	{regexp.MustCompile(`(?i)(id_rsa|\.ssh/|\.aws/credentials|\.env\b|private key)`), "觸及憑證/機密"},
	{regexp.MustCompile(`(?i)git\s+push\s+.*--force|push\s+-f\b`), "強制 push"},
}

// rolePlayPattern 抓「派一個通用專員，再在 task_prompt 裡叫它扮演某個具名角色」。
//
// 【為何要擋】人設檔（.claw/agents/<名字>.md）就是為了這件事存在的。用扮演取代，等於每一輪
// 重新描述一次那個人（付費），而且會漂——這一輪的老王跟上一輪的老王不是同一個人。合成器會從
// 逐字稿學到這個壞習慣：實測一份自生成技能寫著「並行啟動三個 implementer…你扮演老王（UI設計）」，
// 結構與安全都乾淨，照樣通過把關，然後每一輪都重新教一次。
//
// 這不是「危險」，所以刻意【不】放進 dangerousSkillPatterns——那份黑名單只管安全，混進品質
// 判準會讓它漂掉。
var rolePlayPattern = regexp.MustCompile(`(?i)(扮演|假裝(你)?是|role-?play|pretend you)`)

// negationPattern 讓「不要叫子 agent 扮演…」這種【警告句】不被當成犯行。逐行判斷，因為一份好
// 技能本來就會把踩過的雷寫進去——擋掉它等於逼作者不准提這件事，那才是真的會漂。
var negationPattern = regexp.MustCompile(`(?i)(不要|不該|別|勿|禁止|避免|don'?t|do not|avoid|never)`)

// hasRolePlay 逐行找「叫子 agent 扮演某角色」，跳過談論它的否定句。
func hasRolePlay(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if rolePlayPattern.MatchString(line) && !negationPattern.MatchString(line) {
			return true
		}
	}
	return false
}

// agentTypePattern 抓技能正文裡指名的 agent_type（`agent_type=planner`、`"agent_type":"cto"`…）。
var agentTypePattern = regexp.MustCompile(`agent_type["']?\s*[:=]\s*["']?([\p{Han}A-Za-z0-9_-]+)`)

// KnownAgents 列出可派的 agent 名：磁碟上的 .claw/agents/*.md ⊕【隨 binary 出貨的內建】。
//
// 內建那批一定要算進來，否則把關會誤判：一台只放了人設、沒放功能型 agent 的機器上，
// agent_type=spec 明明載得到（AgentLoader 會退回內建），把關卻擋下它。實測過這個誤判。
//
// 目錄不存在【不再】回 nil：內建的永遠在，所以名單永遠非空，檢查也就永遠有效。
// （原本回 nil 是為了「寧可不檢查，也不要拿空清單把每個名字都判成錯的」——那個顧慮
// 在有內建之後消失了。）
func KnownAgents(agentsDir string) []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, e := range entries {
			if n := strings.TrimSuffix(e.Name(), ".md"); !e.IsDir() && n != e.Name() {
				add(n)
			}
		}
	}
	for _, n := range ctxpkg.DefaultAgentNames() {
		add(n)
	}
	return names
}

// Gate 對一個提案技能檔做【確定性】把關（結構 + 安全 + agent_type 存在性），無 API 呼叫。
// 這是晉升的必過關卡。agentsDir 空字串＝不檢查名稱存在性。
func Gate(skillPath, agentsDir string) (GateResult, error) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return GateResult{}, fmt.Errorf("讀取技能檔失敗: %w", err)
	}
	return GateContent(string(data), KnownAgents(agentsDir)), nil
}

// GateContent 是 Gate 的內容版：同一套判準，但不需要先落檔。
//
// 【為何需要】面板讓操作者手寫技能時，要在【寫入前】就驗——先寫再檢查等於中間存在一個
// 未把關的技能檔，而技能是「未來行為的來源」，那個空窗期不該存在。
//
// knownAgents 是 .claw/agents/ 裡真實存在的名字（用 KnownAgents 取）。傳 nil＝略過名稱檢查。
// 刻意做成【必填參數】而不是可變參數：漏傳會是編譯期錯誤，不是靜悄悄少檢查一項。
func GateContent(content string, knownAgents []string) GateResult {
	var issues []string

	name, desc, body, ok := parseFrontmatter(content)
	if !ok {
		issues = append(issues, "缺少合法 frontmatter（--- name/description ---）")
	}
	if strings.TrimSpace(name) == "" {
		issues = append(issues, "缺少 name")
	}
	if strings.TrimSpace(desc) == "" {
		issues = append(issues, "缺少 description")
	}
	if len([]rune(strings.TrimSpace(body))) < 20 {
		issues = append(issues, "正文過短（<20 字元），不像可複用流程")
	}

	// 安全掃描整份內容（含正文）。
	for _, d := range scanDangerous(content) {
		issues = append(issues, "命中危險模式："+d)
	}

	if hasRolePlay(content) {
		issues = append(issues, "叫子 agent「扮演」某個角色——該用 agent_type 指定具名 agent，人設檔已經寫好了，扮演會漂又要重複付費")
	}

	// agent_type 指到不存在的檔案，那一步的委派會直接失敗（subagent.go 對載入失敗回真錯誤）。
	// 實測一份自生成技能寫著 agent_type:"cto"/"backend"/"devops"，而實際的檔案叫
	// 老徐/阿哲/阿海——三個都落空，整批產出等於沒有。晉升前擋下來比執行時才炸便宜得多。
	if len(knownAgents) > 0 {
		known := make(map[string]bool, len(knownAgents))
		for _, a := range knownAgents {
			known[a] = true
		}
		seen := map[string]bool{}
		for _, m := range agentTypePattern.FindAllStringSubmatch(content, -1) {
			if t := m[1]; !known[t] && !seen[t] {
				seen[t] = true
				// 訊息要跟實際行為一致：subagent.go 現在對載入失敗回真錯誤（IsError），
				// 不再靜默退回探路者。所以代價不是「換了個人回答」，是這一步直接失敗、
				// orchestrator 收到錯誤觀察——晉升前擋下來仍然划算，但理由不同了。
				issues = append(issues, fmt.Sprintf("agent_type %q 在 .claw/agents/ 裡不存在——委派會直接失敗（回 error），這一步的產出等於沒有", t))
			}
		}
	}

	return GateResult{Passed: len(issues) == 0, Issues: issues}
}

// scanDangerous 回傳文字命中的危險模式描述（空＝乾淨）。供技能把關與記憶自更新共用，避免黑名單漂移。
func scanDangerous(text string) []string {
	var hits []string
	for _, p := range dangerousSkillPatterns {
		if p.re.MatchString(text) {
			hits = append(hits, p.desc)
		}
	}
	return hits
}

// Promote 把一個提案技能【資料夾】晉升到生效目錄（folder-per-skill）：先 Gate 其中的 SKILL.md，
// 通過才把整個 <proposedSkillDir> 移到 <activeBaseDir>/<資料夾名>。不過則不移、回傳原因。
func Promote(proposedSkillDir, activeBaseDir, agentsDir string) (GateResult, error) {
	res, err := Gate(filepath.Join(proposedSkillDir, SkillFileName), agentsDir)
	if err != nil {
		return res, err
	}
	if !res.Passed {
		return res, nil // 把關不過：不晉升，由呼叫方據 Issues 提示
	}
	if err := os.MkdirAll(activeBaseDir, 0o755); err != nil {
		return res, fmt.Errorf("建立生效技能目錄失敗: %w", err)
	}
	dst := filepath.Join(activeBaseDir, filepath.Base(proposedSkillDir))
	if err := os.Rename(proposedSkillDir, dst); err != nil {
		return res, fmt.Errorf("晉升（移動資料夾）失敗: %w", err)
	}
	return res, nil
}

// parseFrontmatter 從 SKILL.md 取出 name/description/body。ok=false 表示無合法 frontmatter。
func parseFrontmatter(content string) (name, desc, body string, ok bool) {
	if !strings.HasPrefix(content, "---") {
		return "", "", content, false
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) != 3 {
		return "", "", content, false
	}
	body = strings.TrimSpace(parts[2])
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, desc, body, true
}
