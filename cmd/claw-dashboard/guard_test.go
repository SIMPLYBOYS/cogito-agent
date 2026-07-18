package main

import (
	"strings"
	"testing"
)

func TestBindIsLoopback(t *testing.T) {
	loopback := []string{"127.0.0.1:8091", "localhost:8091", "[::1]:8091", "127.0.0.1:0"}
	for _, a := range loopback {
		if !bindIsLoopback(a) {
			t.Errorf("%q 應判為 loopback", a)
		}
	}
	// 非 loopback：空 host（所有介面）、對外 IP、0.0.0.0、無 port 分隔的怪字串
	notLoopback := []string{":8091", "0.0.0.0:8091", "192.168.1.5:8091", "10.0.0.1:8091", "garbage"}
	for _, a := range notLoopback {
		if bindIsLoopback(a) {
			t.Errorf("%q 不該判為 loopback", a)
		}
	}
}

// fail-closed 守衛：這是本階段唯一的 dashboard 存取控制，退回「不守衛」就是把無認證面板對外曝光。
func TestCheckBindSafety(t *testing.T) {
	// loopback → 放行（deny 為空）
	if deny := checkBindSafety("127.0.0.1:8091", false); deny != "" {
		t.Errorf("loopback 應放行，卻被拒：%s", deny)
	}
	// 非 loopback + 未 insecure → 必須拒絕（deny 非空），且理由要指出 SSH tunnel 與 INSECURE 逃生門
	deny := checkBindSafety("0.0.0.0:8091", false)
	if deny == "" {
		t.Fatal("非 loopback 綁定必須被拒（無認證面板不得對外曝光）")
	}
	if !strings.Contains(deny, "SSH tunnel") || !strings.Contains(deny, "COGITO_DASH_INSECURE") {
		t.Errorf("拒絕理由應給出逃生路線（SSH tunnel / INSECURE），got: %s", deny)
	}
	// 非 loopback + 顯式 insecure → 放行（自負風險）
	if deny := checkBindSafety("0.0.0.0:8091", true); deny != "" {
		t.Errorf("顯式 insecure 應放行，卻被拒：%s", deny)
	}
	// 空 host（:8091 = 所有介面）也算非 loopback，未 insecure 必須拒
	if checkBindSafety(":8091", false) == "" {
		t.Error("\":8091\"（所有介面）未 insecure 必須被拒")
	}
}
