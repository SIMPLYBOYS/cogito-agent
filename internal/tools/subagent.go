package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// AgentRunner 定義引擎向工具層暴露的"拉起子 agent"能力。介面定義在 tools 包（使用端），
// 這樣 tools 不需要 import engine，避免迴圈依賴；*engine.AgentEngine 靠 duck typing 滿足它。
// skillBody 為可選的「綁定技能正文」：非空時注入子 agent 的隔離 context，主 context 不被汙染。
// SubTask 是一次子 agent 委派的完整參數（用 struct 而非長 positional 參數，好擴充）。
type SubTask struct {
	Prompt       string
	SkillBody    string
	SystemPrompt string
	Name         string   // agent_type（具名 agent）；用於進度事件前綴 [Subagent:<Name>]，讓並行子 agent 的事件可歸屬到各自的卡
	Model        string   // 空＝沿用主引擎模型
	MaxTokens    int      // <=0＝用預設；供 effort 調整輸出上限
	Registry     Registry // 子 agent 可用的工具（已 Subset / 已 rooted 在隔離目錄）
	Reporter     interface{}
}

type AgentRunner interface {
	RunSub(ctx context.Context, task SubTask) (string, error)
}

// effortToMaxTokens 把 agent 的 effort（low/medium/high）映射成輸出 token 上限——思考深度的粗略代理
// （非 extended-thinking）。未知/空＝0（用 provider 預設 4096）。
func effortToMaxTokens(effort string) int {
	switch effort {
	case "low":
		return 2048
	case "medium":
		return 4096
	case "high":
		return 8192
	default:
		return 0
	}
}

// SubagentTool 是一個標準 BaseTool：主 agent 呼叫它來派出一個受限的探索子 agent，
// 子 agent 在隔離的上下文裡跑完，只回傳一段精煉報告——主 agent 的 session 不被搜索過程汙染。
// 可選地綁定一個技能（skill 參數）：該技能的完整正文只會載入子 agent 的隔離 context。
// defaultSubagentTools 是未指定 agent_type（或具名 agent 未宣告 tools）時的預設工具集——維持唯讀
// 探索，不含 write/edit。寫入能力對子 agent 是 opt-in：只有具名 agent 在其 tools 明確宣告才給，
// 且照樣過審批 middleware。這保證「預設探路者永遠動不了檔」，即便傳入的註冊表是含寫入的超集。
var defaultSubagentTools = []string{"read_file", "bash"}

type SubagentTool struct {
	runner      AgentRunner
	registry    Registry // 子 agent 可用工具的【超集】；預設取唯讀子集，具名 agent 依其 tools 選用
	reporter    interface{}
	skillLoader *ctxpkg.SkillLoader
	agentLoader *ctxpkg.AgentLoader

	// worktree 隔離（可選）：baseWorkDir 是 session 工作區；regFactory 依給定目錄建同款工具超集。
	// isolation:worktree 的 agent 會在 baseWorkDir 的 git worktree 裡跑，工具 rooted 在該 worktree。
	// 兩者為 nil/空時 isolation 靜默降級為共享工作區。
	baseWorkDir string
	regFactory  func(workDir string) Registry

	subMgr *SubagentManager // 背景子 agent 池（background=true 走這裡；共用給查詢工具）
}

// skillsBaseDir 是含 .claw/skills 與 .claw/agents 的目錄（須與主 agent 的索引同源）。
// subagentRegistry 是子 agent 可委派工具的超集（探索用 read_file+bash；實作型 agent 另需 write/edit）。
func NewSubagentTool(runner AgentRunner, subagentRegistry Registry, reporter interface{}, skillsBaseDir string) *SubagentTool {
	return &SubagentTool{
		runner:      runner,
		registry:    subagentRegistry,
		reporter:    reporter,
		skillLoader: ctxpkg.NewSkillLoader(skillsBaseDir),
		agentLoader: ctxpkg.NewAgentLoader(skillsBaseDir),
		subMgr:      NewSubagentManager(runner),
	}
}

// BackgroundTools 回傳查詢背景子 agent 的工具（subagent_result / subagent_list），與本工具共用同一
// SubagentManager。cmd 端把它們一併註冊，模型才查得到 background=true 委派的結果。
func (t *SubagentTool) BackgroundTools() []BaseTool { return t.subMgr.Tools() }

// WithWorktreeIsolation 開啟 worktree 隔離能力：baseWorkDir＝session 工作區，regFactory 依目錄建工具超集
// （與傳入 subagentRegistry 同款，但 rooted 在指定目錄）。未呼叫則 isolation:worktree 降級為共享工作區。
func (t *SubagentTool) WithWorktreeIsolation(baseWorkDir string, regFactory func(workDir string) Registry) *SubagentTool {
	t.baseWorkDir = baseWorkDir
	t.regFactory = regFactory
	return t
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

func (t *SubagentTool) Definition() schema.ToolDefinition {
	desc := "派出一個子 agent在隔離 context 中執行子任務（探索/審查/規劃…），完畢後回傳一份精煉報告——主 context 不被過程汙染，可一次吐多個並行委派。"
	if idx := t.agentLoader.Index(); idx != "" {
		desc += "\n可用的 agent_type（不指定則為預設探路者，唯讀探索）：\n" + idx
	}
	desc += "可選 skill 參數：綁定技能後其完整正文只載入子 context。可選 background=true：丟背景非同步跑、立即回一個 ID，之後用 subagent_result 查結果（適合可先繼續、稍後再取的探索）。"
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: desc,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "給子 agent下達的明確任務指令。",
				},
				"agent_type": map[string]interface{}{
					"type":        "string",
					"description": "（可選）要派出的具名 agent（見本說明的清單）。指定後用該 agent 的角色 prompt 與工具集；不指定＝預設探路者。",
				},
				"skill": map[string]interface{}{
					"type":        "string",
					"description": "（可選）要綁定給子 agent的技能名稱，須與 System Prompt『技能索引』中的名稱一致。指定後該技能正文只進子 context。",
				},
				"background": map[string]interface{}{
					"type":        "boolean",
					"description": "（可選）true＝丟背景非同步跑、立即回傳 ID（用 subagent_result 查結果）；預設 false＝同步等到回報。背景模式在共享工作區跑（不做 worktree 隔離）。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subagentArgs struct {
	TaskPrompt string `json:"task_prompt"`
	AgentType  string `json:"agent_type"`
	Skill      string `json:"skill"`
	Background bool   `json:"background"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析參數失敗: %w", err)
	}

	// 載入具名 agent（若有）：角色 prompt、工具子集、model/effort/isolation。載入失敗＝error-as-observation。
	var def ctxpkg.AgentDef
	role := "探路者"
	if input.AgentType != "" {
		d, err := t.agentLoader.Load(input.AgentType)
		if err != nil {
			return fmt.Errorf("載入 agent 失敗: %v", err).Error(), nil
		}
		def = d
		role = d.Name
		log.Printf("[Subagent] 🎭 使用具名 agent [%s]（工具 %v，model=%q effort=%q isolation=%q）\n",
			d.Name, d.Tools, d.Model, d.Effort, d.Isolation)
	}

	// 工具集：具名 agent 宣告了 tools 就用（可含 write/edit），否則預設唯讀探路者工具集（安全底線）。
	toolset := defaultSubagentTools
	if len(def.Tools) > 0 {
		toolset = def.Tools
	}
	reg := t.registry.Subset(toolset)

	// 綁定技能：完整正文只載入子 context。失敗＝error-as-observation。
	var skillBody string
	if input.Skill != "" {
		body, err := t.skillLoader.ReadSkill(input.Skill)
		if err != nil {
			return fmt.Errorf("綁定技能失敗: %v", err).Error(), nil
		}
		skillBody = body
		log.Printf("[Subagent] 📎 綁定技能 [%s]（注入 %d 字元正文至子 context）\n", input.Skill, len(body))
	}

	// 背景模式：丟 SubagentManager 非同步跑（共享工作區、silent、不做 worktree 隔離），立即回 ID。
	if input.Background {
		if t.subMgr == nil {
			return "背景子 agent 未啟用（本部署未接背景池）。", nil
		}
		id, serr := t.subMgr.Spawn(SubTask{
			Prompt:       input.TaskPrompt,
			SkillBody:    skillBody,
			SystemPrompt: def.Prompt,
			Model:        def.Model,
			MaxTokens:    effortToMaxTokens(def.Effort),
			Registry:     reg,
			Reporter:     nil, // 背景＝silent，用 subagent_result 取結果
		}, role)
		if serr != nil {
			return serr.Error(), nil
		}
		log.Printf("[Subagent] 🌀 背景委派 [%s] → %s\n", role, id)
		return fmt.Sprintf("🌀 已在背景啟動子 agent [%s]（ID: %s）。之後用 `subagent_result`（id=%s）查結果，或 `subagent_list` 看全部。", role, id, id), nil
	}

	// worktree 隔離（可選）：isolation:worktree 且能力已裝配時，在 base 的 git worktree 裡跑，工具 rooted
	// 在 worktree；完事把 diff 序列化 apply 回主工作區。非 git repo / 未裝配則靜默降級為共享工作區。
	var mergeBack func() string
	if def.Isolation == "worktree" && t.regFactory != nil && t.baseWorkDir != "" {
		if wt, cleanup, err := addWorktree(t.baseWorkDir); err != nil {
			log.Printf("[Subagent] ⚠️ worktree 隔離不可用（%v），改用共享工作區\n", err)
		} else {
			defer cleanup()
			reg = t.regFactory(wt).Subset(toolset) // 工具 rooted 在隔離 worktree
			base := t.baseWorkDir
			mergeBack = func() string {
				patch, derr := worktreeDiff(wt)
				switch {
				case derr != nil:
					return "\n\n[隔離回寫] 抓 diff 失敗：" + derr.Error()
				case strings.TrimSpace(patch) == "":
					return "\n\n[隔離回寫] 子 agent 未改動任何檔案。"
				default:
					if aerr := applyPatchToBase(base, patch); aerr != nil {
						return "\n\n[隔離回寫] ⚠️ " + aerr.Error() + "\n（改動仍在隔離區的 diff）:\n" + patch
					}
					return "\n\n[隔離回寫] ✅ 已把子 agent 的改動 apply 回主工作區。"
				}
			}
			log.Printf("[Subagent] 🌿 worktree 隔離：%s\n", wt)
		}
	}

	log.Printf("[Subagent] 🚀 主 Agent 發起委派！正在拉起 [%s]: [%s]...\n", role, input.TaskPrompt)

	summary, err := t.runner.RunSub(ctx, SubTask{
		Prompt:       input.TaskPrompt,
		SkillBody:    skillBody,
		SystemPrompt: def.Prompt, // 空＝RunSub 回退預設探路者 prompt
		Name:         input.AgentType,
		Model:        def.Model,
		MaxTokens:    effortToMaxTokens(def.Effort),
		Registry:     reg,
		Reporter:     t.reporter,
	})
	if err != nil {
		if errors.Is(err, ErrPolicyDenied) {
			// 政策拒絕【不做】error-as-observation：原樣回傳讓 registry 標 Denied，主迴圈據此
			// 終止整個目標——否則主 agent 會把「子 agent 被拒」當成可重試的觀察，換個方式再派。
			return "", err
		}
		// error-as-observation：讓主 agent 看到失敗但不中斷主 ReAct 迴圈。
		return fmt.Errorf("子 agent執行失敗: %v", err).Error(), nil
	}
	if mergeBack != nil {
		summary += mergeBack() // 把隔離回寫結果附在報告尾，讓主 agent 知道改動去向
	}

	log.Printf("[Subagent] ✅ 子 agent任務結束。報告回傳給主幹...")

	return fmt.Sprintf("【子 agent探索報告】:\n%s", summary), nil
}
