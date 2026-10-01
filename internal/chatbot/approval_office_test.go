package chatbot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// pixel-office 稽核 #10：審批卡送到辦公室時要帶 approval 欄位（橋只認欄位，不認文字開頭）。
// office 頻道不再走 notify 的純文字——那跟模型的回覆同一條路、同一個形狀；Slack/TG 照常 notify 回原平台，再鏡射一份帶欄位的。
func TestApprovalPostsStructuredCardToOffice(t *testing.T) {
	got := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		body["_path"], body["_token"] = r.URL.Path, r.Header.Get("X-Office-Token")
		got <- body
	}))
	defer srv.Close()
	t.Setenv("COGITO_OFFICE_URL", srv.URL)
	t.Setenv("COGITO_OFFICE_TOKEN", "test-only-token") // 不去讀使用者真的 token 檔

	for _, tc := range []struct {
		conv       string
		wantNotify bool
	}{{"office:p17", false}, {"slack:C999", true}} {
		m := newTestMgr(time.Minute)
		notified := 0
		done := make(chan struct{})
		go func() {
			m.WaitForApproval("T-"+tc.conv, tc.conv, "bash", `{"command":"rm -rf build"}`, func(string) { notified++ })
			close(done)
		}()
		var body map[string]any
		select {
		case body = <-got:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s：辦公室沒收到審批卡", tc.conv)
		}
		m.ResolveApproval("T-"+tc.conv, true, "")
		<-done

		a, _ := body["approval"].(map[string]any)
		if a == nil || a["task_id"] != "T-"+tc.conv || a["tool"] != "bash" || a["params"] != `{"command":"rm -rf build"}` || a["timeout_s"] != float64(60) {
			t.Errorf("%s：審批欄位不對：%v", tc.conv, body["approval"])
		}
		if body["agent"] != tc.conv || body["_path"] != "/office/chat" || body["_token"] != "test-only-token" {
			t.Errorf("%s：來源／路徑／token 不對：%v %v %v", tc.conv, body["agent"], body["_path"], body["_token"])
		}
		if (notified > 0) != tc.wantNotify {
			t.Errorf("%s：notify 呼叫 %d 次，期望 %v（office 的純文字卡會被當成冒名）", tc.conv, notified, tc.wantNotify)
		}
	}
	select {
	case extra := <-got:
		t.Errorf("每張審批只該送一份到辦公室，多收到：%v", extra)
	default:
	}
}
