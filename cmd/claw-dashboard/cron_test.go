package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/cron"
)

// 排程的核心邏輯（store／due／時區／鎖）測在 internal/cron；這裡只測面板這一層：
// CSRF、未啟用排程器時的行為、頁面渲染。
func TestCronHandlers(t *testing.T) {
	ws := t.TempDir()
	srv := newServer(nil, "", ws, nil) // chat=nil → 排程器未啟用
	path := cron.ConfigPath(ws)

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

	// 跨站被擋且不寫入
	if rec := post("/cron/add", url.Values{"name": {"x"}, "schedule": {"* * * * *"}, "prompt": {"p"}}, true); rec.Code != 403 {
		t.Errorf("跨站新增應 403，得 %d", rec.Code)
	}
	if jobs, _ := cron.ReadJobs(path); len(jobs) != 0 {
		t.Error("跨站請求不該寫入")
	}

	// 同源可新增
	if rec := post("/cron/add", url.Values{"name": {"巡檢"}, "schedule": {"0 9 * * *"}, "prompt": {"檢查"}}, false); rec.Code != 303 {
		t.Errorf("新增應 303，得 %d", rec.Code)
	}
	jobs, _ := cron.ReadJobs(path)
	if len(jobs) != 1 {
		t.Fatalf("預期寫入 1 個 job，得 %d", len(jobs))
	}

	// 排程器未啟用時「立即執行」不執行、只提示
	if rec := post("/cron/run", url.Values{"id": {jobs[0].ID}}, false); rec.Code != 303 {
		t.Errorf("立即執行應 303，得 %d", rec.Code)
	}
	page := httptest.NewRecorder()
	srv.ServeHTTP(page, httptest.NewRequest("GET", "/cron", nil))
	body := page.Body.String()
	if !strings.Contains(body, "排程器未啟用") {
		t.Error("未啟用 chat 時應提示排程器未啟用")
	}
	if !strings.Contains(body, "巡檢") {
		t.Error("cron 頁應列出 job")
	}
}

// 未明設時區時要「講大聲」：標題警示 + 設定區預設展開；設好之後回歸安靜的緊湊顯示。
func TestCronPage_TimezoneNudge(t *testing.T) {
	srv := newServer(nil, "", t.TempDir(), nil)
	get := func() string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/cron", nil))
		return rec.Body.String()
	}

	t.Setenv(cron.TZKey, "")
	body := get()
	if !strings.Contains(body, "未明設時區") {
		t.Error("未設時區時標題應警示")
	}
	if !strings.Contains(body, `class="mcpedit" open`) {
		t.Error("未設時區時設定區應預設展開")
	}

	t.Setenv(cron.TZKey, "Asia/Taipei")
	body = get()
	if strings.Contains(body, "未明設時區") {
		t.Error("設好時區後不該再警示")
	}
	if !strings.Contains(body, "Asia/Taipei") {
		t.Error("設好後標題應顯示該時區")
	}
	if strings.Contains(body, `class="mcpedit" open`) {
		t.Error("設好後設定區應收合（維持緊湊顯示）")
	}
}

// 破壞性動作必須兩段式：頁面上「移除」只是展開，真正送出的是裡面另一顆按鈕。
// 這條擋的是「把確認層拿掉」的回歸——單一按鈕直接送 POST 就等於誤點即刪。
func TestCronPage_RemoveNeedsConfirm(t *testing.T) {
	ws := t.TempDir()
	if err := cron.Add(cron.ConfigPath(ws), "巡檢", "0 9 * * *", "檢查"); err != nil {
		t.Fatal(err)
	}
	srv := newServer(nil, "", ws, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/cron", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `<details class="danger"><summary>移除</summary>`) {
		t.Error("移除應包在 details.danger 的確認層裡")
	}
	if !strings.Contains(body, "確定移除") {
		t.Error("確認層應有明確的二次確認按鈕")
	}
	if !strings.Contains(body, "無法復原") {
		t.Error("確認層應說明後果")
	}
	// 表單本身仍在（確認後才送得出去），但不可有「直接就是 gact ghost 移除」的單擊路徑
	if strings.Contains(body, `<button type="submit" class="gact ghost">移除</button>`) {
		t.Error("不該還留著單擊即刪的移除按鈕")
	}
}
