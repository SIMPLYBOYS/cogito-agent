# 安全模型

cogito-agent 是**自託管**的 agent 框架：把聊天訊息變成在你機器上執行的 bash 與檔案操作。
這份文件講清楚它**防什麼**、更重要的是**不防什麼**。

> 這裡寫的每一條都對應到程式碼或實測，沒有「我們很重視安全」這種句子。
> 讀完若你覺得某條防線不夠，那多半是對的——**上線前請照最後一節的配置走**。

## 威脅模型：我們假設什麼

| 假設 | 說明 |
|---|---|
| **單一組織 / 自託管** | 不是多租戶硬邊界。同一行程內的 per-conversation 隔離是**運作上的分區**，不是安全邊界（見 [docs/multi-tenancy.md](docs/multi-tenancy.md)） |
| **operator 可信** | 能寫 `.claw/` 的人等同完全控制：可改政策、晉升技能、改排程、解除成本上限 |
| **LLM 輸出不可信** | 模型會產生錯誤與有害的命令。所有硬性防線都在**框架層強制**，不依賴模型自覺 |
| **agent 讀到的內容不可信** | 網頁、MCP 回傳、檔案內容都可能藏指令。**這是我們最大的缺口，見下方** |

---

## 防什麼

每條都可在程式碼中查證。

### 入口與身分

- **入口授權 fail-closed**：只有 `COGITO_ALLOWED_USERS` 名單內的 user id 能驅動 agent。
  **不設 = 拒絕所有人**，不是「開放所有人」。
- **審批身分分離**：高危審批限 `COGITO_ADMIN_USERS`，與發起者名單分開——杜絕「自己發起、自己放行」。
- **綁定守衛 fail-closed**：HTTP 入口與面板綁到非 loopback 位址時**直接拒絕啟動**
  （`cmdutil.IsLoopback`），除非顯式 `COGITO_HTTP_INSECURE=1`。

### 工具邊界

- **檔案工具擋逃逸**（[`internal/tools/path.go`](internal/tools/path.go)）：`..` 穿越、絕對路徑、
  **以及 symlink**——解析到最深的**已存在**祖先後重驗前綴，所以「symlink 指向尚不存在的新檔」
  也繞不過。
- **控制面唯讀**：檔案工具**不得寫入 `.claw/`**（技能／記憶／護欄／排程）。少了這層，
  「產物只進暫存區、需人工放行」整條鏈會被 agent 自己繞過。
- **機密路徑要審批**：`.env`、`id_rsa`、`credentials`、`.ssh`、`.aws`、`/etc/passwd`、`/etc/shadow`
  出現在 bash 或 MCP 參數即掛起等人（[`internal/chatbot/approval.go`](internal/chatbot/approval.go)）。
- **MCP 子行程只拿白名單環境變數**：`ANTHROPIC_API_KEY` 等一律讀不到，避免第三方 npx/uvx
  套件順手撈走憑證。

### 政策與熔斷

- **Deny > Ask > Allow**：宣告式政策檔（`.claw/policy.json`），裁決與規則順序無關。
- **無人值守時 Ask ＝ Deny**：排程／自動化情境下沒有人可問，「等人回答」不是安全。
- **政策拒絕 ＝ 目標終止**，不是可重試的工具錯誤——不給 agent 改寫命令繞過的空間。
  （這條是[事故](docs/incident-blacklist-bypass.md)之後補的。）
- **框架層硬性防線**（[`internal/engine/loop.go`](internal/engine/loop.go)）：單次 Run 回合上限 **40**、
  成本熔斷 **$1.0**、單輪工具併發上限 **5**、成本達 80% 軟著陸提醒。

### 產物治理

技能、記憶、知識圖譜的邊**一律只寫進提案暫存區**，過確定性把關（結構 + 危險指令／憑證掃描）
並經**人工放行**才生效。agent 不能自我晉升。

### 沙箱

`COGITO_SANDBOX=docker` 時 bash 進容器：只掛 workspace、**預設 `--network none` 完全斷網**、
限記憶體／CPU／PID。

---

## ⚠️ 不防什麼

這節比上一節重要。

### 1. Prompt injection —— **明確不防**

入口白名單擋得住**陌生人**，擋不住**陌生內容**。agent 讀到的網頁、MCP 回傳、檔案裡藏的指令
不是「誰發的」，白名單看不到它。我們沒有輸入端的注入分類器。

> 已做的緩解只有兩處：MCP 工具回傳標記為**資料而非指令**、
> 以及不把 workDir 內容當外滲通道。**這不等於防住了。**

### 2. 命令黑名單可以被繞過 —— **有實際事故記錄**

黑名單是軟性防線。改寫命令、編碼、先寫腳本再執行都能繞過。

這不是理論——[`docs/incident-blacklist-bypass.md`](docs/incident-blacklist-bypass.md) 是一份
**帶逐步證據的實際繞過記錄**：policy 擋下 `rm -rf` 之後，agent 自己改寫命令繞了過去。
修復方式是**把拒絕升級成目標終止**，而不是再補幾條黑名單——因為補黑名單是輸不完的競賽。

### 3. host 模式下 bash ＝ 宿主機 RCE 路徑

`COGITO_SANDBOX` 未設 docker 時，bash **直接在宿主機以本行程權限執行**。bot 是開放入口，
所以路徑是完整的：**陌生 prompt → bash → 宿主機**。

啟動時會印警告橫幅（[`internal/sandbox/env.go`](internal/sandbox/env.go)），但**只警告不阻擋**
——受控環境（內網、單人、已知 prompt）仍可能是合理選擇，決定權在你。

### 4. `workDir` 對 bash 只是慣例，不是邊界

檔案工具有前綴檢查，**bash 沒有**。`cd ..` 就出去了。真正的邊界是容器，不是這個變數。

### 5. host 模式沒有任何網路控制

docker 模式預設**完全斷網**（比多數同類方案嚴）。但 host 模式**全通**——包括雲端 metadata
endpoint。目前沒有中間態（可用網路但受限）。

### 6. 不是多租戶硬邊界

同一行程內的 per-conversation 隔離擋的是**誤觸**不是**惡意**。要真的隔離，用「一行程一租戶」
（硬租戶），見 [docs/multi-tenancy.md](docs/multi-tenancy.md)。

### 7. 面板遠端存取沒有認證

維運面板預設綁 loopback + CSRF。**遠端認證尚未實作**——非 loopback 綁定會被 fail-closed 擋下。
規劃見 [docs/tsnet-plan.md](docs/tsnet-plan.md)。不要用反向代理硬開出去而不加認證層。

### 8. 不防惡意或被入侵的 operator

能寫 `.claw/` 的人可以改政策、晉升技能、改排程。所有治理防線都建立在「operator 可信」之上。

### 9. 憑證在使用時是明文

`.env` 的金鑰以環境變數傳給行程。host 模式下同機任何能讀該行程環境的東西都看得到。
我們**沒有** vault、沒有 egress 端的密鑰替換。

### 10. 不保證模型輸出正確

評測結果是機率性的（見 [docs/eval-results.md](docs/eval-results.md)，含未達顯著的項目）。
不要把 agent 的自陳當成驗證——**驗證是呼叫端的責任**。

---

## 兩種模式的實際保證

| | host（預設） | docker（`COGITO_SANDBOX=docker`） |
|---|---|---|
| bash 執行位置 | **宿主機，本行程權限** | 容器 |
| 檔案系統可見範圍 | **全機** | 只有掛載的 workspace |
| 網路 | **全通**（含 metadata endpoint） | **預設完全斷網** |
| 資源限制 | 無 | 記憶體／CPU／PID 上限 |
| 適用 | 你自己在終端下 prompt（`claw-cli`） | **開放入口（bot）一律用這個** |

---

## 上線前的最低配置

```bash
COGITO_SANDBOX=docker                # 不是選配。開放入口必開
COGITO_ALLOWED_USERS=U123,U456       # 不設＝拒絕所有人，但別依賴這個當唯一防線
COGITO_ADMIN_USERS=U123              # 審批身分要與發起者分開
# 面板／HTTP 入口留在 loopback，遠端存取等 tsnet
```

再加上：**不要把有價值憑證放進 agent 能讀到的環境**。它擋得住直接 `cat .env`（要審批），
擋不住一段藏在網頁裡、被 agent 讀進來的指令。

---

## 回報安全問題

請走 GitHub 的私密漏洞回報（repo 的 **Security → Report a vulnerability**），不要開公開 issue。

如果你發現的是「某條防線可以被繞過」——很可能我們已經知道並寫在上面了。
若是上面**沒寫到**的繞過方式，那就是我們想知道的。
