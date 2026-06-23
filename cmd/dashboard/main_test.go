package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardHandler_RendersReport(t *testing.T) {
	dir := t.TempDir()
	report := `{
      "model":"claude-haiku-4-5","generated_at":"2026-06-23T10:00:00Z",
      "total":2,"passed":1,"pass_rate":0.5,"total_cost_usd":0.012,
      "results":[
        {"test_case_id":"t1","passed":true,"turn_count":2,"tool_error_count":0,"duration_ms":1200,"total_cost_usd":0.006},
        {"test_case_id":"t2","passed":false,"error_msg":"boom","turn_count":5,"tool_error_count":3,"duration_ms":3400,"total_cost_usd":0.006}
      ]}`
	if err := os.WriteFile(filepath.Join(dir, "bench-1.json"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	dashboardHandler(dir)(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("狀態碼應為 200，got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"claude-haiku-4-5", "50.0%", "t1", "t2", "boom"} {
		if !strings.Contains(body, want) {
			t.Errorf("頁面應包含 %q", want)
		}
	}
}

func TestDashboardHandler_EmptyDir(t *testing.T) {
	rec := httptest.NewRecorder()
	dashboardHandler(t.TempDir())(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("空目錄也應回 200，got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "沒有報告") {
		t.Error("空目錄應顯示友善提示")
	}
}

func TestLoadReports_SortedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := `{"model":"m","generated_at":"2026-06-20T10:00:00Z","total":1,"passed":1,"pass_rate":1,"results":[]}`
	newer := `{"model":"m","generated_at":"2026-06-23T10:00:00Z","total":1,"passed":0,"pass_rate":0,"results":[]}`
	_ = os.WriteFile(filepath.Join(dir, "bench-old.json"), []byte(older), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "bench-new.json"), []byte(newer), 0o644)

	reports, err := loadReports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].GeneratedAt != "2026-06-23T10:00:00Z" {
		t.Errorf("應按新→舊排序，got %+v", reports)
	}
}
