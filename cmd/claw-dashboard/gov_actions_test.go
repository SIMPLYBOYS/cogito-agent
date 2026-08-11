package main

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// governance 放行動作：跨站被擋；無提案給 flash；技能名路徑穿越被守衛擋。
func TestGovActions_CSRFAndGuards(t *testing.T) {
	ws := t.TempDir()
	srv := newServer(nil, "", ws, nil)

	post := func(path, body, secFetch string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	govBody := func() string {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/governance", nil))
		return rec.Body.String()
	}

	// 跨站 → 403
	if c := post("/governance/apply-config", "", "cross-site"); c != 403 {
		t.Fatalf("跨站 apply-config 應 403，got %d", c)
	}

	// 同源、無提案 → 303 + flash
	if c := post("/governance/apply-config", "", "same-origin"); c != 303 {
		t.Fatalf("同源 apply-config 應 303，got %d", c)
	}
	if !strings.Contains(govBody(), "無調參提案可套用") {
		t.Error("無提案時應 flash 提示")
	}

	// 路徑穿越的技能名 → 擋（flash 無效）
	if c := post("/governance/promote-skill", "name="+url.QueryEscape("../evil"), "same-origin"); c != 303 {
		t.Fatalf("promote-skill 應 303，got %d", c)
	}
	if !strings.Contains(govBody(), "無效的技能名") {
		t.Error("路徑穿越的技能名應被守衛擋下")
	}
}

// 丟棄技能提案：真的移除、跨站被擋，且路徑穿越不能刪到 skills-proposed/ 以外。
//
// 最後一項是這條測試的重點：丟棄走的是 RemoveAll，守衛破了就不是「顯示錯東西」，
// 是刪掉別人的目錄。晉升與丟棄共用 resolveProposedSkill 就是為了不讓守衛只修一邊。
func TestGovActions_DiscardSkill(t *testing.T) {
	ws := t.TempDir()
	proposed := filepath.Join(ws, ".claw", "skills-proposed", "junk")
	if err := os.MkdirAll(proposed, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(proposed, "SKILL.md"), "---\nname: junk\ndescription: 不要的\n---\n正文")
	// 守衛破掉時會被穿越刪到的目標：skills-proposed 的兄弟目錄。
	sibling := filepath.Join(ws, ".claw", "skills")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	srv := newServer(nil, "", ws, nil)
	post := func(body, secFetch string) int {
		req := httptest.NewRequest("POST", "/governance/discard-skill", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}

	// 跨站 → 403，且提案不能被動到
	if c := post("name=junk", "cross-site"); c != 403 {
		t.Fatalf("跨站 discard-skill 應 403，got %d", c)
	}
	if _, err := os.Stat(proposed); err != nil {
		t.Fatal("跨站請求不該刪掉提案")
	}

	// 路徑穿越 → 擋下，兄弟目錄必須還在
	if c := post("name="+url.QueryEscape("../skills"), "same-origin"); c != 303 {
		t.Fatalf("discard-skill 應 303，got %d", c)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatal("路徑穿越刪到了 skills-proposed/ 以外的目錄")
	}

	// 正常丟棄 → 目錄消失、頁面不再列出
	if c := post("name=junk", "same-origin"); c != 303 {
		t.Fatalf("discard-skill 應 303，got %d", c)
	}
	if _, err := os.Stat(proposed); !os.IsNotExist(err) {
		t.Error("丟棄後提案目錄應該不存在")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/governance", nil))
	// 用只屬於清單項目的標記，別用 "junk"——flash 訊息本身就含技能名。
	if body := rec.Body.String(); strings.Contains(body, "junk/SKILL.md") {
		t.Error("丟棄後治理頁仍列出該提案")
	}
}
