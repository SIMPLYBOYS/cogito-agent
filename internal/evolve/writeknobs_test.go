package evolve

import "testing"

// 安全底線：WriteActiveKnobs 把越界值夾回邊界；0 保留為「不覆蓋」；寫出的檔 LoadKnobs 讀得回。
func TestWriteActiveKnobs_Clamps(t *testing.T) {
	root := t.TempDir()
	// 送一個危險的超大成本 + 越界回合 + 0 併發（0＝不覆蓋）
	got, err := WriteActiveKnobs(root, Knobs{MaxTurns: 9999, MaxConcurrentTools: 0, MaxCostUSD: 99999})
	if err != nil {
		t.Fatal(err)
	}
	lim := Limits()
	if got.MaxCostUSD != lim.MaxCostUSD {
		t.Errorf("越界成本應夾到 %.1f，got %.2f", lim.MaxCostUSD, got.MaxCostUSD)
	}
	if got.MaxTurns != lim.MaxTurns {
		t.Errorf("越界回合應夾到 %d，got %d", lim.MaxTurns, got.MaxTurns)
	}
	if got.MaxConcurrentTools != 0 {
		t.Errorf("0 應保留為不覆蓋，got %d", got.MaxConcurrentTools)
	}
	// 落地檔可被 LoadKnobs 讀回
	k, ok := LoadKnobs(root)
	if !ok || k.MaxCostUSD != lim.MaxCostUSD {
		t.Errorf("寫出的 config.json 應可 LoadKnobs 讀回，got ok=%v k=%+v", ok, k)
	}
}
