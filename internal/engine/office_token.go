package engine

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// OfficeToken 是像素辦公室橋的 token（pixel-office 稽核 #4）：橋只收帶 token 的請求，擋掉打得到 127.0.0.1、
// 卻讀不到使用者檔案的東西（同機其他使用者、被騙去發請求的本機服務）。
// 來源與橋同一套：COGITO_OFFICE_TOKEN，否則 OFFICE_TOKEN_FILE，否則 ~/.pixel-office/token。每次都讀——橋換了 token 不必重啟 cogito。
func OfficeToken() string {
	if t := strings.TrimSpace(os.Getenv("COGITO_OFFICE_TOKEN")); t != "" {
		return t
	}
	path := os.Getenv("OFFICE_TOKEN_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, ".pixel-office", "token")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// PostOffice 把 JSON 送到橋，帶上 X-Office-Token。所有往辦公室送的請求都走這裡，別再各自 client.Post。
func PostOffice(client *http.Client, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if t := OfficeToken(); t != "" {
		req.Header.Set("X-Office-Token", t)
	}
	return client.Do(req)
}
