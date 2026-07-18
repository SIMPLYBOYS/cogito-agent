package main

import (
	"bytes"
	"context"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/mcp"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/sandbox"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// mcpDialTimeout 是啟動時連接單一 MCP server（含握手 / tools/list）的上限，與 bot 一致。
const mcpDialTimeout = 30 * time.Second

// operatorSessionID 是內嵌 operator chat 專用的 session id，與 IM/CLI 的 session 隔開，不撞歷史。
const operatorSessionID = "operator"

// chatRunner 讓 dashboard 在自己的行程內驅動 agent（等同「claw-cli 包在 web form 後面」）：專用
// operator session，工具集與 claw-cli 對齊（read/write/bash/edit/skill/recall + subagent + 背景任務）。
// 這是【寫入】能力（會真的跑 bash/寫檔），故 opt-in（COGITO_DASH_CHAT=1）；一次只允許一個 run（mu 序列化）。
// 執行事件經 hub 以 SSE 即時串流到瀏覽器（見 chat_stream.go）。
//
// 行程模型：若 bot（cmd/claw）也在跑，這是【第二個】agent 實例。用專屬 operator session 避免歷史相撞；
// 但兩者共享同一 workspace，檔案/bash 副作用是共用的——這正是 operator 就地驅動同一工作區的目的。
type chatRunner struct {
	eng      *engine.AgentEngine
	reporter engine.Reporter // fanout：終端 console + SSE hub
	hub      *sseHub
	workDir  string
	mu       sync.Mutex   // 序列化：一次一個 operator run，避免同 session 併發改歷史
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

	// 外部 MCP 工具（設了 COGITO_MCP_CONFIG 才啟用）：與 bot/cli 同一條路，經 gateway 漸進式暴露
	// （mcp_call_tool / mcp_describe_tool + 輕量目錄），連不上的 server 略過、不擋 chat 啟動。
	// gateway 建一次，主 agent 與子 agent 共用（見下方 buildSubReg）。
	mcpGW := buildMCPGateway()
	registerGatewayTools(registry, mcpGW)

	eng := engine.NewAgentEngine(tracked, registry, false, false)

	hub := &sseHub{}
	// 事件同時打到終端（跑 dashboard 的 console）與 SSE hub（瀏覽器即時串流）。
	reporter := multiReporter{rs: []engine.Reporter{engine.NewTerminalReporter(), sseReporter{hub: hub}}}

	buildSubReg := func(wd string) tools.Registry {
		r := tools.NewRegistry()
		r.Register(tools.NewReadFileTool(wd))
		r.Register(tools.NewBashToolWithExecutor(wd, executor))
		r.Register(tools.NewWriteFileTool(wd))
		r.Register(tools.NewEditFileTool(wd))
		registerGatewayTools(r, mcpGW) // 子 agent 也能用 MCP（如研究型子 agent 查 twinkle-hub）
		return r
	}
	subTool := tools.NewSubagentTool(eng, buildSubReg(workDir), reporter, workDir).
		WithWorktreeIsolation(workDir, buildSubReg)
	registry.Register(subTool)
	for _, bt := range subTool.BackgroundTools() {
		registry.Register(bt)
	}

	c := &chatRunner{eng: eng, reporter: reporter, hub: hub, workDir: workDir}
	c.lastErr.Store("")
	return c, nil
}

// buildMCPGateway 連上 COGITO_MCP_CONFIG 指定的外部 MCP 伺服器並建 gateway（漸進式暴露）。與 bot
// 同一套：未設 config 回 nil；連不上的 server 略過，不擋 chat 啟動。連線是行程級長壽命的（由 gateway/
// tools 鏈保活，行程結束 OS 回收 stdio 子進程）。回傳 gateway 供主 agent 與子 agent 共用同一組連線。
func buildMCPGateway() *mcp.Gateway {
	cfgPath := os.Getenv("COGITO_MCP_CONFIG")
	if cfgPath == "" {
		return nil
	}
	servers, err := mcp.LoadConfig(cfgPath)
	if err != nil {
		log.Printf("[mcp] 讀取設定失敗，operator chat 不載 MCP: %v", err)
		return nil
	}
	var clients []*mcp.Client
	for _, s := range servers {
		dialCtx, cancel := context.WithTimeout(context.Background(), mcpDialTimeout)
		cl, errDial := mcp.Dial(dialCtx, s)
		cancel()
		if errDial != nil {
			log.Printf("[mcp] 連接 %q 失敗，略過: %v", s.Name, errDial)
			continue
		}
		clients = append(clients, cl)
		log.Printf("[mcp] 已連接 server %q", s.Name)
	}
	if len(clients) == 0 {
		return nil
	}
	gwCtx, cancel := context.WithTimeout(context.Background(), mcpDialTimeout)
	gw, errGw := mcp.NewGateway(gwCtx, clients)
	cancel()
	if errGw != nil {
		log.Printf("[mcp] 建立 gateway 失敗: %v", errGw)
		return nil
	}
	log.Printf("[mcp] operator chat gateway 就緒：%d 個外部工具（主 agent 與子 agent 皆可用）經 mcp_call_tool 漸進式暴露", gw.Count())
	return gw
}

// registerGatewayTools 把 gateway 的 2 個工具（mcp_call_tool / mcp_describe_tool）註冊進一個 registry。
// nil-safe。同一組 gateway 工具可註冊進多個 registry（主 + 各子 agent），共用底層連線（transport 有
// mutex 序列化，多路並發安全）。
func registerGatewayTools(r tools.Registry, gw *mcp.Gateway) {
	if gw == nil {
		return
	}
	for _, gt := range gw.Tools() {
		r.Register(gt)
	}
}

// start 非阻塞地跑一輪 operator agent：先【同步】Append user 訊息（讓它立刻顯示），再在背景 goroutine
// 跑 engine.Run（可能數十秒），執行事件經 hub 即時串流。POST 因此立刻返回。忙碌（已有 run 進行中）時
// 回 false、不排隊。mu 於 goroutine 內 Unlock（Go 允許跨 goroutine 解鎖）。
func (c *chatRunner) start(userMsg string) bool {
	if !c.mu.TryLock() {
		return false
	}
	c.hub.begin()
	sess := ctxpkg.GlobalSessionMgr.GetOrCreate(operatorSessionID, c.workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: userMsg}) // 立刻落地→GET 馬上看得到提問
	// delta sink：主迴圈的 LLM 文字 token 增量 → hub「delta」事件 → 瀏覽器逐字顯示。
	ctx := engine.WithStreamSink(context.Background(), func(delta string) {
		c.hub.push(evJSON("delta", delta))
	})
	go func() {
		defer c.mu.Unlock()
		defer c.hub.end()
		if err := c.eng.Run(ctx, sess, c.reporter); err != nil {
			c.lastErr.Store(err.Error())
			c.hub.push(evJSON("error", "執行出錯："+err.Error()))
		} else {
			c.lastErr.Store("")
		}
	}()
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
	var model string
	if s.store != nil {
		if snap, ok, _ := s.store.Load(operatorSessionID); ok {
			hist = snap.History
			model = snap.Model
		}
	}
	data := chatView{
		Msgs:    toBubbles(hist),
		Running: s.chat.hub.isRunning(),
		Model:   model,
	}
	if e, _ := s.chat.lastErr.Load().(string); e != "" {
		data.LastErr = e
	}
	var b bytes.Buffer
	_ = chatTmpl.Execute(&b, data)
	s.renderChat(w, "Operator Chat", template.HTML(b.String())) // renderChat：放寬 CSP 供 SSE 串流
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
		s.chat.start(msg) // 非阻塞：背景跑 agent、事件即時串流；忙碌則 no-op（回 false 不排隊）
	}
	http.Redirect(w, r, "/chat#end", http.StatusSeeOther) // PRG：避免重整重送；錨點捲到最新
}

// chatReset 清空 operator session，開始新對話（甩掉舊上下文，例如工具能力變更前的過時結論）。
// 執行中不清（避免抽掉正在跑的 agent 的歷史）。CSRF 防護同 chatPost。
func (s *server) chatReset(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.NotFound(w, r)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	if !s.chat.hub.isRunning() {
		ctxpkg.GlobalSessionMgr.GetOrCreate(operatorSessionID, s.chat.workDir).Reset()
		s.chat.lastErr.Store("")
	}
	http.Redirect(w, r, "/chat", http.StatusSeeOther)
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
  .chat .note { color:var(--m); font-size:12.5px; margin:0 0 10px; }
  .chat .note a { font-weight:600; }
  .chat .resetform { margin:0 0 16px; }
  .chat button.reset { font:inherit; font-size:12px; color:var(--m); background:transparent; border:1px solid var(--ln); border-radius:6px; padding:3px 11px; cursor:pointer; }
  .chat button.reset:hover { color:var(--a); border-color:var(--a); }
  .chat .banner { border:1px solid var(--a); border-radius:7px; padding:8px 12px; margin-bottom:14px; color:var(--a); font-size:13px; }
  .chat .banner.done { color:var(--ok,#86b06e); border-color:var(--ok,#86b06e); }
  .chat .thread { display:flex; flex-direction:column; gap:12px; margin-bottom:16px; }
  .chat .msg { max-width:82%; padding:9px 13px; border-radius:10px; white-space:pre-wrap; word-break:break-word; font-size:14px; }
  .chat .msg.you { align-self:flex-end; background:var(--a); color:#fff; border-bottom-right-radius:3px; }
  .chat .msg.bot { align-self:flex-start; background:var(--p); border:1px solid var(--ln); border-bottom-left-radius:3px; }
  .chat .msg .tag { display:block; font-size:10px; text-transform:uppercase; letter-spacing:.1em; opacity:.7; margin-bottom:3px; }
  .chat .msg.bot .used { display:block; margin-top:6px; font-size:11px; color:var(--m); }
  .chat .empty { color:var(--m); font-style:italic; margin-bottom:20px; }
  /* 即時串流區：agent 執行事件逐筆冒出 */
  .chat #live { display:flex; flex-direction:column; gap:4px; margin-bottom:16px; }
  .chat .ev { display:flex; gap:8px; font-size:12.5px; line-height:1.5; padding:3px 0; border-left:2px solid var(--ln); padding-left:12px; }
  .chat .ev .ic { color:var(--a2); flex:none; }
  .chat .ev .tx { white-space:pre-wrap; word-break:break-word; }
  .chat .ev.turn { color:var(--m); text-transform:uppercase; letter-spacing:.08em; font-size:11px; border-left-color:transparent; margin-top:6px; }
  .chat .ev.turn .ic { color:var(--m); }
  .chat .ev.think { color:var(--m); font-style:italic; }
  .chat .ev.tool .ic { color:var(--a2); }
  .chat .ev.result { color:var(--m); }
  .chat .ev.error { color:var(--a); border-left-color:var(--a); }
  .chat .ev.msg { color:var(--fg); border-left-color:var(--a); }
  .chat .ev.msg .ic { color:var(--a); }
  .chat .ev.msg.streaming .tx::after { content:'▋'; color:var(--a); animation:blink 1s step-end infinite; }
  @keyframes blink { 50% { opacity:0; } }
  @media (prefers-reduced-motion: reduce) { .chat .ev.msg.streaming .tx::after { animation:none; } }
  .chat form.composer { display:flex; flex-direction:column; gap:8px; border-top:1px solid var(--ln); padding-top:16px; }
  .chat textarea { width:100%; resize:vertical; font:inherit; font-size:14px; color:var(--fg); background:var(--p);
    border:1px solid var(--ln); border-radius:8px; padding:10px 12px; }
  .chat textarea:focus { outline:none; border-color:var(--a); }
  .chat textarea:disabled { opacity:.55; }
  .chat .row { display:flex; align-items:center; gap:12px; }
  .chat button { font:inherit; font-weight:700; letter-spacing:.03em; color:#fff; background:var(--a); border:none;
    border-radius:8px; padding:8px 20px; cursor:pointer; }
  .chat button:hover { filter:brightness(1.08); }
  .chat button:disabled { opacity:.5; cursor:not-allowed; }
  .chat .hint { color:var(--m); font-size:11.5px; }
</style>
<div class="chat">
  {{if .Running}}<noscript><meta http-equiv="refresh" content="3"></noscript>{{end}}
  <p class="note">operator 就地驅動 agent（session <code>operator</code>，工作區同 bot／CLI）。完整執行樹（工具調用、子 agent）見 <a href="/runs/operator">/runs/operator</a>。</p>
  {{if and .Msgs (not .Running)}}<form method="POST" action="/chat/reset" class="resetform"><button type="submit" class="reset">＋ 新對話（清空上下文）</button></form>{{end}}
  {{if .LastErr}}{{if not .Running}}<div class="banner">⚠️ 上次執行出錯：{{.LastErr}}</div>{{end}}{{end}}
  {{if .Msgs}}<div class="thread">
    {{range .Msgs}}<div class="msg {{if .You}}you{{else}}bot{{end}}"><span class="tag">{{if .You}}你{{else}}operator agent{{end}}</span>{{.Text}}{{if .UsedTool}}<span class="used">⚙ 本回合動過工具／子 agent，詳見執行樹</span>{{end}}</div>{{end}}
  </div>{{else}}{{if not .Running}}<p class="empty">尚無對話。在下方交辦第一個任務。</p>{{end}}{{end}}
  {{if .Running}}<div id="runbanner" class="banner">⏳ agent 執行中…（即時串流；完成後可繼續交辦）</div>
  <div id="live"></div>{{end}}
  <form method="POST" action="/chat" class="composer" id="composer">
    <textarea name="msg" rows="3" placeholder="交辦任務給 operator agent…（會真的執行 bash／寫檔）"{{if .Running}} disabled{{else}} autofocus{{end}}></textarea>
    <div class="row"><button type="submit"{{if .Running}} disabled{{end}}>送出</button><span class="hint">Enter 換行；點「送出」交辦{{with .Model}} · {{.}}{{end}}</span></div>
  </form>
  <div id="end"></div>
</div>
{{if .Running}}<script src="/chat.js"></script>{{end}}`))
