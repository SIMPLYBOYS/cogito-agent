---
name: orchestrate
description: 當任務需要拆解成多個子任務、或要「實作＋審查＋糾錯」、或要多視角並行處理時，用此技能以 orchestrator 模式編排具名子 agent（規劃→分派→審查→糾錯→整合）。
---

# 編排模式（Orchestrator）

你是這次任務的 **orchestrator**。**不要自己埋頭做完所有事**——把可切分的子任務【委派】給具名子 agent
（`spawn_subagent` 的 `agent_type`），你負責規劃、分派、審查、整合。這是標準 ReAct：你的「行動」就是
`spawn_subagent`，子 agent 的報告就是你的「觀察」，據此決定下一步——直到目標達成。

## 可用的具名 agent（依任務挑；見 spawn_subagent 說明裡的清單）

- `planner`：把任務拆成有序步驟（規劃難時先派它取步驟清單）
- `implementer`：實作 / 改檔（**可寫**，worktree 隔離——並行派多個也不會相互覆蓋）
- `code-reviewer`：審查變更（只讀，找問題 + 給修法）
- `security-auditor`：資安審查（只讀）
- `correctness`：只審邏輯正確性——比較符號、邊界、off-by-one、金額計算（只讀）
- `performance`：只審效能——N+1、迴圈內 I/O、O(n²)、字串累加（只讀）

【多視角審查】審查一份程式碼時，把不同面向【並行】派給對應的窄專員（正確性→correctness、
安全→security-auditor、效能→performance），每個只回報自己那一面向的發現，你負責整合成
一個明確結論。窄專員比廣的 code-reviewer 更適合並行——各看各的、不重疊、報告更聚焦。

## 流程

1. **規劃**：想清楚拆成哪些子任務、哪些可並行、哪些有先後。難拆就先 `spawn_subagent(agent_type=planner)` 取步驟清單。
2. **分派**：
   - **並行**（彼此獨立）：在【同一輪】吐【多個】`spawn_subagent`（例如同時研究不同模組、或並行實作不同檔案——implementer 的 worktree 隔離讓並行寫入安全）。
   - **串行**（有依賴）：一個個派，把前一個的報告餵進下一個的 `task_prompt`。
3. **審查**：實作完，派 `code-reviewer`（或 `security-auditor`）審查產出，把意見收回來。
4. **糾錯**：若審查有問題，派 `implementer` 依審查意見修正（`task_prompt` 帶上審查評語）。必要時再審一輪，直到乾淨。
5. **整合交付**：收齊各子 agent 的精煉報告，給使用者最終交付（改了什麼、驗證結果、關鍵決策）。

## 紀律

- **保持自己的 context 精簡**：細活交給子 agent，它們只回傳精煉報告（不會用一堆搜尋/嘗試汙染你的 context）——這正是委派的價值。
- **你是編排者，不是執行者**：別自己做子 agent 該做的事。
- **並行優先**：彼此獨立的子任務同一輪一起派，別串行浪費時間。
- **審查是必要一步**：實作型任務完成後，預設派 reviewer 過一遍再交付。
