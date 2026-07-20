package evolve

import (
	"fmt"
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

// Gate 對一個提案技能檔做【確定性】把關（結構 + 安全），無 API 呼叫。這是晉升的必過關卡。
func Gate(skillPath string) (GateResult, error) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return GateResult{}, fmt.Errorf("讀取技能檔失敗: %w", err)
	}
	return GateContent(string(data)), nil
}

// GateContent 是 Gate 的內容版：同一套判準，但不需要先落檔。
//
// 【為何需要】面板讓操作者手寫技能時，要在【寫入前】就驗——先寫再檢查等於中間存在一個
// 未把關的技能檔，而技能是「未來行為的來源」，那個空窗期不該存在。
func GateContent(content string) GateResult {
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
func Promote(proposedSkillDir, activeBaseDir string) (GateResult, error) {
	res, err := Gate(filepath.Join(proposedSkillDir, SkillFileName))
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
