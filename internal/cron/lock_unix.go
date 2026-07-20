//go:build unix

package cron

import (
	"os"
	"syscall"
)

// acquireLock 取跨行程獨佔鎖（非阻塞）。取到回 (釋放函式, true)；已被別的行程持有回 (nil, false)。
//
// 【為何用 flock 而非 PID 檔】flock 由核心管理，行程崩潰／被 kill 時 OS 會自動釋放；PID 檔則會
// 留下陳舊鎖，得再加一套「這個 PID 還活著嗎」的判斷，反而更容易錯。
func acquireLock(path string) (func(), bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false // 開不了鎖檔就別跑，寧可這輪不執行也不要雙跑
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}
