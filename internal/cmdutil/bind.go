package cmdutil

import "net"

// IsLoopback 判斷監聽位址是否只綁本機（loopback）。各 cmd 入口的 fail-closed 綁定守衛共用它——
// 「哪些位址算安全」只該有一個定義，否則新入口容易漏做（office HTTP 入口就漏過一次）。
//
// 【只認字面 loopback，不做 DNS 解析】空 host（如 ":8091"）＝所有介面＝非 loopback；"localhost" 視為
// loopback；其餘一律 net.ParseIP 後看 IsLoopback()。刻意不解析主機名——避免「名字解析成 loopback 但
// 實際綁到別處」的模糊，保守優先。
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // 沒有 host:port 分隔就整串當 host（下面多半 ParseIP 失敗 → 非 loopback，保守）
	}
	switch host {
	case "":
		return false // ":8091" = 綁所有介面
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
