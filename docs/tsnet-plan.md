# C-Auth：tsnet 實作 Action Plan（roadmap #4）

> **狀態：未實作、已規劃。** 這份是「真要動時照著走」的 do-list；設計理由/信任鏈對照在 vault
> `cogito-agent-Operator-Dashboard-C-Spec` §八。與 [multi-tenancy.md](multi-tenancy.md) 的關係：
> tsnet 是「誰能操作**維運面板**」的密碼學身分，**不是** workload 租戶隔離（那是兩層租戶模型）。

## 一句話目標

面板從 `http.ListenAndServe("127.0.0.1:8091", mux)` 換成掛在 **tsnet 虛擬節點**上，用
`LocalClient.WhoIs(r.RemoteAddr)` 拿到經 SSO 驗證的 `LoginName`（WireGuard 密碼學保證、無 HTTP
header 參與、偽造需偽造金鑰），比對 `COGITO_ADMIN_USERS`。**其餘 handler 不動。**

## 動手前的三個前提（缺一不可）

1. **一個 Tailscale 帳號 + auth key**（`TS_AUTHKEY`）——首次註冊節點用。
2. **tailnet 內至少兩台裝置**——一台跑面板、一台當「授權/非授權用戶」驗存取。這是本機驗不了、
   但**不需要公開 IP/反代/staging** 的關鍵（§八 推翻了「只有雲上能驗」）。
3. **依賴重量的決策已定**（見下「Phase 1」）——`tailscale.com/tsnet` 是一整套網路棧，會把目前
   極精簡的 go.sum（91 行）灌到數百行。這是**難乾淨還原**的公開 repo 決定，先決定隔離方式再動。

---

## Phase 0 —— Spike / 去風險（丟棄式分支，不進 main）

目的：拿真數字再決定，不憑感覺。

- [ ] 開丟棄式分支，`go get tailscale.com/tsnet`，把 §八 的最小骨架跑一次 `go build`。
- [ ] **量兩個數字**：(a) `cmd/claw-dashboard` binary 大小增幅；(b) go.sum 行數增幅、`go mod download` 時間。
- [ ] 確認在**本機**能起 tsnet 節點（需 `TS_AUTHKEY` + `Dir` 事先存在），`WhoIs` 對自己回得出 `LoginName`。
- [ ] 丟棄分支。**Phase 0 的產物是數字與信心，不是程式碼。**

**退出條件**：知道確切膨脹量、確認骨架可跑。若膨脹不可接受 → 停在這，維持 loopback + SSH tunnel。

## Phase 1 —— 依賴隔離結構（先決定，再寫）

問題：`tailscale.com` 進 go.mod 就影響**所有** binary 的 `go build`/CI/go.sum，即使只有面板用。

| 選項 | 主 module go.sum | 代價 | 建議 |
|---|---|---|---|
| **A. 獨立 nested module** `cmd/claw-dashboard-tsnet/`（自帶 go.mod，`replace` 指回主 module） | **維持 91 行乾淨** | 需先把面板 server 從 `package main` 抽成**可跨 module import 的公開 package**（`dashboard/`，非 `internal/`——internal 跨 module 不可 import）；一點重構 | **傾向**（保住精簡 go.mod 這個賣點） |
| B. build tag `//go:build tsnet` 在主 module | ❌ 仍灌胖（go mod tidy 收 build-tagged 依賴） | 只省「預設 binary 編譯/大小」，不省 go.sum | 次選 |
| C. 直接進主 module + env-gate | ❌ 灌胖 | 最省事、程式最少 | 只在你不在乎 go.sum 重量時 |

- [ ] 選定 A/B/C（**A 需先做「抽出可 import 的 dashboard package」子任務**）。
- [ ] 若 A：把 `newServer` + mux 組裝抽到 `dashboard/`（public package），`cmd/claw-dashboard` 與
      `cmd/claw-dashboard-tsnet` 都薄薄地 import 它。**這步不碰 tsnet，可獨立先做、獨立驗**（既有測試照過）。

## Phase 2 —— tsnet listener + WhoIs middleware

- [ ] `srv := new(tsnet.Server)`；設 `Hostname`（如 `cogito-dash`）、`Dir`（持久狀態，**必須事先存在**）、
      `AuthKey`（讀 `TS_AUTHKEY`）、`AdvertiseTags`（打 ACL tag）、`ListenTLS`（tailnet 內 HTTPS，可選）。
- [ ] `ln, _ := srv.Listen("tcp", *addr)`；`lc, _ := srv.LocalClient()`；`http.Serve(ln, mw(mux))`。
- [ ] middleware `mw`：`who, err := lc.WhoIs(r.Context(), r.RemoteAddr)` → `who.UserProfile.LoginName`
      比對 `COGITO_ADMIN_USERS`；不在名單 → 403。**唯讀 GET 是否也要 admin，依 C2 政策定**（見 §三）。
- [ ] **與 #5 整合**：`operatorIDFrom(r)` 在 tsnet 模式下改回傳 `WhoIs` 的 `LoginName`（而非
      `X-Forwarded-User`）——稽核身分從「反代宣稱」升級成「WireGuard 密碼學」。這是 #4/#5 的匯流點。

## Phase 3 —— 設定與預設（不破壞現況）

- [ ] **env-gate**：`COGITO_DASH_TSNET=1` 才走 tsnet；**預設仍是現在的 loopback + `checkBindSafety`
      fail-closed 守衛**——零行為改變，與 memory-scope / dash-chat 同一 opt-in 慣例。
- [ ] `Dir` 不存在時明確報錯（tsnet 會失敗）；`TS_AUTHKEY` 缺失時提示。
- [ ] 文件：`.env.example` 加 `COGITO_DASH_TSNET` / `TS_AUTHKEY`；README 面板段補「遠端存取」說明。

## Phase 4 —— 在真 tailnet 驗證（§八 的待驗 5 項）

本機驗不了的就在這裡驗，**兩台裝置即可**：

- [ ] 授權裝置連得上、非授權裝置連不上（ACL tag 阻擋生效）。
- [ ] `WhoIs` 對遠端連線回得出正確 `LoginName`，middleware 比對 `COGITO_ADMIN_USERS` 正確放行/擋。
- [ ] binary 大小增幅實測（對齊 Phase 0 的估計）。
- [ ] `WhoIs` 寫進 audit 的欄位取捨——**只留 `LoginName`**，別無腦 dump 全欄位（§八：某些司法管轄區屬個資）。
- [ ] Tailscale 免費方案的裝置/用戶上限查證（單人綽綽有餘，記一下確切數字）。

## Phase 5 —— 收尾

- [ ] roadmap #4 標結案（附實測 binary 增幅 + 兩裝置驗證結果）。
- [ ] multi-tenancy.md「硬租戶維運面板身分」段從「候選」改「已實作」。

---

## 明確不做 / 保持不變

- **不動任何業務 handler**——只換 listener + 加一層 middleware + 身分來源。
- **不改預設**——沒設 `COGITO_DASH_TSNET` 就是現在的 loopback，`checkBindSafety` 守衛照舊。
- **tsnet ≠ 租戶隔離**——它是 operator 面板身分；workload 隔離是 [multi-tenancy.md](multi-tenancy.md) 的兩層模型。
- **不做反代 + token/OIDC 那條**（§八的路徑①）——只有雲上能驗、footgun 多；tsnet 路徑本機兩裝置即可驗、信任鏈更硬。

## 觸發條件（何時把這份從「規劃」變「動手」）

任一成立：①面板要給**本機以外**的人操作（不想只用 SSH tunnel）；②真的要多實例 + 遠端聚合
（§九.2：每個 profile 的面板是 tailnet 獨立節點/ACL）；③把面板放到雲上。在那之前，loopback + SSH
tunnel 已足夠，不值得為它扛 Tailscale 依賴。
