package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/engine"
)

// 跑分要跑在「真的會生效的那組參數」上：以 .claw/config.json 為主、缺項補引擎預設。
// 先前這裡寫死 40/5/1.0 且完全不讀 config.json——跑分用預設值跑，調參建議卻拿去跟現行值比。
func TestEffectiveKnobs(t *testing.T) {
	t.Run("無 config.json 時補引擎預設", func(t *testing.T) {
		k := effectiveKnobs(t.TempDir())
		if k.MaxTurns != engine.DefaultMaxTurns {
			t.Errorf("MaxTurns 應為引擎預設 %d，實得 %d", engine.DefaultMaxTurns, k.MaxTurns)
		}
		if k.MaxConcurrentTools != engine.DefaultMaxConcurrentTools {
			t.Errorf("MaxConcurrentTools 應為引擎預設 %d，實得 %d", engine.DefaultMaxConcurrentTools, k.MaxConcurrentTools)
		}
		if k.MaxCostUSD != engine.DefaultMaxCostUSD {
			t.Errorf("MaxCostUSD 應為引擎預設 %v，實得 %v", engine.DefaultMaxCostUSD, k.MaxCostUSD)
		}
	})

	t.Run("有 config.json 時以它為準", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"max_turns":25,"max_concurrent_tools":2,"max_cost_usd":3.5}`
		if err := os.WriteFile(filepath.Join(root, ".claw", "config.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		k := effectiveKnobs(root)
		if k.MaxTurns != 25 || k.MaxConcurrentTools != 2 || k.MaxCostUSD != 3.5 {
			t.Errorf("應讀到 config.json 的值，實得 %+v", k)
		}
	})

	t.Run("部分缺項只補該項", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".claw"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claw", "config.json"), []byte(`{"max_turns":12}`), 0o600); err != nil {
			t.Fatal(err)
		}
		k := effectiveKnobs(root)
		if k.MaxTurns != 12 {
			t.Errorf("已設的項要保留，實得 %d", k.MaxTurns)
		}
		if k.MaxConcurrentTools != engine.DefaultMaxConcurrentTools {
			t.Errorf("未設的項要補預設，實得 %d", k.MaxConcurrentTools)
		}
	})
}
