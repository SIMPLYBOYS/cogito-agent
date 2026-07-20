package sandbox

import (
	"os"
	"strings"
)

// 環境變數過濾：agent 的 bash 不該看到本行程的金鑰。
//
// 【問題】Go 的 exec.Command 若不設 cmd.Env，子行程會繼承【全部】父行程環境變數。bot／dashboard
// 的環境裡有 ANTHROPIC_API_KEY、SLACK_BOT_TOKEN 等，於是 agent 一句 `env` 或 `echo $ANTHROPIC_API_KEY`
// 就把憑證讀出來——不需要任何漏洞，正常功能即可外洩。提示注入下更是直接的外傳管道。
//
// 【為何用白名單而非黑名單】黑名單擋得掉今天已知的 *_KEY / *_TOKEN，擋不掉使用者明天加的
// `STRIPE_LIVE`。白名單預設拒絕，新增的祕密自動就在牆外。
//
// 【邊界說明】這是【降低洩漏面】，不是隔離邊界。真正的邊界是 COGITO_SANDBOX=docker——容器不繼承
// 宿主機環境，本過濾在該模式下等於免費。host 模式本就是「便利但無隔離」，此處只是讓它別把金鑰
// 直接攤在 agent 面前。
const passExtraKey = "COGITO_SANDBOX_ENV_PASS"

// baseEnvKeys 是一般命令跑得起來所需的最小集合。刻意不含任何憑證類變數。
var baseEnvKeys = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TMPDIR", "TZ", "PWD",
	"LANG", "LC_ALL", "LC_CTYPE",
	// Go 工具鏈（agent 常跑 build/test）。逐一列出而非用 "GO" 前綴——前綴會誤放 GOOGLE_API_KEY 之類。
	"GOPATH", "GOROOT", "GOBIN", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "GOSUMDB", "GOTOOLCHAIN",
	// 快取/設定目錄：npx、uvx 等會用到（否則每次重抓套件，甚至直接失敗）。非祕密。
	"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
	// TLS 憑證位置（某些環境需明指），非祕密。
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	// 代理設定：擋掉的話公司網路後面會整個連不出去。
	// 註：代理 URL 可以內嵌帳密（http://user:pass@…），這是刻意的取捨——寧可放行也不要讓網路全斷。
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
}

// FilteredEnv 依白名單組出要傳給【我們自己拉起的子行程】的環境變數——agent 的 bash 與 MCP
// server 子行程都走這裡。
//
// 使用者可用 COGITO_SANDBOX_ENV_PASS（逗號分隔）補上自家工具鏈需要的變數，例如
// COGITO_SANDBOX_ENV_PASS=NODE_ENV,CARGO_HOME。
func FilteredEnv() []string { return filteredEnv() }

func filteredEnv() []string {
	allow := map[string]bool{}
	for _, k := range baseEnvKeys {
		allow[k] = true
	}
	for _, k := range strings.Split(os.Getenv(passExtraKey), ",") {
		if k = strings.TrimSpace(k); k != "" {
			allow[k] = true
		}
	}

	out := make([]string, 0, len(allow))
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && allow[k] {
			out = append(out, kv)
		}
	}
	return out
}
