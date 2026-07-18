package main

import (
	"os"
	"sort"
	"strings"
)

// updateEnvFile 就地更新 .env 的指定 key，保留所有其他行【逐字不動】——含註解、空行，以及【祕密行】
// （API 金鑰 / token）。安全關鍵：本函式只改 updates 裡列出的 key，絕不讀取或改動其他行，故 .env 裡的
// 祕密永遠不會被本路徑經手或外洩。找不到的 key 追加到檔尾。原子寫（temp + rename），權限 0600。
//
// 呼叫端只把【非祕密】的操作型 key 放進 updates（見 dashboard 的可編輯設定），祕密不在其列。
func updateEnvFile(path string, updates map[string]string) error {
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // 空行 / 註解：不動
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue // 非 KEY=VALUE 行：不動
		}
		key := strings.TrimSpace(line[:eq])
		if v, ok := updates[key]; ok { // 只有在更新清單裡的 key 才改；其他（含祕密）逐字保留
			lines[i] = key + "=" + v
			seen[key] = true
		}
	}

	// 去尾端空行後，把未出現的 key 追加（穩定排序，避免 map 迭代非確定性）。
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	var missing []string
	for k := range updates {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		lines = append(lines, k+"="+updates[k])
	}

	out := strings.Join(lines, "\n") + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readEnvValue 從 .env 讀單一 key 的目前值（供編輯表單帶入現值）。缺檔/缺 key 回空。不碰祕密以外的
// 邏輯——呼叫端只讀非祕密 key。
func readEnvValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq >= 0 && strings.TrimSpace(line[:eq]) == key {
			return strings.TrimSpace(line[eq+1:])
		}
	}
	return ""
}
