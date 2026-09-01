package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 假 Tavily：/search 回兩筆、/extract 回一頁；順手記下收到的 query 供斷言。
func fakeTavily(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch r.URL.Path {
		case "/search":
			_, _ = w.Write([]byte(`{"results":[
				{"title":"Go 1.24 Release Notes","url":"https://go.dev/doc/go1.24","content":"Go 1.24 adds ..."},
				{"title":"別家的整理","url":"https://blog.example.com/go124","content":"二手摘要 ..."}]}`))
		case "/extract":
			_, _ = w.Write([]byte(`{"results":[{"url":"https://go.dev/doc/go1.24","raw_content":"The Go 1.24 release ..."}],"failed_results":[]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestWebSearchFormatsResults(t *testing.T) {
	srv, got := fakeTavily(t)
	ws := &WebSearchTool{key: "k", base: srv.URL}
	out, err := ws.Execute(context.Background(), json.RawMessage(`{"query":"go 1.24 release notes"}`))
	if err != nil {
		t.Fatal(err)
	}
	// 標題與網址都要在——沒有網址，模型就沒辦法走 fetch_url 追一手來源
	for _, want := range []string{"Go 1.24 Release Notes", "https://go.dev/doc/go1.24", "fetch_url"} {
		if !strings.Contains(out, want) {
			t.Errorf("輸出缺 %q:\n%s", want, out)
		}
	}
	if (*got)["query"] != "go 1.24 release notes" {
		t.Errorf("query 沒送到: %v", (*got)["query"])
	}
}

func TestWebSearchCapsQuery(t *testing.T) {
	srv, got := fakeTavily(t)
	ws := &WebSearchTool{key: "k", base: srv.URL}
	long := strings.Repeat("內", 900)
	if _, err := ws.Execute(context.Background(), json.RawMessage(`{"query":"`+long+`"}`)); err != nil {
		t.Fatal(err)
	}
	// 查詢封頂：查詢該是問題，不是一整段被夾帶外滲的內文
	if q := (*got)["query"].(string); len([]rune(q)) > webQueryMax+1 {
		t.Errorf("query 沒被截斷：%d runes", len([]rune(q)))
	}
}

func TestFetchURL(t *testing.T) {
	srv, _ := fakeTavily(t)
	fu := &FetchURLTool{key: "k", base: srv.URL}
	out, err := fu.Execute(context.Background(), json.RawMessage(`{"url":"https://go.dev/doc/go1.24"}`))
	if err != nil || !strings.Contains(out, "The Go 1.24 release") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := fu.Execute(context.Background(), json.RawMessage(`{"url":"ftp://x"}`)); err == nil {
		t.Error("非 http/https 應拒收")
	}
}
