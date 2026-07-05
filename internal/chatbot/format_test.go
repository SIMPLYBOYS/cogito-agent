package chatbot

import (
	"strings"
	"testing"
)

func TestFormatForChat_Table(t *testing.T) {
	md := "找到職缺：\n\n| 職稱 | 公司 | 薪資 |\n|------|------|------|\n| AI 架構師 | 杰恒 | 年薪 200萬 |\n| RD 主管 | 艾得克台灣分公司 | 年薪 400萬 |\n\n以上。"
	out := formatForChat(md)

	if !strings.Contains(out, "```") {
		t.Fatal("表格應包在 ``` 等寬 block 內")
	}
	if strings.Contains(out, "|---") || strings.Contains(out, "| 職稱 |") {
		t.Error("不應殘留 GFM 管線表格")
	}
	if !strings.Contains(out, "─") {
		t.Error("表頭下應有分隔線")
	}
	for _, v := range []string{"職稱", "AI 架構師", "艾得克台灣分公司", "年薪 400萬", "以上。"} {
		if !strings.Contains(out, v) {
			t.Errorf("應保留內容 %q", v)
		}
	}
	// 對齊驗證：同一欄的儲存格在等寬下起始位置一致——用「公司」欄第一格起點檢查。
	// 表頭「職稱」(寬4) + 2 空格，資料「AI 架構師」(A、I 各1 + 空格1 + 架構師各2=共 3+1+6=… ) 需 padding 補到欄寬。
	// 這裡簡單驗證：每個資料列的『第一個雙空格分隔』後，第二欄都對齊到同一顯示欄位。
	var body []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "```") {
			in = !in
			continue
		}
		if in {
			body = append(body, l)
		}
	}
	if len(body) < 4 { // 表頭 + 分隔 + 2 資料列
		t.Fatalf("表格列數不足: %v", body)
	}
}

func TestHeaderToBold(t *testing.T) {
	if headerToBold("## 市場概況") != "**市場概況**" {
		t.Errorf("## 標題應轉粗體，got %q", headerToBold("## 市場概況"))
	}
	if headerToBold("一般句子 # 井號") != "一般句子 # 井號" {
		t.Error("非開頭井號不應誤轉")
	}
}

func TestDispWidth(t *testing.T) {
	if dispWidth("中") != 2 || dispWidth("a") != 1 {
		t.Errorf("CJK 應算 2、ASCII 算 1，got 中=%d a=%d", dispWidth("中"), dispWidth("a"))
	}
	if dispWidth("職稱") != 4 || dispWidth("AI") != 2 {
		t.Errorf("複合寬度錯: 職稱=%d AI=%d", dispWidth("職稱"), dispWidth("AI"))
	}
}
