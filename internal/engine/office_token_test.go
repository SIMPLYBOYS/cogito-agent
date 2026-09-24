package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPostOfficeSendsToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Office-Token")
	}))
	defer srv.Close()

	// 檔案來源（橋預設的 ~/.pixel-office/token 用 OFFICE_TOKEN_FILE 指到暫存檔）
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COGITO_OFFICE_TOKEN", "")
	t.Setenv("OFFICE_TOKEN_FILE", f)
	resp, err := PostOffice(srv.Client(), srv.URL+"/office/event", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "from-file" {
		t.Fatalf("want token from file, got %q", got)
	}

	// 環境變數優先
	t.Setenv("COGITO_OFFICE_TOKEN", "from-env")
	resp, err = PostOffice(srv.Client(), srv.URL+"/office/event", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "from-env" {
		t.Fatalf("want env token, got %q", got)
	}
}
