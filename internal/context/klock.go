package context

import "sync"

// knowledgeMu 序列化對【共用知識層】（.claw 的 memory / kg / skills 提案、放行、ingest）的寫入。
// 多 session 併發（不同頻道、甚至日後多平台）同時做 consolidation / apply / ingest 時，這些檔案的
// append 與 read-modify-write 否則會交錯競態。
//
// 範圍刻意只蓋【寫入】：
//   - recall / 子圖檢索等【讀路徑】不鎖——那是熱路徑，必須保持跨 session 並行。
//   - synth/extract 的 LLM 呼叫不鎖——只鎖其【檔案寫尾段】，避免持鎖等待網路而串行化各 session。
//
// 非可重入（sync.Mutex）：持鎖的公開寫 API 內【不可】再呼叫另一個會鎖的公開寫 API。
// 目前唯一的巢狀是 ApplyProposedMemory → Prune；Prune 刻意不鎖（只在 Apply 的鎖內被呼叫）。
var knowledgeMu sync.Mutex

// LockKnowledge / UnlockKnowledge 供同模組外（如 evolve 的 synth/apply）取得同一把知識層寫入鎖。
func LockKnowledge()   { knowledgeMu.Lock() }
func UnlockKnowledge() { knowledgeMu.Unlock() }
