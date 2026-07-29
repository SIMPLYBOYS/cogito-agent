package chatbot

import "testing"

// COGITO_OFFICE_URL 未設＝不投影（回 nil）；設了＝建 reporter（Close 不卡）。
func TestNewOfficeReporterEnvGate(t *testing.T) {
	t.Setenv("COGITO_OFFICE_URL", "")
	if newOfficeReporter("slack:C1") != nil {
		t.Fatal("未設 URL 應回 nil")
	}
	t.Setenv("COGITO_OFFICE_URL", "http://127.0.0.1:1")
	r := newOfficeReporter("slack:C1")
	if r == nil {
		t.Fatal("設了 URL 應建 reporter")
	}
	r.Close()
}
