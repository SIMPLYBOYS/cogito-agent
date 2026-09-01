package engine

// OfficeReporter 把 agent 執行事件投影到像素辦公室（unity_demo 的 FastAPI 橋）。
// 契約：POST <url>/office/event，kind ∈ start/turn/think/tool/result/error/msg/done——與 dashboard
// sseReporter 同一套事件詞彙，子 agent 事件沿用 "[Subagent:名] 工具" 前綴（由橋端解析）。
//
// 【協定全文】docs/office-protocol.md（欄位語意、截斷長度、傳遞保證、版本演進規則）。
// 可執行正本是本套件的 TestOfficeReporterContract——改動事件形狀請同時更新那三處。
//
// 事件走緩衝 channel + 單一 sender goroutine，fire-and-forget：橋不在線或太慢就丟事件。
// 絕不能反壓 agent 主迴圈——這條不變。
//
// 但「掉幀無害」只對【泡泡】成立。橋端不是無狀態渲染器：它拿 spawn_subagent 的 tool 事件
// 徵用 NPC 進 busy、拿 result/error 釋放。丟掉釋放事件，那個 NPC 就永遠不回座位（實際症狀：
// 跑幾輪 orchestrator 之後「很多人杵著不動」，而且完全查不到原因）。故事件分兩級：
// 狀態機事件走獨立佇列（見 isCritical），泡泡事件維持滿了就丟。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// Cost 是本次任務的【真實】花費（美元，provider 回報的 usage 累計），只在 done 事件帶。
	// omitempty：0 或未知＝不送——投影估計值跟畫假的進度條是同一種謊，寧可空白。
	Cost float64 `json:"cost,omitempty"`
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

	// queue 是【單一保序佇列】。刻意不用兩條 channel 做優先級——那會讓關鍵事件插隊到
	// 泡泡前面，而順序是有語意的：`done` 插到 `msg` 前面會讓卡片在報告寫進去之前就關掉；
	// 下一個任務的 `start` 插到上一個任務的尾巴泡泡前面，泡泡就掛到錯的卡上。
	// （TestOfficeReporterContract 抓到過這個——那條測試的存在理由就是釘住順序。）
	//
	// 改成 slice + mutex：順序完全保留，滿載時【只丟泡泡】，關鍵事件照樣入列。
	mu    sync.Mutex
	queue []officeEvent
	wake  chan struct{} // 容量 1 的喚醒訊號（有事件了）
	// dropped 記關鍵事件仍然被丟掉的次數。連這條佇列都滿＝橋掛很久了，此時丟仍優於
	// 反壓引擎；但要留下痕跡，否則下次又是「查不到為什麼有人不回座位」。
	dropped atomic.Int64
}

// isCritical 判斷一個事件會不會改變橋端的狀態機（→ 不可丟）。
//
// 【為何需要這個分級】原本 push 一律「滿了就丟」，理由寫的是「掉幀無害」。那句話對泡泡
// 成立、對狀態機不成立：橋端拿 spawn_subagent 的 tool 事件【徵用】一個 NPC 進 busy、拿它的
// result/error 事件【釋放】。丟掉釋放事件，那個 NPC 就永遠不回座位——而 orchestrator 並行
// 收工時正是事件最密集、最容易溢位的一刻。實際症狀：跑幾輪之後「很多 agent 都杵著不動」。
//
// 判準與橋端的正規表達式對齊（backend/main.py 的 SPAWN_RE / 投影表）：
//   - start / done：任務起訖，開卡與收卡
//   - name 以 spawn_subagent 開頭的 tool / result / error：徵用與釋放 NPC
//
// 其餘（think/turn/msg、一般工具的 tool/result）只影響泡泡，滿了照丟。
func isCritical(kind, label string) bool {
	switch kind {
	case "start", "done":
		return true
	case "msg":
		// 訊息升關鍵：它是報告本體，不是裝飾。看板六個子 agent 並行收工正是泡泡佇列
		// 最容易滿的一刻——掉一則報告比掉十顆泡泡嚴重（實際回報：對照 dashboard 才
		// 發現工作串少了訊息）。關鍵佇列 320 有餘裕。
		return true
	case "tool", "result", "error":
		return strings.HasPrefix(label, "spawn_subagent")
	}
	return false
}

// NewOfficeReporter 建投影回報器。url 為橋的根位址（如 http://localhost:8123），
// agent 為對應 Unity NPC 的 persona id（如 p17）。
func NewOfficeReporter(url, agent string) *OfficeReporter {
	r := &OfficeReporter{
		agent: agent,
		done:  make(chan struct{}),
		quit:  make(chan struct{}),
		wake:  make(chan struct{}, 1),
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

// 佇列水位。bubbleQueueMax 以下人人可入；之上【只收關鍵事件】直到 criticalQueueMax。
// 兩段式水位而非兩條佇列，是為了保序——見 struct 上 queue 的說明。
const (
	bubbleQueueMax   = 64
	criticalQueueMax = 320 // 64 + 256：泡泡滿載後關鍵事件仍有的餘裕
)

// Close 送出收工訊號、等緩衝排空（有預算）後返回。冪等；逾時即放生 sender goroutine——它排完
// 剩餘事件自行結束（不洩漏），只是那些事件晚一點或送不到。掉幀無害，卡住有害。
func (r *OfficeReporter) Close() {
	r.closeOnce.Do(func() { close(r.quit) })
	select {
	case <-r.done:
	case <-time.After(closeDrainBudget):
	}
	// 關鍵事件被丟＝橋端狀態機已與真實情況脫節（NPC 卡在 busy、卡片沒收）。這種事以前是
	// 靜默的，於是症狀變成「很多人杵著不動」而完全查不到原因——寧可吵一句。
	if n := r.dropped.Load(); n > 0 {
		log.Printf("⚠ [office] %s 有 %d 個狀態事件未送達（橋端卡片/NPC 可能停在錯的狀態）", r.agent, n)
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
	// pop 取出隊首（保序）。ok=false 表示目前沒東西。
	pop := func() (officeEvent, bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.queue) == 0 {
			return officeEvent{}, false
		}
		ev := r.queue[0]
		r.queue = r.queue[1:]
		return ev, true
	}
	for {
		if ev, ok := pop(); ok {
			post(ev)
			continue
		}
		select {
		case <-r.wake:
		case <-r.quit:
			// 收工：把佇列排完再走（含 `done`——漏送等於橋端的卡片永遠不關）。
			// Close 那頭有總預算，卡住也不會拖著呼叫端。
			for {
				ev, ok := pop()
				if !ok {
					return
				}
				post(ev)
			}
		}
	}
}

// push 投遞事件：永不阻塞、永不 panic（ch/critical 都不會被關，見 quit 的說明）。
//
// 狀態機事件走 critical 佇列，不與泡泡搶同一個緩衝——否則 orchestrator 並行收工時，
// 大量泡泡會把「釋放 NPC」的事件擠掉。兩條都滿才丟，且關鍵事件被丟時記一筆。
func (r *OfficeReporter) push(kind, label, detail string) {
	r.pushEv(officeEvent{V: officeProtocolVersion, Agent: r.agent, Kind: kind, Label: label, Detail: detail})
}

func (r *OfficeReporter) pushEv(ev officeEvent) {
	critical := isCritical(ev.Kind, ev.Label)

	r.mu.Lock()
	switch {
	case len(r.queue) < bubbleQueueMax:
		// 一般情況：照順序入列。
	case critical && len(r.queue) < criticalQueueMax:
		// 泡泡額度用完，但關鍵事件還有餘裕——照樣入列，順序不變。
	case critical:
		// 連關鍵額度都滿＝橋掛很久了。此時丟仍優於反壓引擎，但要留痕跡。
		r.dropped.Add(1)
		r.mu.Unlock()
		return
	default:
		r.mu.Unlock()
		return // 泡泡滿了就丟（這類掉幀真的無害）
	}
	r.queue = append(r.queue, ev)
	r.mu.Unlock()

	select { // 喚醒 sender；訊號已在就不必重複
	case r.wake <- struct{}{}:
	default:
	}
}

// DroppedCritical 回報被丟掉的狀態機事件數。非零＝橋端狀態機可能與真實情況不同步
// （NPC 卡在 busy、卡片沒收），Close 時會記進日誌。
func (r *OfficeReporter) DroppedCritical() int64 { return r.dropped.Load() }

// Begin / End 標記一次任務的起訖（Reporter 介面沒有生命週期事件，由 caller 顯式呼叫）。
// Begin 標記任務開始；workDir 帶上該會話的工作目錄，讓辦公室看板直接標出產出落在哪。
func (r *OfficeReporter) Begin(task, workDir string) {
	r.push("start", schema.TruncRunes(task, 80, "…"), workDir)
}

// End 收工。costUSD 是本次任務的真實花費（呼叫端算增量）；≤0＝未知，不送（見 Cost 欄位）。
func (r *OfficeReporter) End(err error, costUSD float64) {
	ev := officeEvent{V: officeProtocolVersion, Agent: r.agent, Kind: "done", Label: "ok"}
	if costUSD > 0 {
		ev.Cost = costUSD
	}
	if err != nil {
		ev.Label, ev.Detail = "error", schema.TruncRunes(err.Error(), 120, "…")
	}
	r.pushEv(ev)
}

func (r *OfficeReporter) OnThinking(context.Context) { r.push("think", "", "") }
func (r *OfficeReporter) OnTurn(_ context.Context, turn int) {
	r.push("turn", fmt.Sprintf("%d", turn), "")
}

// 寫檔工具的參數就是【產出本身】——辦公室要把寫進去的內容當程式碼區塊顯示，120 字只夠看到
// 「package main」。其餘工具維持短截：bash 指令本來就短，read_file 的參數只是路徑，放寬沒意義。
var writeTools = map[string]bool{"write_file": true, "edit_file": true}

const (
	toolArgsMax  = 120
	writeArgsMax = 2400 // 約 60~80 行；更長的檔案看前段就夠判斷它在寫什麼
)

func (r *OfficeReporter) OnToolCall(_ context.Context, name, args string) {
	max := toolArgsMax
	if writeTools[name] {
		max = writeArgsMax
	}
	r.push("tool", name, schema.TruncRunes(args, max, "…"))
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
		// 全文供橋端工作串/報告面板用；泡泡顯示由橋端自行截短。8000 對齊「dashboard 看得到、
		// 辦公室也要看得到」——先前 2000 會把長會議報告的尾巴切掉，而行動指示常在尾巴
		// （dashboard 的 OnMessage 不截，落差就是這樣來的）。仍設上限：這是單筆 HTTP 事件，
		// 不是傳檔通道。
		r.push("msg", schema.TruncRunes(content, 8000, "…"), "")
	}
}

var _ Reporter = (*OfficeReporter)(nil)
