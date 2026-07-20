//go:build !unix

package cron

// 非 Unix 平台沒有 flock。本專案的部署形態是 macOS 開發／Linux 常駐，故此處退化為「不加鎖」，
// 只保證仍可編譯。若哪天真要在 Windows 上同時跑 bot 與 dashboard 的排程器，得補上
// msvcrt.locking（或改用 LockFileEx），否則兩邊會各跑一遍。
func acquireLock(string) (func(), bool) { return func() {}, true }
