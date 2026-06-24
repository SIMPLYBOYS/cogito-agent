package cmdutil

import "testing"

func TestBootstrap_FlushIdempotent(t *testing.T) {
	flush := Bootstrap("test-svc")
	if flush == nil {
		t.Fatal("flush 不應為 nil")
	}
	// once 去重：重複呼叫（如 defer + 顯式）不應 panic。
	flush()
	flush()
}
