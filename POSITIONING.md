# 定位（Positioning）

cogito-agent 是一個 **Go 寫的輕量 agent harness**，由課程實作演進而來，公開、可自託管。本文誠實說明它**是什麼、不是什麼**，以及它的差異化與發展優先序。

## 一句話定位

> 輕量、單一 binary、可審計的 **Anthropic 優先自託管 agent**——以**安全控制**與**可觀測/評測**見長。

## 是什麼 / 不是什麼

| | 說明 |
|---|---|
| ✅ 是 | ReAct agent harness：工具註冊/中間件、MCP、技能漸進式載入、子 agent、Slack 接入 |
| ✅ 是 | **安全控制連貫**：成本熔斷、死迴圈指紋偵測、HITL 審批、回合上限、Docker 沙箱（斷網/限資源） |
| ✅ 是 | **可觀測/評測完整**：OpenTelemetry → Jaeger/Langfuse、USD 成本追蹤、benchmark + 儀表板 + CI |
| ✅ 是 | 乾淨的參考實作，適合學習與自託管 |
| ❌ 不是 | 有資金的通用 agent 平台、全模型/全平台框架、含模型訓練（RL 微調）飛輪的產品 |

## 與有資金框架的關係（例：Nous Research 的 Hermes Agent）

像 Hermes Agent 這類**有資金實驗室**的自我進化框架，在以下維度是**不同量級**：

- **模型/平台廣度**：200+ 模型、多即時通訊平台 vs cogito-agent 目前 Claude + Slack。
- **自我進化深度**：走到 **RL 微調（改模型權重）** 的訓練飛輪 vs cogito-agent 的 prompt/技能/記憶層進化（已實作、且一律 gated）。
- **生態與資源**：社群技能 hub、自家模型血統、資金。

因此 cogito-agent **不以「對打通用平台」為目標**，而是走**利基**：在「不想要重量級依賴、要乾淨安全邊界與成本/trace 可視、Anthropic 優先、單 binary 自託管」的場景做到好。在**可觀測、評測、控制**這幾塊，它已接近或勝過一般框架。

## 發展優先序

已完成：多 Provider（Claude + 任意 OpenAI 相容端點）、自我進化四件套（技能/記憶/Reflexion/參數，皆 gated）、SWE-bench 相容評測管線（端到端真跑已驗證）。競爭門檻是**廣度**，故剩餘優先序：

1. **多平台接入**——從 Slack 走向多個即時通訊平台（最大的廣度缺口）。
2. **官方 SWE-bench Lite 數字**——把評測管線接上官方 Docker 環境，跑出可引用的 pass@X%。
3. **記憶/評測深化**——embedding 檢索、bench 多 Provider 對比等（介面已留升級點）。

> 簡言之：harness 深度與安全/可觀測已到位，再補**廣度**讓它更可用。把 cogito-agent 當「**最佳實踐參考實作 + 安全/可觀測見長的利基自託管 agent**」看待，比當成大型平台的競品更務實。設計取捨詳見 [DESIGN.md](DESIGN.md)。
