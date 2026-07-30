package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	r.End(errors.New("網路中斷"))
	r.Close() // 排空後 got 即完整

	want := []officeEvent{
		{Agent: "p17", Kind: "start", Label: "盤點 TODO", Detail: "/tmp/wd"},
		{Agent: "p17", Kind: "tool", Label: "bash", Detail: `{"command":"grep TODO"}`},
		{Agent: "p17", Kind: "result", Label: "bash", Detail: "3 處"},
		{Agent: "p17", Kind: "error", Label: "bash", Detail: "boom"},
		{Agent: "p17", Kind: "msg", Label: "完成"},
		{Agent: "p17", Kind: "done", Label: "error", Detail: "網路中斷"},
	}
	if len(got) != len(want) {
		t.Fatalf("事件數 %d != %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("事件 %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// 橋不在線：事件靜默丟、Close 不卡死。
func TestOfficeReporterBridgeDown(t *testing.T) {
	r := NewOfficeReporter("http://127.0.0.1:1", "p17") // 連線秒拒
	r.Begin("x", "")
	r.End(nil)
	r.Close() // 卡住即測試逾時失敗
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
	r.End(nil)
	r.Close()

	// Close 後仍有事件到（模擬殘留的 goroutine）：必須靜默丟棄，不 panic
	r.OnToolCall(context.Background(), "bash", "ls")
	r.OnMessage(context.Background(), "遲到的訊息")
	r.OnTurn(context.Background(), 7)
	r.End(errors.New("遲到的收工"))

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
