package cron

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func newSched(t *testing.T, r Runner) *Scheduler {
	t.Helper()
	s := New(t.TempDir(), r, "test")
	return s
}

// countingRunner 記錄被呼叫幾次與收到什麼，供鎖與執行流程的測試。
type countingRunner struct {
	mu     sync.Mutex
	calls  int
	block  chan struct{} // 非 nil 時 RunJob 會卡住，用來模擬「長任務持有鎖」
	prompt string
}

func (c *countingRunner) RunJob(_, prompt string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.prompt = prompt
	c.mu.Unlock()
	if c.block != nil {
		<-c.block
	}
	return "done", nil
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestStore_CRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claw", "cron.json")

	if err := Add(path, "巡檢", "0 9 * * 1-5", "檢查 CI"); err != nil {
		t.Fatalf("新增失敗: %v", err)
	}
	jobs, err := ReadJobs(path)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("預期 1 個 job，得 %d（err=%v）", len(jobs), err)
	}
	j := jobs[0]
	if j.Name != "巡檢" || j.Schedule != "0 9 * * 1-5" || !j.Enabled {
		t.Fatalf("欄位不符: %+v", j)
	}

	if err := Edit(path, j.ID, "巡檢2", "*/15 * * * *", "檢查 CI 與部署"); err != nil {
		t.Fatalf("編輯失敗: %v", err)
	}
	jobs, _ = ReadJobs(path)
	if jobs[0].Name != "巡檢2" || jobs[0].Schedule != "*/15 * * * *" {
		t.Fatalf("編輯未生效: %+v", jobs[0])
	}

	if err := SetResult(path, j.ID, "ok", "", "2026-01-01T09:00:00Z"); err != nil {
		t.Fatalf("回寫失敗: %v", err)
	}
	jobs, _ = ReadJobs(path)
	if jobs[0].LastStatus != "ok" || jobs[0].LastRun != "2026-01-01T09:00:00Z" {
		t.Fatalf("結果未回寫: %+v", jobs[0])
	}

	if err := Toggle(path, j.ID); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = ReadJobs(path); jobs[0].Enabled {
		t.Error("toggle 後應為停用")
	}
	if err := Remove(path, j.ID); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = ReadJobs(path); len(jobs) != 0 {
		t.Errorf("移除後應為空，得 %d", len(jobs))
	}
}

func TestStore_RejectsBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claw", "cron.json")
	for _, bad := range []string{"", "not a cron", "99 * * * *", "* * *"} {
		if err := Add(path, "x", bad, "p"); err == nil {
			t.Errorf("排程 %q 應被拒", bad)
		}
	}
	if err := Add(path, "", "* * * * *", "p"); err == nil {
		t.Error("空名稱應被拒")
	}
	if err := Add(path, "x", "* * * * *", ""); err == nil {
		t.Error("空任務描述應被拒")
	}
}

// due 的三種情境：未到點、到點、跨重啟由 LastRun 補跑。
func TestDue(t *testing.T) {
	hourly := Job{ID: "a", Schedule: "0 * * * *", Enabled: true}

	s := newSched(t, &countingRunner{})
	if s.due(hourly, at(t, "2026-01-01T09:30:00Z")) {
		t.Error("剛新增的 job 不該立刻觸發（不補跑過去的排程）")
	}
	if !s.due(hourly, at(t, "2026-01-01T10:00:00Z")) {
		t.Error("到點應觸發")
	}

	// 跨重啟：LastRun=08:00，現在 09:30 → 下次 09:00 已過，應補跑一次
	s2 := newSched(t, &countingRunner{})
	past := hourly
	past.LastRun = "2026-01-01T08:00:00Z"
	if !s2.due(past, at(t, "2026-01-01T09:30:00Z")) {
		t.Error("LastRun 之後已有排程點，應補跑")
	}

	s3 := newSched(t, &countingRunner{})
	if s3.due(Job{ID: "b", Schedule: "garbage", Enabled: true}, at(t, "2026-01-01T10:00:00Z")) {
		t.Error("壞運算式不該觸發")
	}
}

// 時區必須真的改變觸發時刻——雲端機器多為 UTC，設錯會在錯的時間跑。
func TestDue_RespectsTimezone(t *testing.T) {
	daily9 := Job{ID: "tz", Schedule: "0 9 * * *", Enabled: true, LastRun: "2026-01-01T00:00:00Z"}

	t.Setenv(TZKey, "Asia/Taipei") // UTC+8：09:00 台北 ＝ 01:00 UTC
	s := newSched(t, &countingRunner{})
	if s.due(daily9, at(t, "2026-01-01T00:30:00Z")) {
		t.Error("00:30Z＝台北 08:30，未到 09:00，不該觸發")
	}
	if !s.due(daily9, at(t, "2026-01-01T01:00:00Z")) {
		t.Error("01:00Z＝台北 09:00，應觸發")
	}

	t.Setenv(TZKey, "UTC")
	s2 := newSched(t, &countingRunner{})
	if s2.due(daily9, at(t, "2026-01-01T01:00:00Z")) {
		t.Error("時區設為 UTC 時，01:00Z 尚未到 09:00，不該觸發")
	}
	if !s2.due(daily9, at(t, "2026-01-01T09:00:00Z")) {
		t.Error("時區設為 UTC 時，09:00Z 應觸發")
	}
}

func TestLocation(t *testing.T) {
	t.Setenv(TZKey, "")
	if loc, warn := Location(); loc != time.Local || warn != "" {
		t.Errorf("未設應回本地時區且無警告，得 %v / %q", loc, warn)
	}
	t.Setenv(TZKey, "Asia/Taipei")
	if loc, warn := Location(); loc.String() != "Asia/Taipei" || warn != "" {
		t.Errorf("應載入 Asia/Taipei，得 %v / %q", loc, warn)
	}
	t.Setenv(TZKey, "Mars/Olympus")
	loc, warn := Location()
	if loc != time.Local {
		t.Errorf("無效時區應退回本地，得 %v", loc)
	}
	if warn == "" {
		t.Error("無效時區應回提示訊息")
	}
}

// 「執行中」標記不可被第二個 fire 覆寫或誤清。
func TestRunningMarker(t *testing.T) {
	s := newSched(t, &countingRunner{})
	if s.RunningID() != "" {
		t.Error("初始應為閒置")
	}
	if !s.tryMarkRunning("a") {
		t.Fatal("閒置時應可標記")
	}
	if s.tryMarkRunning("b") {
		t.Error("已有 job 在跑時不該讓 b 搶標記")
	}
	if s.RunningID() != "a" {
		t.Errorf("b 搶不到後仍應是 a，得 %q", s.RunningID())
	}
	s.clearRunning()
	if s.RunningID() != "" {
		t.Error("清除後應回到閒置")
	}
}

// 跨行程鎖：同一份 cron.json 有兩個排程器（bot + dashboard）時，同一輪只能有一個真的執行。
// 這是「bot 也跑排程器」的安全前提——沒有它，到點的 job 會被兩邊各跑一遍。
func TestTick_CrossProcessLock(t *testing.T) {
	ws := t.TempDir()
	path := ConfigPath(ws)
	if err := Add(path, "job", "* * * * *", "做事"); err != nil {
		t.Fatal(err)
	}
	jobs, _ := ReadJobs(path)
	// LastRun 設在過去 → 必定到點
	if err := SetResult(path, jobs[0].ID, "", "", "2020-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	rA := &countingRunner{block: make(chan struct{})}
	rB := &countingRunner{}
	a := New(ws, rA, "A")
	b := New(ws, rB, "B")

	// A 開始 tick 並卡在任務執行中（持有鎖）
	done := make(chan struct{})
	go func() { a.Tick(); close(done) }()

	// 等 A 真的進到 RunJob，確保鎖已被持有
	deadline := time.Now().Add(3 * time.Second)
	for rA.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if rA.count() != 1 {
		t.Fatalf("A 應已開始執行，實得 %d 次", rA.count())
	}

	// 此時 B tick：搶不到鎖，必須什麼都不做
	b.Tick()
	if got := rB.count(); got != 0 {
		t.Errorf("B 搶不到鎖時不該執行任務，實得 %d 次", got)
	}

	close(rA.block) // 放行 A
	<-done

	// A 放鎖後 B 才可能執行（此處只驗鎖已釋放：B 能取得）
	unlock, ok := acquireLock(path + ".lock")
	if !ok {
		t.Error("A 結束後鎖應已釋放")
	} else {
		unlock()
	}
}
