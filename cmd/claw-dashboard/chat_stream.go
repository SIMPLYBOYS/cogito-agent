package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// sseHub 是單一 operator run 的事件緩衝。sseReporter 在 agent 執行時 push 事件；/chat/stream 的每個
// 連線輪詢 since() 取新事件推給瀏覽器。用「緩衝 + 尾隨」而非 pub/sub channel：晚連的客戶端也能補齊
// 已發生的事件、且連線結束即回收（無 goroutine/channel 洩漏）。token 級串流需 provider 支援（目前
// Generate 阻塞、不吐 delta），故這裡串「執行事件」而非逐字。
type sseHub struct {
	mu      sync.Mutex
	events  []string
	running bool
}

func (h *sseHub) begin() { h.mu.Lock(); h.events = nil; h.running = true; h.mu.Unlock() }
func (h *sseHub) end()   { h.mu.Lock(); h.running = false; h.mu.Unlock() }
func (h *sseHub) push(data string) {
	h.mu.Lock()
	h.events = append(h.events, data)
	h.mu.Unlock()
}
func (h *sseHub) isRunning() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.running }

// since 回傳索引 n 之後的新事件、目前是否仍在跑、事件總數。
func (h *sseHub) since(n int) (evs []string, running bool, total int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n < 0 {
		n = 0
	}
	if n < len(h.events) {
		evs = append(evs, h.events[n:]...)
	}
	return evs, h.running, len(h.events)
}

// evJSON 把事件編成單行 JSON（SSE 的 data 不能含裸換行；JSON 轉義代為處理）。
func evJSON(kind, label string) string {
	b, _ := json.Marshal(map[string]string{"kind": kind, "label": label})
	return string(b)
}

// sseReporter 把 engine 執行事件推進 hub，供瀏覽器即時串流。實作 engine.Reporter。
type sseReporter struct{ hub *sseHub }

func (r sseReporter) OnTurn(_ context.Context, turn int) {
	r.hub.push(evJSON("turn", fmt.Sprintf("回合 %d", turn)))
}
func (r sseReporter) OnThinking(_ context.Context) { r.hub.push(evJSON("think", "思考中…")) }
func (r sseReporter) OnToolCall(_ context.Context, name, args string) {
	r.hub.push(evJSON("tool", name+"  "+schema.TruncRunes(args, 120, "…")))
}
func (r sseReporter) OnToolResult(_ context.Context, name, result string, isErr bool) {
	kind := "result"
	if isErr {
		kind = "error"
	}
	r.hub.push(evJSON(kind, name+" → "+schema.TruncRunes(result, 160, "…")))
}
func (r sseReporter) OnMessage(_ context.Context, content string) {
	if content != "" {
		r.hub.push(evJSON("msg", content))
	}
}

// multiReporter 把事件同時打到終端（跑 dashboard 的 console）與 SSE hub（瀏覽器）。
type multiReporter struct{ rs []engine.Reporter }

func (m multiReporter) OnTurn(ctx context.Context, t int) {
	for _, r := range m.rs {
		r.OnTurn(ctx, t)
	}
}
func (m multiReporter) OnThinking(ctx context.Context) {
	for _, r := range m.rs {
		r.OnThinking(ctx)
	}
}
func (m multiReporter) OnToolCall(ctx context.Context, n, a string) {
	for _, r := range m.rs {
		r.OnToolCall(ctx, n, a)
	}
}
func (m multiReporter) OnToolResult(ctx context.Context, n, res string, e bool) {
	for _, r := range m.rs {
		r.OnToolResult(ctx, n, res, e)
	}
}
func (m multiReporter) OnMessage(ctx context.Context, c string) {
	for _, r := range m.rs {
		r.OnMessage(ctx, c)
	}
}

// chatStream 是 SSE 端點：尾隨 hub、把當前 run 的事件逐筆推給瀏覽器；run 結束送 done 收線。
// 用伺服端 150ms tick 輪詢共享緩衝（非阻塞 channel），簡單且無洩漏：run 結束或客戶端斷線即回傳。
func (s *server) chatStream(w http.ResponseWriter, r *http.Request) {
	if s.chat == nil {
		http.NotFound(w, r)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sent := 0
	for {
		evs, running, total := s.chat.hub.since(sent)
		for _, e := range evs {
			fmt.Fprintf(w, "data: %s\n\n", e)
			sent++
		}
		fl.Flush()
		if !running && sent >= total {
			fmt.Fprint(w, "event: done\ndata: {}\n\n")
			fl.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(60 * time.Millisecond): // 逐字串流：小 tick 讓 token 增量順滑冒出
		}
	}
}

// chatJS 提供 chat 頁的串流客戶端（用 EventSource 接 /chat/stream，逐事件塞進 #live）。獨立檔以走
// script-src 'self'（不放 inline script）。所有動態文字用 textContent 塞入 → 天然免 XSS。
func (s *server) chatJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	_, _ = w.Write([]byte(chatJSSrc))
}

const chatJSSrc = `(function () {
  var live = document.getElementById('live');
  if (!live) return;
  var ICON = { turn:'⟳', think:'◌', tool:'▸', result:'✓', error:'✗', msg:'◆' };
  var cur = null;    // 正在逐字串流的 msg 泡（delta 累進、msg/其他事件收尾）
  var active = null; // 主流最新一列（脈動）；下一事件到來即移交
  // 子 agent 委派卡：spawn_subagent 上工開卡、result 收工關卡。並行委派時多張卡同時脈動＝多 agent 同時工作。
  // 卡以 agent_type 為鍵；子 agent 內部事件（[Subagent:<type>] …）依此歸屬到各自的卡（並行時才不會混流）。
  var cards = {};    // agent_type -> { box, st, body }
  var order = [];    // 開啟中的卡（agent_type），收卡用
  function lastCard() { return order.length ? cards[order[order.length - 1]] : null; }
  function markActive(r) {
    if (active && active !== r) active.classList.remove('active');
    active = r; if (r) r.classList.add('active');
  }
  function stopActive() { if (active) { active.classList.remove('active'); active = null; } }
  function scroll() { window.scrollTo(0, document.body.scrollHeight); }
  function agentType(label) {                         // 開/收事件都帶 spawn_subagent:<type> 後綴；退回從 args 取
    var m = /^spawn_subagent:(\S+)/.exec(label); if (m) return m[1];
    m = /"agent_type"\s*:\s*"([^"]+)"/.exec(label); return m ? m[1] : '子 agent';
  }
  function subRole(label) { var m = /^\[Subagent:([^\]]+)\]/.exec(label); return m ? m[1] : null; }
  function stripSub(label) { return label.replace(/^\[Subagent(:[^\]]+)?\]\s*/, ''); }
  function openCard(type) {                           // 委派上工：開一張兄弟並列的脈動卡（不巢狀）
    stopActive();
    if (cards[type]) return;                          // 同型別已有開啟卡（罕見）→ 沿用，事件併入
    var box = document.createElement('div'); box.className = 'subagent active';
    var head = document.createElement('div'); head.className = 'subhead';
    var badge = document.createElement('span'); badge.className = 'sbadge'; badge.textContent = '🤖 ' + type + ' 專員';
    var st = document.createElement('span'); st.className = 'sstate';
    var sp = document.createElement('span'); sp.className = 'spin';
    var stx = document.createElement('span'); stx.textContent = '審查中';
    st.appendChild(sp); st.appendChild(stx); head.appendChild(badge); head.appendChild(st);
    var body = document.createElement('div'); body.className = 'subbody';
    box.appendChild(head); box.appendChild(body);
    live.appendChild(box); cards[type] = { box:box, st:st, body:body }; order.push(type); scroll();
  }
  function closeCard(type, ok, report) {              // 委派收工：依型別收對卡（收工事件帶 spawn_subagent:<type>），
    var key = (type && cards[type]) ? type : (order.length ? order[0] : null); // 取不到型別才退最舊卡
    if (key == null) return;
    var idx = order.indexOf(key); if (idx >= 0) order.splice(idx, 1);
    var c = cards[key]; delete cards[key]; if (!c) return;
    c.box.classList.remove('active');
    c.st.className = 'sstate ' + (ok ? 'done' : 'fail');
    c.st.textContent = ok ? '✓ 報告完成' : '✗ 失敗';
    if (report) { var d = document.createElement('div'); d.className = 'subreport'; d.textContent = '報告：' + report; c.body.appendChild(d); }
    scroll();
  }
  function row(kind, text, into, act) {               // into 省略＝主流 live；act＝是否標記脈動（主流才要）
    var r = document.createElement('div'); r.className = 'ev ' + kind;
    var ic = document.createElement('span'); ic.className = 'ic'; ic.textContent = ICON[kind] || '·';
    var tx = document.createElement('span'); tx.className = 'tx'; tx.textContent = text;
    r.appendChild(ic); r.appendChild(tx); (into || live).appendChild(r); if (act) markActive(r); return r;
  }
  function endStream() { if (cur) { cur.classList.remove('streaming'); cur = null; } }
  var es = new EventSource('/chat/stream');
  es.onmessage = function (e) {
    var ev; try { ev = JSON.parse(e.data); } catch (x) { return; }
    var lbl = ev.label || '';
    // 子 agent 內部事件：縮排進對應角色的卡（找不到卡→落到最後開的卡→再退主流）；顯示時剝掉前綴
    if (lbl.indexOf('[Subagent') === 0) {
      endStream(); var c = cards[subRole(lbl)] || lastCard();
      if (c) row(ev.kind, stripSub(lbl), c.body, false); else row(ev.kind, stripSub(lbl), null, true);
      scroll(); return;
    }
    // 委派：tool＝上工開卡、result/error＝收工關卡
    if (ev.kind === 'tool' && lbl.indexOf('spawn_subagent') === 0) { endStream(); openCard(agentType(lbl)); return; }
    if ((ev.kind === 'result' || ev.kind === 'error') && lbl.indexOf('spawn_subagent') === 0 && order.length) {
      endStream(); var i = lbl.indexOf(' → '); closeCard(agentType(lbl), ev.kind === 'result', i >= 0 ? lbl.slice(i + 3) : ''); return;
    }
    if (ev.kind === 'delta') {                         // 逐字：累進當前 msg 泡
      if (!cur) { cur = row('msg', '', null, true); cur.className = 'ev msg streaming active'; markActive(cur); }
      cur.querySelector('.tx').textContent += lbl;
      scroll(); return;
    }
    if (ev.kind === 'msg') {                            // 訊息定稿：收尾串流泡（不重複）
      if (cur) { cur.querySelector('.tx').textContent = lbl; endStream(); }
      else { row('msg', lbl, null, true); }
      scroll(); return;
    }
    endStream(); row(ev.kind, lbl, null, true); scroll(); // 其他主流事件：先收尾串流泡再插入
  };
  es.addEventListener('done', function () {
    es.close(); endStream(); stopActive();
    while (order.length) closeCard(null, true, '');    // 收尾任何未關的子 agent 卡（退最舊）
    var b = document.getElementById('runbanner');
    if (b) { b.className = 'banner done'; b.textContent = '✓ 完成'; }
    var f = document.getElementById('composer');
    if (f) {
      var t = f.querySelector('textarea'); if (t) { t.disabled = false; t.focus(); }
      var s = f.querySelector('button'); if (s) s.disabled = false;
    }
  });
  es.onerror = function () { es.close(); };
})();`
