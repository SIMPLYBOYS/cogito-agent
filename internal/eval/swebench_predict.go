package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	ctxpkg "github.com/SIMPLYBOYS/cogito-agent/internal/context"
	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
	"github.com/SIMPLYBOYS/cogito-agent/internal/observability"
	"github.com/SIMPLYBOYS/cogito-agent/internal/provider"
	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// Prediction 是 SWE-bench 官方評測吃的格式（每行一筆 JSONL）：官方 harness 會在該 instance 的
// Docker 容器內把 model_patch 套上、再套 test_patch 跑測試，判 resolved。
type Prediction struct {
	InstanceID string `json:"instance_id"`
	Model      string `json:"model_name_or_path"`
	Patch      string `json:"model_patch"`
}

const predictionModelName = "cogito-agent"

// GeneratePrediction 對一個 SWE-bench 實例【生成】一筆 prediction：clone@base_commit → 跑 agent 改原始碼
// → 抓 git diff 當 model_patch。【不】套 test_patch、【不】自己驗證（那是官方 harness 在容器內做的，
// 也確保 agent 看不到驗證測試＝防作弊）。生成階段刻意輕量：只 clone+checkout，不裝環境。
func GeneratePrediction(ctx context.Context, ins SWEInstance, opts SWEOptions, model string) (Prediction, error) {
	o := opts.withDefaults()
	cwd, _ := os.Getwd()
	workDir := fmt.Sprintf("%s/workspace/swegen_%s_%d", cwd, sanitizeID(ins.InstanceID), time.Now().UnixNano())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return Prediction{}, err
	}

	// 只 clone + checkout 到 base_commit（生成不需環境；環境是官方評測容器的事）。
	setup := fmt.Sprintf("set -e\ngit clone --quiet %s%s.git .\ngit checkout --quiet %s\n", o.RepoURLPrefix, ins.Repo, ins.BaseCommit)
	if err := runBashIn(workDir, setup); err != nil {
		return Prediction{}, fmt.Errorf("clone/checkout 失敗: %w", err)
	}

	p := provider.NewClaudeProvider(model)
	session := ctxpkg.NewSession("swegen-"+sanitizeID(ins.InstanceID), workDir)
	tracked := observability.NewCostTracker(p, model, session)

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	eng := engine.NewAgentEngine(tracked, registry, false, false)
	rep := &benchReporter{}
	session.Append(schema.Message{Role: schema.RoleUser, Content: ins.ToTestCase(o).TaskPrompt})
	_ = eng.Run(ctx, session, rep) // 即便 agent 中途出錯，仍抓它已做的 diff（部分修補也交給官方評）

	patch, err := captureDiff(workDir)
	if err != nil {
		return Prediction{}, err
	}
	return Prediction{InstanceID: ins.InstanceID, Model: predictionModelName, Patch: patch}, nil
}

// captureDiff 把工作區相對 base_commit 的所有改動（含新檔）抓成可 git apply 的 patch。
func captureDiff(workDir string) (string, error) {
	if err := runBashIn(workDir, "git add -A"); err != nil {
		return "", err
	}
	cmd := exec.Command("bash", "-c", "git diff --cached")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff 失敗: %w", err)
	}
	return string(out), nil
}

func runBashIn(dir, script string) error {
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	return cmd.Run()
}

// WritePredictions 把 predictions 寫成 JSONL（官方 harness 的 --predictions_path 吃這個）。
func WritePredictions(path string, preds []Prediction) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range preds {
		b, _ := json.Marshal(p)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}
