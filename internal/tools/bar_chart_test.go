package tools

import (
	"context"
	"strings"
	"testing"
)

func TestBarChart(t *testing.T) {
	args := `{"title":"職缺數","max_width":20,"data":[
		{"label":"台北","value":4731},
		{"label":"台中","value":2366},
		{"label":"高雄","value":0}]}`
	out, err := (&BarChartTool{}).Execute(context.Background(), []byte(args))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "```") || !strings.Contains(out, "職缺數") {
		t.Fatalf("應含標題與 ``` block，got:\n%s", out)
	}

	line := func(prefix string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, prefix) {
				return l
			}
		}
		return ""
	}
	// 最大值 → 滿格 20 個 █
	if n := strings.Count(line("台北"), "█"); n != 20 {
		t.Errorf("最大值應滿格 20 █，got %d：%q", n, line("台北"))
	}
	// 一半的值 → 約 10 個 █（比例正確）
	if n := strings.Count(line("台中"), "█"); n != 10 {
		t.Errorf("半值應約 10 █，got %d：%q", n, line("台中"))
	}
	// 值 0 → 空長條，無 █
	if n := strings.Count(line("高雄"), "█"); n != 0 {
		t.Errorf("值 0 不該有 █，got %d：%q", n, line("高雄"))
	}
	// 數值有標出
	if !strings.Contains(out, "4731") {
		t.Error("應標出數值")
	}
}

func TestBarChart_EmptyData(t *testing.T) {
	if _, err := (&BarChartTool{}).Execute(context.Background(), []byte(`{"data":[]}`)); err == nil {
		t.Error("空 data 應回 error")
	}
}
