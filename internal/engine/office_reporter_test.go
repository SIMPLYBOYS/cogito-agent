package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 合約測試：事件經 HTTP 送達橋端，kind/label 與 unity_demo 的投影表（backend/main.py）對齊。
func TestOfficeReporterContract(t *testing.T) {
	var mu sync.Mutex
	var got []officeEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/office/event" {
			t.Errorf("路徑錯誤: %s", r.URL.Path)
		}
		var ev officeEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}))
	defer srv.Close()

	ctx := context.Background()
	r := NewOfficeReporter(srv.URL+"/", "p17") // 尾斜線要被容忍
	r.Begin("盤點 TODO", "/tmp/wd")
	r.OnToolCall(ctx, "bash", `{"command":"grep TODO"}`)
	r.OnToolResult(ctx, "bash", "3 處", false)
	r.OnToolResult(ctx, "bash", "boom", true)
	r.OnMessage(ctx, "完成")
	r.End(errors.New("網路中斷"), 0.0231)
	r.Close() // 排空後 got 即完整

	want := []officeEvent{
		{V: officeProtocolVersion, Agent: "p17", Kind: "start", Label: "盤點 TODO", Detail: "/tmp/wd"},
		{V: officeProtocolVersion, Agent: "p17", Kind: "tool", Label: "bash", Detail: `{"command":"grep TODO"}`},
		{V: officeProtocolVersion, Agent: "p17", Kind: "result", Label: "bash", Detail: "3 處"},
		{V: officeProtocolVersion, Agent: "p17", Kind: "error", Label: "bash", Detail: "boom"},
		{V: officeProtocolVersion, Agent: "p17", Kind: "msg", Label: "完成"},
		// done 帶本次真實花費——外殼收工列據此顯示；0/未知不送（見 Cost 欄位註）
		{V: officeProtocolVersion, Agent: "p17", Kind: "done", Label: "error", Detail: "網路中斷", Cost: 0.0231},
	}
	if len(got) != len(want) {
		t.Fatalf("事件數 %d != %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("事件 %d: got %+v want %+v", i, got[i], want[i])
		}
		// 每個事件都必須帶協定版本——橋端靠它在協定演進時分支（見 docs/office-protocol.md）
		if got[i].V != officeProtocolVersion {
			t.Errorf("事件 %d 缺協定版本：v=%d", i, got[i].V)
		}
	}
}

// 橋不在線：事件靜默丟、Close 不卡死。
func TestOfficeReporterBridgeDown(t *testing.T) {
	r := NewOfficeReporter("http://127.0.0.1:1", "p17") // 連線秒拒
	r.Begin("x", "")
	r.End(nil, 0)
	r.Close() // 卡住即測試逾時失敗
}

// 花費為 0 / 未知時，done 的線上格式【不得出現 cost 鍵】——
// 顯示 $0.0000 會把「沒拿到 usage」偽裝成「免費」，投影估計值跟畫假進度條是同一種謊。
func TestOfficeReporterOmitsUnknownCost(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer srv.Close()

	r := NewOfficeReporter(srv.URL, "p05")
	r.End(nil, 0)
	r.Close()
	if len(bodies) != 1 || strings.Contains(bodies[0], `"cost"`) {
		t.Fatalf("cost 未知卻上了線: %v", bodies)
	}
}

// 半死的橋（接受連線但不回應）：Close 必須在預算內返回，不能無上限等排空。
// 它跑在 handleAgentRun 的 defer、在釋放頻道鎖【之前】——卡住等於「顯示任務完成，但下一則被擋」。
// 修復前實測：10 筆緩衝事件卡 20 秒（每筆吃滿 2s client timeout），64 筆滿載可達 ~128 秒。
func TestOfficeReporterCloseBoundedOnSlowBridge(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block // 收下請求卻不回應，直到測試收尾
	}))
	defer srv.Close()
	defer close(block) // LIFO：先解封 handler，srv.Close() 才不會等在途請求

	r := NewOfficeReporter(srv.URL, "p01")
	for i := 0; i < 20; i++ {
		r.OnToolCall(context.Background(), "bash", "ls")
	}

	start := time.Now()
	r.Close()
	if elapsed := time.Since(start); elapsed > closeDrainBudget+2*time.Second {
		t.Errorf("Close 應在 ~%v 內放生 sender 返回，實際卡了 %v", closeDrainBudget, elapsed)
	}
}

// 生命週期安全：reporter 交給引擎與子 agent 持有，「Close 之後不會再有事件」只是約定。
// 一個「掉幀無害」的狀態投影絕不該有能力弄垮 agent——故 Close 後 push、以及重複 Close，
// 都必須是 no-op 而非 panic（先前 Close 是 close(ch)，兩者都會 panic）。
func TestOfficeReporterCloseIsSafe(t *testing.T) {
	var got int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		got++
		mu.Unlock()
	}))
	defer srv.Close()

	r := NewOfficeReporter(srv.URL, "p01")
	r.Begin("任務", "/tmp/x")
	r.End(nil, 0)
	r.Close()

	// Close 後仍有事件到（模擬殘留的 goroutine）：必須靜默丟棄，不 panic
	r.OnToolCall(context.Background(), "bash", "ls")
	r.OnMessage(context.Background(), "遲到的訊息")
	r.OnTurn(context.Background(), 7)
	r.End(errors.New("遲到的收工"), 0)

	// 重複 Close：必須冪等，不 panic
	r.Close()
	r.Close()
}

// 併發下 push 與 Close 交錯也不能 panic（-race 下跑更有意義）。
func TestOfficeReporterConcurrentPushClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	r := NewOfficeReporter(srv.URL, "p01")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.OnToolCall(context.Background(), "bash", "ls")
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); r.Close() }() // 與上面的 push 同時發生
	wg.Wait()
	r.Close() // 再關一次也無妨
}

// 分級的存在理由：泡泡爆量時，釋放 NPC 的事件不能被擠掉。
// 沒有這層，orchestrator 並行收工（事件最密集的一刻）會讓 NPC 永遠卡在 busy。
func TestOfficeReporter_CriticalNotDroppedUnderBubbleFlood(t *testing.T) {
	var mu sync.Mutex
	var got []officeEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev officeEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // 橋端慢：讓緩衝真的塞住
	}))
	defer srv.Close()

	r := NewOfficeReporter(srv.URL, "p17")
	ctx := context.Background()

	// 遠超過泡泡緩衝（64）的洪水，中間夾雜關鍵事件
	r.Begin("產出 spec", "/w")
	for i := range 500 {
		r.OnThinking(ctx)
		r.OnToolCall(ctx, "read_file", "x")
		if i%100 == 0 { // 徵用／釋放各一對
			r.OnToolCall(ctx, "spawn_subagent:planner", "規劃")
			r.OnToolResult(ctx, "spawn_subagent:planner", "完成", false)
		}
	}
	r.End(nil, 0)
	r.Close()

	if d := r.DroppedCritical(); d != 0 {
		t.Errorf("關鍵事件不該被丟，丟了 %d 個", d)
	}

	mu.Lock()
	defer mu.Unlock()
	counts := map[string]int{}
	for _, e := range got {
		if isCritical(e.Kind, e.Label) {
			counts[e.Kind+"/"+e.Label]++
		}
	}
	// start 1、done 1、spawn 的徵用/釋放各 5 對
	for label, want := range map[string]int{
		"start/產出 spec":                 1,
		"done/ok":                       1,
		"tool/spawn_subagent:planner":   5,
		"result/spawn_subagent:planner": 5,
	} {
		if counts[label] != want {
			t.Errorf("關鍵事件 %q 送達 %d 次，應為 %d（泡泡洪水把它擠掉了）", label, counts[label], want)
		}
	}
	// 泡泡本來就該被丟——沒被丟表示這個測試沒真的造成壓力
	bubbles := len(got) - 12
	if bubbles >= 1000 {
		t.Errorf("泡泡幾乎沒掉（%d/1000），測試沒造成緩衝壓力，結論不成立", bubbles)
	}

	// 【保序】不可為了「不丟」而讓關鍵事件插隊：`done` 若跑到 `msg` 前面，橋端會在報告
	// 寫進卡片之前就把卡關掉；下一個任務的 `start` 若插到上一輪尾巴泡泡前面，泡泡會掛到
	// 錯的卡上。第一版改法就是這樣壞的，被 TestOfficeReporterContract 抓到。
	if got[0].Kind != "start" {
		t.Errorf("start 應為第一個事件，got %q", got[0].Kind)
	}
	if last := got[len(got)-1]; last.Kind != "done" {
		t.Errorf("done 應為最後一個事件（不能插隊到泡泡前面），got %q", last.Kind)
	}
	// 徵用一定在對應的釋放之前
	acquire, release := -1, -1
	for i, e := range got {
		if e.Label == "spawn_subagent:planner" {
			if e.Kind == "tool" && acquire < 0 {
				acquire = i
			}
			if e.Kind == "result" && release < 0 {
				release = i
			}
		}
	}
	if acquire < 0 || release < 0 || acquire > release {
		t.Errorf("徵用(%d)必須早於釋放(%d)", acquire, release)
	}
}

func TestIsCritical(t *testing.T) {
	for _, c := range []struct {
		kind, label string
		want        bool
	}{
		{"start", "任何任務", true},
		{"done", "ok", true},
		{"done", "error", true},
		{"tool", "spawn_subagent", true},         // 無名子 agent（探路者）
		{"tool", "spawn_subagent:planner", true}, // 具名
		{"result", "spawn_subagent:planner", true},
		{"error", "spawn_subagent:planner", true},
		{"tool", "read_file", false}, // 一般工具只影響泡泡
		{"result", "bash", false},
		{"think", "", false},
		{"turn", "3", false},
		// msg 升關鍵（2026-09-02）：它是報告本體，不是裝飾——看板多子 agent 並行收工的
		// 泡泡洪峰把訊息擠掉過（實際回報：對照 dashboard 才發現工作串少了訊息）。
		{"msg", "一段回覆", true},
	} {
		if got := isCritical(c.kind, c.label); got != c.want {
			t.Errorf("isCritical(%q,%q)=%v want %v", c.kind, c.label, got, c.want)
		}
	}
}

// 長訊息不在 2000 字被砍——行動指示常在尾巴（dashboard 不截，辦公室也不該截在報告腰上）。
func TestLongMessageNotCutAt2000(t *testing.T) {
	var mu sync.Mutex
	var got officeEvent
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
	}))
	defer srv.Close()
	r := NewOfficeReporter(srv.URL, "p05")
	long := strings.Repeat("內容", 1500) + "【尾巴的行動指示】"
	r.OnMessage(context.Background(), long)
	r.Close()
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got.Label, "【尾巴的行動指示】") {
		t.Fatalf("3000 字訊息的尾巴被砍掉了（長度=%d）", len([]rune(got.Label)))
	}
}
