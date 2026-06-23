# cogito-agent 沙箱映像：當 COGITO_SANDBOX=docker 時，bash 工具的每條命令都在此容器內執行。
# 容器只掛入該 session 的 workDir（-v workDir:/workspace），宿主機其餘檔案系統不可見；
# 預設 --network none 斷網、限記憶體/CPU/PID。這是「失控控制」軟性防線之外的 OS 級硬邊界。
#
# 建置：
#   docker build -t cogito-sandbox:latest -f docker/sandbox.Dockerfile .
# 啟用：
#   export COGITO_SANDBOX=docker
#   go run ./cmd/claw-cli -prompt "..."        # 或 cmd/claw（Slack）
#
# 以 golang 為基底，讓代理在容器內就能 go build/test；附常用 CLI（ripgrep/jq/git）。
FROM golang:1.25-bookworm

RUN apt-get update && apt-get install -y --no-install-recommends \
    ripgrep jq git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

# 說明：此處以容器內 root 執行——對宿主機而言仍由 namespace 完全隔離，且能避免掛載 volume 的
# uid 不匹配寫入失敗（跨 mac/Linux 最省事）。若要再縮提權面，可改加非 root USER 並處理 volume 權限。
