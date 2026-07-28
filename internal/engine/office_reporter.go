package engine

// OfficeReporter 把 agent 執行事件投影到像素辦公室（unity_demo 的 FastAPI 橋）。
// 契約：POST <url>/office/event {"agent","kind","label","detail"}，kind ∈
// start/turn/think/tool/result/error/msg/done——與 dashboard sseReporter 同一套事件詞彙，
// 子 agent 事件沿用 "[Subagent:名] 工具" 前綴（由橋端解析）。
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
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type officeEvent struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

type OfficeReporter struct {
	agent string
	ch    chan officeEvent
	done  chan struct{}
}

// NewOfficeReporter 建投影回報器。url 為橋的根位址（如 http://localhost:8123），
// agent 為對應 Unity NPC 的 persona id（如 p17）。
func NewOfficeReporter(url, agent string) *OfficeReporter {
	r := &OfficeReporter{agent: agent, ch: make(chan officeEvent, 64), done: make(chan struct{})}
	go r.send(strings.TrimRight(url, "/") + "/office/event")
	return r
}

// Close 排空緩衝後返回——程序退出前呼叫，確保 done 事件送達（橋不在線時連線秒拒，不會卡）。
func (r *OfficeReporter) Close() {
	close(r.ch)
	<-r.done
}

func (r *OfficeReporter) send(endpoint string) {
	defer close(r.done)
	client := &http.Client{Timeout: 2 * time.Second}
	for ev := range r.ch {
		b, _ := json.Marshal(ev)
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(b))
		if err != nil {
			continue // 橋不在線：靜默丟
		}
		resp.Body.Close()
	}
}

func (r *OfficeReporter) push(kind, label, detail string) {
	select {
	case r.ch <- officeEvent{Agent: r.agent, Kind: kind, Label: label, Detail: detail}:
	default: // 緩衝滿：丟事件保引擎不阻塞
	}
}

// Begin / End 標記一次任務的起訖（Reporter 介面沒有生命週期事件，由 caller 顯式呼叫）。
func (r *OfficeReporter) Begin(task string) { r.push("start", schema.TruncRunes(task, 80, "…"), "") }
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
	r.push(kind, name, schema.TruncRunes(result, 160, "…"))
}
func (r *OfficeReporter) OnMessage(_ context.Context, content string) {
	if content != "" {
		r.push("msg", schema.TruncRunes(content, 200, "…"), "")
	}
}

var _ Reporter = (*OfficeReporter)(nil)

// MultiReporter 把事件同時發給多個 Reporter（如終端 + 辦公室投影）。
type MultiReporter []Reporter

func (m MultiReporter) OnThinking(ctx context.Context) {
	for _, r := range m {
		r.OnThinking(ctx)
	}
}
func (m MultiReporter) OnTurn(ctx context.Context, t int) {
	for _, r := range m {
		r.OnTurn(ctx, t)
	}
}
func (m MultiReporter) OnToolCall(ctx context.Context, n, a string) {
	for _, r := range m {
		r.OnToolCall(ctx, n, a)
	}
}
func (m MultiReporter) OnToolResult(ctx context.Context, n, res string, e bool) {
	for _, r := range m {
		r.OnToolResult(ctx, n, res, e)
	}
}
func (m MultiReporter) OnMessage(ctx context.Context, c string) {
	for _, r := range m {
		r.OnMessage(ctx, c)
	}
}

var _ Reporter = (MultiReporter)(nil)
