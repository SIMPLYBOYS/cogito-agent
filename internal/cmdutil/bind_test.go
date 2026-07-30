package cmdutil

import "testing"

func TestIsLoopback(t *testing.T) {
	loopback := []string{"127.0.0.1:8091", "localhost:8091", "[::1]:8091", "127.0.0.1:0"}
	for _, a := range loopback {
		if !IsLoopback(a) {
			t.Errorf("%q 應判為 loopback", a)
		}
	}
	// 非 loopback：空 host（所有介面）、對外 IP、0.0.0.0、無 port 分隔的怪字串
	notLoopback := []string{":8091", "0.0.0.0:8091", "192.168.1.5:8091", "10.0.0.1:8091", "garbage"}
	for _, a := range notLoopback {
		if IsLoopback(a) {
			t.Errorf("%q 不該判為 loopback", a)
		}
	}
}
