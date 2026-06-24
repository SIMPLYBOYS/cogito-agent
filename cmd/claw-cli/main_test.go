package main

import "testing"

func TestGoalVerify(t *testing.T) {
	if _, ok := goalVerify("exit 0", "."); !ok {
		t.Error("退出碼 0 應視為達成")
	}
	if out, ok := goalVerify("echo nope; exit 1", "."); ok || out == "" {
		t.Errorf("退出碼非 0 應視為未達成且回傳輸出，got ok=%v out=%q", ok, out)
	}
}
