package engine

// OfficeReporter 把 agent 執行事件投影到像素辦公室（unity_demo 的 FastAPI 橋）。
// 契約：POST <url>/office/event，kind ∈ start/turn/think/tool/result/error/msg/done——與 dashboard
// sseReporter 同一套事件詞彙，子 agent 事件沿用 "[Subagent:名] 工具" 前綴（由橋端解析）。
//
// 【協定全文】docs/office-protocol.md（欄位語意、截斷長度、傳遞保證、版本演進規則）。
// 可執行正本是本套件的 TestOfficeReporterContract——改動事件形狀請同時更新那三處。
//
// 事件走緩衝 channel + 單一 sender goroutine，fire-and-forget：橋不在線或太慢就丟事件。
// 辦公室是狀態投影，掉幀無害，絕不能反壓 agent 主迴圈。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// officeProtocolVersion 是事件協定版本，隨每個事件送出。橋端據此在協定演進時分支相容；
// 沒有它的話，改欄位語意就只能靠雙方同時上線。**只有不相容的變更才進版號**（加欄位不算——
// 那對「忽略未知欄位」的解析器天生相容）。契約全文見 docs/office-protocol.md。
const officeProtocolVersion = 1

type officeEvent struct {
	V      int    `json:"v"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type OfficeReporter struct {
	agent string
	ch    chan officeEvent
	done  chan struct{}
	// quit 是收工訊號。【刻意不關 ch】——關了的話，任何 Close 之後才到的事件都會讓 push panic
	// （送進已關的 channel 必 panic，select/default 擋不住），而 reporter 是交給引擎與子 agent 持有的，
	// 生命週期靠約定維持。一個「掉幀無害」的狀態投影絕不該有能力弄垮 agent，故改用 quit 訊號收工：
	// ch 永不關 → push 永遠安全，最壞只是事件被丟。
	quit      chan struct{}
	closeOnce sync.Once // Close 冪等（重複呼叫不 panic）
}

// NewOfficeReporter 建投影回報器。url 為橋的根位址（如 http://localhost:8123），
// agent 為對應 Unity NPC 的 persona id（如 p17）。
func NewOfficeReporter(url, agent string) *OfficeReporter {
	r := &OfficeReporter{
		agent: agent,
		ch:    make(chan officeEvent, 64),
		done:  make(chan struct{}),
		quit:  make(chan struct{}),
	}
	go r.send(strings.TrimRight(url, "/") + "/office/event")
	return r
}

// closeDrainBudget 是 Close 等待緩衝排空的【總】預算。橋健康時排空是毫秒級、橋不在線時連線秒拒，
// 兩者都不會用到它；真正的風險是【半死的橋】（接受連線但不回應）——每筆事件吃滿 client timeout，
// 64 筆緩衝能讓 Close 卡上兩分鐘。而 Close 跑在 handleAgentRun 的 defer、在釋放頻道鎖【之前】，
// 於是使用者看到「✅ 任務完成」後，兩分鐘內發不了下一則（被回「上一個任務仍在進行」）。
// 狀態投影不值這個代價——這正是本檔開頭「絕不能反壓 agent 主迴圈」該涵蓋的最後一哩。
const closeDrainBudget = 2 * time.Second

// Close 送出收工訊號、等緩衝排空（有預算）後返回。冪等；逾時即放生 sender goroutine——它排完
// 剩餘事件自行結束（不洩漏），只是那些事件晚一點或送不到。掉幀無害，卡住有害。
func (r *OfficeReporter) Close() {
	r.closeOnce.Do(func() { close(r.quit) })
	select {
	case <-r.done:
	case <-time.After(closeDrainBudget):
	}
}

func (r *OfficeReporter) send(endpoint string) {
	defer close(r.done)
	// client 每個 sender 各一份，但 Transport 為 nil ＝ 共用 http.DefaultTransport 的連線池，
	// 跨 reporter 實例本來就重用連線（實測：5 個 reporter 送 5 個請求，伺服器端只見 1 條新 TCP 連線）。
	// 別為了「共用 client」重構——那是不存在的問題。
	client := &http.Client{Timeout: 2 * time.Second}
	post := func(ev officeEvent) {
		b, _ := json.Marshal(ev)
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(b))
		if err != nil {
			return // 橋不在線：靜默丟
		}
		resp.Body.Close()
	}
	for {
		select {
		case ev := <-r.ch:
			post(ev)
		case <-r.quit:
			// 收工：把已緩衝的排完再走（Close 那頭有總預算，卡住也不會拖著呼叫端）。
			// 排空當下若又有新事件進來，下一輪 default 就結束——收工後的事件本來就是可丟的。
			for {
				select {
				case ev := <-r.ch:
					post(ev)
				default:
					return
				}
			}
		}
	}
}

// push 投遞事件：緩衝滿或已收工都直接丟棄，永不阻塞、永不 panic（ch 不會被關，見 quit 的說明）。
func (r *OfficeReporter) push(kind, label, detail string) {
	select {
	case r.ch <- officeEvent{V: officeProtocolVersion, Agent: r.agent, Kind: kind, Label: label, Detail: detail}:
	default: // 緩衝滿：丟事件保引擎不阻塞
	}
}

// Begin / End 標記一次任務的起訖（Reporter 介面沒有生命週期事件，由 caller 顯式呼叫）。
// Begin 標記任務開始；workDir 帶上該會話的工作目錄，讓辦公室看板直接標出產出落在哪。
func (r *OfficeReporter) Begin(task, workDir string) {
	r.push("start", schema.TruncRunes(task, 80, "…"), workDir)
}
func (r *OfficeReporter) End(err error) {
	if err != nil {
		r.push("done", "error", schema.TruncRunes(err.Error(), 120, "…"))
		return
	}
	r.push("done", "ok", "")
}

func (r *OfficeReporter) OnThinking(context.Context) { r.push("think", "", "") }
func (r *OfficeReporter) OnTurn(_ context.Context, turn int) {
	r.push("turn", fmt.Sprintf("%d", turn), "")
}
func (r *OfficeReporter) OnToolCall(_ context.Context, name, args string) {
	r.push("tool", name, schema.TruncRunes(args, 120, "…"))
}
func (r *OfficeReporter) OnToolResult(_ context.Context, name, result string, isErr bool) {
	kind := "result"
	if isErr {
		kind = "error"
	}
	r.push(kind, name, schema.TruncRunes(result, 400, "…"))
}
func (r *OfficeReporter) OnMessage(_ context.Context, content string) {
	if content != "" {
		// 全文供橋端「點 NPC 看報告」面板用，上限放寬；泡泡顯示由橋端自行截短。
		r.push("msg", schema.TruncRunes(content, 2000, "…"), "")
	}
}

var _ Reporter = (*OfficeReporter)(nil)
