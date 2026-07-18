package main

import (
	"fmt"
	"net"
)

// bindIsLoopback 判斷監聽位址是否只綁本機（loopback）。
//
// 【只認字面 loopback，不做 DNS 解析】空 host（如 ":8091"）＝所有介面＝非 loopback；"localhost" 視為
// loopback；其餘一律 net.ParseIP 後看 IsLoopback()。刻意不解析主機名——避免「名字解析成 loopback 但
// 實際綁到別處」的模糊，保守優先。
func bindIsLoopback(addr string) bool {
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

// checkBindSafety 是【fail-closed 守衛】：非 loopback 綁定時，除非顯式 insecure，否則回一段拒絕理由
// （非空字串＝呼叫端應拒絕啟動）。這是本階段唯一的 dashboard 存取控制——remote-auth 尚未實作，故不讓
// 一個無認證的 operator dashboard 意外對外曝光。理由與設計見 vault：C-Spec 的 §三「本階段 ① 的實作」。
func checkBindSafety(addr string, insecure bool) (deny string) {
	if bindIsLoopback(addr) {
		return "" // loopback：只有本機連得到，安全
	}
	if insecure {
		return "" // 顯式放行，自負風險（COGITO_DASH_INSECURE=1）
	}
	return fmt.Sprintf(
		"拒絕綁定非 loopback 位址 %q：operator dashboard 的 remote 存取需認證，尚未實作。\n"+
			"  ・遠端存取請用 SSH tunnel（推薦）：ssh -L 8091:127.0.0.1:8091 <host>\n"+
			"  ・真要對外曝光（無認證、自負風險）：設環境變數 COGITO_DASH_INSECURE=1",
		addr)
}
