package main

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// operatorSessionID 是內嵌 operator chat 專用的 session id，與 IM/CLI 的 session 隔開，不撞歷史。
const operatorSessionID = "operator"

// chatRunner 讓 dashboard 在自己的行程內驅動 agent（等同「claw-cli 包在 web form 後面」）：專用
// operator session，工具集與 claw-cli 對齊（read/write/bash/edit/skill/recall + subagent + 背景任務）。
// 這是【寫入】能力（會真的跑 bash/寫檔），故 opt-in（COGITO_DASH_CHAT=1）；一次只允許一個 run（mu 序列化）。
//
// 行程模型：若 bot（cmd/claw）也在跑，這是【第二個】agent 實例。用專屬 operator session 避免歷史相撞；
// 但兩者共享同一 workspace，檔案/bash 副作用是共用的——這正是 operator 就地驅動同一工作區的目的。
type chatRunner struct {
	eng      *engine.AgentEngine
	reporter engine.Reporter
	workDir  string
	mu       sync.Mutex   // 序列化：一次一個 operator run，避免同 session 併發改歷史
	running  atomic.Bool  // 供 GET 顯示「執行中」（另開分頁時看得到）
	lastErr  atomic.Value // string：上次 run 的錯誤（空＝成功）
}

// newChatRunner 組裝 operator agent。呼叫端須先對 GlobalSessionMgr.SetStore（讓 operator session
// 落地、且唯讀視圖看得到）。provider 缺 key 會回錯——由呼叫端決定「停用 chat 但保留唯讀面板」。
func newChatRunner(workDir string) (*chatRunner, error) {
	realProvider, modelName, err := provider.FromEnv()
	if err != nil {
		return nil, err
	}
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(operatorSessionID, workDir)
	tracked := observability.NewCostTracker(realProvider, modelName, sess)
	executor := sandbox.FromEnv()

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashToolWithExecutor(workDir, executor))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewReadSkillTool(workDir))
	registry.Register(tools.NewRecallTool(workDir))
	registry.Register(tools.NewBarChartTool())

	taskMgr := tools.NewTaskManager(executor, workDir)
	for _, tt := range tools.NewTaskTools(taskMgr) {
		registry.Register(tt)
	}

	eng := engine.NewAgentEngine(tracked, registry, false, false)
	reporter := engine.NewTerminalReporter() // 進度打到跑 dashboard 的終端；web 視圖重載時從 session 讀

	buildSubReg := func(wd string) tools.Registry {
		r := tools.NewRegistry()
		r.Register(tools.NewReadFileTool(wd))
		r.Register(tools.NewBashToolWithExecutor(wd, executor))
		r.Register(tools.NewWriteFileTool(wd))
		r.Register(tools.NewEditFileTool(wd))
		return r
	}
	subTool := tools.NewSubagentTool(eng, buildSubReg(workDir), reporter, workDir).
		WithWorktreeIsolation(workDir, buildSubReg)
	registry.Register(subTool)
	for _, bt := range subTool.BackgroundTools() {
		registry.Register(bt)
	}

	c := &chatRunner{eng: eng, reporter: reporter, workDir: workDir}
	c.lastErr.Store("")
	return c, nil
}

// run 同步跑一輪 operator agent（阻塞到 agent 完成）。忙碌時回 false、不排隊。
func (c *chatRunner) run(userMsg string) bool {
	if !c.mu.TryLock() {
		return false
	}
	defer c.mu.Unlock()
	c.running.Store(true)
	defer c.running.Store(false)

	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(operatorSessionID, c.workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: userMsg})
	if err := c.eng.Run(context.Background(), sess, c.reporter); err != nil {
		c.lastErr.Store(err.Error())
	} else {
		c.lastErr.Store("")
	}
	return true
}

// sameOrigin 是 CSRF 防線：唯讀 GET 無所謂，但 POST 會執行 agent（bash/寫檔），必須擋掉別的網站對
// localhost 發的跨站自動 POST。優先信 Sec-Fetch-Site（瀏覽器送、JS 無法偽造），退回 Origin 比對。
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "cross-site", "same-site":
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 非瀏覽器（curl 等操作者自用）；瀏覽器的表單 POST 一定帶 Origin
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func (s *server) chatGet(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		s.render(w, "Operator Chat", template.HTML(`<p class="muted">內嵌 operator chat 未啟用（唯讀模式）。</p>`+
			`<p class="muted">這是<b>寫入</b>能力——會真的執行 bash／寫檔。啟用：啟動 dashboard 時設 `+
			`<code>COGITO_DASH_CHAT=1</code>（並確保有可用的 LLM provider 金鑰）。</p>`))
		return
	}
	var hist []schema.Message
	var meta string
	if s.store != nil {
		if snap, ok, _ := s.store.Load(operatorSessionID); ok {
			hist = snap.History
			meta = snap.Model
		}
	}
	data := chatView{
		Msgs:    toBubbles(hist),
		Running: s.chat.running.Load(),
		Model:   meta,
	}
	if e, _ := s.chat.lastErr.Load().(string); e != "" {
		data.LastErr = e
	}
	var b bytes.Buffer
	_ = chatTmpl.Execute(&b, data)
	s.render(w, "Operator Chat", template.HTML(b.String()))
}

func (s *server) chatPost(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	msg := strings.TrimSpace(r.FormValue("msg"))
	if msg != "" {
		s.chat.run(msg) // 同步：阻塞到 agent 完成；忙碌則 no-op（回 false 不排隊）
	}
	http.Redirect(w, r, "/chat#end", http.StatusSeeOther) // PRG：避免重整重送；錨點捲到最新
}

// chatView / bubble 是 chat 頁的精簡對話視圖：只呈現 user 提問 + agent 最終回覆（工具調用/子 agent 的
// 完整執行樹在 /runs/operator，不在此重造）。usedTools 標記該回合動過工具，提示去看執行樹。
type chatView struct {
	Msgs    []bubble
	Running bool
	Model   string
	LastErr string
}

type bubble struct {
	You      bool
	Text     string
	UsedTool bool // agent 這則回覆前動過工具
}

// toBubbles 把扁平 history 壓成對話氣泡：user（無 ToolCallID）＝提問；assistant 無 tool call＝最終回覆。
// 中間的 tool-call turn 與 tool 結果不進氣泡（併成回覆上的 usedTool 標記），細節留給 /runs/operator。
func toBubbles(history []schema.Message) []bubble {
	var out []bubble
	pendingTool := false
	for _, m := range history {
		switch m.Role {
		case schema.RoleUser:
			if m.ToolCallID != "" {
				continue // 工具結果
			}
			out = append(out, bubble{You: true, Text: m.Content})
		case schema.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				pendingTool = true // 動了工具，等最終回覆時標記
				continue
			}
			out = append(out, bubble{Text: m.Content, UsedTool: pendingTool})
			pendingTool = false
		}
	}
	return out
}

var chatTmpl = template.Must(template.New("chat").Parse(`<style>
  .chat { --a:var(--acc); --a2:var(--acc2); --m:var(--mut); --ln:var(--line); --p:var(--bg2); }
  .chat .note { color:var(--m); font-size:12.5px; margin:0 0 14px; }
  .chat .note a { font-weight:600; }
  .chat .banner { border:1px solid var(--a); border-radius:7px; padding:8px 12px; margin-bottom:14px; color:var(--a); font-size:13px; }
  .chat .thread { display:flex; flex-direction:column; gap:12px; margin-bottom:22px; }
  .chat .msg { max-width:82%; padding:9px 13px; border-radius:10px; white-space:pre-wrap; word-break:break-word; font-size:14px; }
  .chat .msg.you { align-self:flex-end; background:var(--a); color:#fff; border-bottom-right-radius:3px; }
  .chat .msg.bot { align-self:flex-start; background:var(--p); border:1px solid var(--ln); border-bottom-left-radius:3px; }
  .chat .msg .tag { display:block; font-size:10px; text-transform:uppercase; letter-spacing:.1em; opacity:.7; margin-bottom:3px; }
  .chat .msg.bot .used { display:block; margin-top:6px; font-size:11px; color:var(--m); }
  .chat .empty { color:var(--m); font-style:italic; margin-bottom:20px; }
  .chat form.composer { display:flex; flex-direction:column; gap:8px; border-top:1px solid var(--ln); padding-top:16px; }
  .chat textarea { width:100%; resize:vertical; font:inherit; font-size:14px; color:var(--fg); background:var(--p);
    border:1px solid var(--ln); border-radius:8px; padding:10px 12px; }
  .chat textarea:focus { outline:none; border-color:var(--a); }
  .chat .row { display:flex; align-items:center; gap:12px; }
  .chat button { font:inherit; font-weight:700; letter-spacing:.03em; color:#fff; background:var(--a); border:none;
    border-radius:8px; padding:8px 20px; cursor:pointer; }
  .chat button:hover { filter:brightness(1.08); }
  .chat .hint { color:var(--m); font-size:11.5px; }
</style>
<div class="chat">
  <p class="note">operator 就地驅動 agent（session <code>operator</code>，工作區同 bot／CLI）。完整執行樹（工具調用、子 agent）見 <a href="/runs/operator">/runs/operator</a>。</p>
  {{if .Running}}<div class="banner">⏳ agent 執行中…（本頁同步阻塞；完成後會自動更新。可另開分頁看 /runs/operator）</div>{{end}}
  {{if .LastErr}}<div class="banner">⚠️ 上次執行出錯：{{.LastErr}}</div>{{end}}
  {{if .Msgs}}<div class="thread">
    {{range .Msgs}}<div class="msg {{if .You}}you{{else}}bot{{end}}"><span class="tag">{{if .You}}你{{else}}operator agent{{end}}</span>{{.Text}}{{if .UsedTool}}<span class="used">⚙ 本回合動過工具／子 agent，詳見執行樹</span>{{end}}</div>{{end}}
  </div>{{else}}<p class="empty">尚無對話。在下方交辦第一個任務。</p>{{end}}
  <form method="POST" action="/chat" class="composer">
    <textarea name="msg" rows="3" placeholder="交辦任務給 operator agent…（會真的執行 bash／寫檔）" autofocus></textarea>
    <div class="row"><button type="submit">送出</button><span class="hint">Enter 換行；點「送出」交辦{{with .Model}} · {{.}}{{end}}</span></div>
  </form>
  <div id="end"></div>
</div>`))
