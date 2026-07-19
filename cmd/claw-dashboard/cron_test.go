package main

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCronStore_CRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claw", "cron.json")

	if err := addCronJob(path, "巡檢", "0 9 * * 1-5", "檢查 CI"); err != nil {
		t.Fatalf("新增失敗: %v", err)
	}
	jobs, err := readCronJobs(path)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("預期 1 個 job，得 %d（err=%v）", len(jobs), err)
	}
	j := jobs[0]
	if j.Name != "巡檢" || j.Schedule != "0 9 * * 1-5" || !j.Enabled {
		t.Fatalf("欄位不符: %+v", j)
	}

	// 編輯：只動名稱/排程/描述
	if err := editCronJob(path, j.ID, "巡檢2", "*/15 * * * *", "檢查 CI 與部署"); err != nil {
		t.Fatalf("編輯失敗: %v", err)
	}
	jobs, _ = readCronJobs(path)
	if jobs[0].Name != "巡檢2" || jobs[0].Schedule != "*/15 * * * *" {
		t.Fatalf("編輯未生效: %+v", jobs[0])
	}

	// 執行結果回寫
	if err := setCronResult(path, j.ID, "ok", "", "2026-01-01T09:00:00Z"); err != nil {
		t.Fatalf("回寫失敗: %v", err)
	}
	jobs, _ = readCronJobs(path)
	if jobs[0].LastStatus != "ok" || jobs[0].LastRun != "2026-01-01T09:00:00Z" {
		t.Fatalf("結果未回寫: %+v", jobs[0])
	}

	// 停用 → 移除
	if err := toggleCronJob(path, j.ID); err != nil {
		t.Fatal(err)
	}
	jobs, _ = readCronJobs(path)
	if jobs[0].Enabled {
		t.Error("toggle 後應為停用")
	}
	if err := removeCronJob(path, j.ID); err != nil {
		t.Fatal(err)
	}
	if jobs, _ = readCronJobs(path); len(jobs) != 0 {
		t.Errorf("移除後應為空，得 %d", len(jobs))
	}
}

func TestCronStore_RejectsBadSchedule(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claw", "cron.json")
	for _, bad := range []string{"", "not a cron", "99 * * * *", "* * *"} {
		if err := addCronJob(path, "x", bad, "p"); err == nil {
			t.Errorf("排程 %q 應被拒", bad)
		}
	}
	// 名稱/描述空也該擋
	if err := addCronJob(path, "", "* * * * *", "p"); err == nil {
		t.Error("空名稱應被拒")
	}
	if err := addCronJob(path, "x", "* * * * *", ""); err == nil {
		t.Error("空任務描述應被拒")
	}
}

// due 的三種情境：未到點、到點、跨重啟由 LastRun 補跑。
func TestCronDue(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	newSched := func() *cronScheduler {
		return &cronScheduler{base: map[string]time.Time{}, now: time.Now}
	}

	hourly := cronJob{ID: "a", Schedule: "0 * * * *", Enabled: true}

	// 新 job 首次觀察於 09:30 → 基準=09:30，下次 10:00，此刻不該跑（不補跑過去的排程）
	s := newSched()
	if s.due(hourly, at("2026-01-01T09:30:00Z")) {
		t.Error("剛新增的 job 不該立刻觸發")
	}
	// 同一個 scheduler，時間走到 10:00 → 到點
	if !s.due(hourly, at("2026-01-01T10:00:00Z")) {
		t.Error("到點應觸發")
	}

	// 跨重啟：LastRun=08:00，現在 09:30 → 下次 09:00 已過，應補跑一次
	s2 := newSched()
	past := hourly
	past.LastRun = "2026-01-01T08:00:00Z"
	if !s2.due(past, at("2026-01-01T09:30:00Z")) {
		t.Error("LastRun 之後已有排程點，應補跑")
	}

	// 壞運算式：不跑（不 panic）
	s3 := newSched()
	if s3.due(cronJob{ID: "b", Schedule: "garbage", Enabled: true}, at("2026-01-01T10:00:00Z")) {
		t.Error("壞運算式不該觸發")
	}
}

func TestCronHandlers(t *testing.T) {
	ws := t.TempDir()
	srv := newServer(nil, "", ws, nil) // chat=nil → 排程器未啟用
	path := cronConfigPath(ws)

	post := func(p string, form url.Values, crossSite bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", p, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if crossSite {
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	// 跨站被擋
	if rec := post("/cron/add", url.Values{"name": {"x"}, "schedule": {"* * * * *"}, "prompt": {"p"}}, true); rec.Code != 403 {
		t.Errorf("跨站新增應 403，得 %d", rec.Code)
	}
	if jobs, _ := readCronJobs(path); len(jobs) != 0 {
		t.Error("跨站請求不該寫入")
	}

	// 同源可新增
	if rec := post("/cron/add", url.Values{"name": {"巡檢"}, "schedule": {"0 9 * * *"}, "prompt": {"檢查"}}, false); rec.Code != 303 {
		t.Errorf("新增應 303，得 %d", rec.Code)
	}
	jobs, _ := readCronJobs(path)
	if len(jobs) != 1 {
		t.Fatalf("預期寫入 1 個 job，得 %d", len(jobs))
	}

	// 排程器未啟用時「立即執行」不執行、只提示
	rec := post("/cron/run", url.Values{"id": {jobs[0].ID}}, false)
	if rec.Code != 303 {
		t.Errorf("立即執行應 303，得 %d", rec.Code)
	}
	page := httptest.NewRecorder()
	srv.ServeHTTP(page, httptest.NewRequest("GET", "/cron", nil))
	if !strings.Contains(page.Body.String(), "排程器未啟用") {
		t.Error("未啟用 chat 時應提示排程器未啟用")
	}
	if !strings.Contains(page.Body.String(), "巡檢") {
		t.Error("cron 頁應列出 job")
	}
}

// 「執行中」標記不可被第二個 fire 覆寫或誤清——否則被 operator chat 擋掉的那次會把真正
// 在跑的那個標記清掉，UI 顯示閒置。
func TestCronRunningMarker(t *testing.T) {
	s := &cronScheduler{base: map[string]time.Time{}, now: time.Now}

	if s.runningID() != "" {
		t.Error("初始應為閒置")
	}
	if !s.tryMarkRunning("a") {
		t.Fatal("閒置時應可標記")
	}
	if s.runningID() != "a" {
		t.Errorf("應標記為 a，得 %q", s.runningID())
	}
	if s.tryMarkRunning("b") {
		t.Error("已有 job 在跑時不該讓 b 搶標記")
	}
	if s.runningID() != "a" {
		t.Errorf("b 搶不到後仍應是 a，得 %q", s.runningID())
	}
	s.clearRunning()
	if s.runningID() != "" {
		t.Error("清除後應回到閒置")
	}
}
