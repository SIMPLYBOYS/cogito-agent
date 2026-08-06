package chatbot

import (
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// 指令解析：逐條審核的入口。誤消費（把使用者的話當指令吃掉）比不認得更糟，故不認得的尾綴一律不消費。
func TestParseMemoryCommand(t *testing.T) {
	cases := []struct {
		in   string
		verb string
		nums []int
		ok   bool
	}{
		{"memory list", "list", nil, true},
		{"apply memory", "apply", nil, true},
		{"approve memory", "apply", nil, true},
		{"apply memory 2", "apply", []int{2}, true},
		{"apply memory 1 3", "apply", []int{1, 3}, true},
		{"reject memory", "reject", nil, true},
		{"discard memory 2", "reject", []int{2}, true},
		{"APPLY MEMORY 2", "apply", []int{2}, true}, // 大小寫不敏感
		// 不該消費的
		{"apply memory 全部", "", nil, false},
		{"apply memory 0", "", nil, false}, // 編號從 1 起
		{"memory list all", "", nil, false},
		{"幫我 apply memory", "", nil, false},
		{"apply config", "", nil, false},
		{"memory", "", nil, false},
	}
	for _, c := range cases {
		verb, nums, ok := parseMemoryCommand(c.in)
		if ok != c.ok || verb != c.verb || len(nums) != len(c.nums) {
			t.Errorf("%q → (%q,%v,%v)，期望 (%q,%v,%v)", c.in, verb, nums, ok, c.verb, c.nums, c.ok)
			continue
		}
		for i := range c.nums {
			if nums[i] != c.nums[i] {
				t.Errorf("%q → nums=%v，期望 %v", c.in, nums, c.nums)
				break
			}
		}
	}
}

func TestParseMemoryCommand_Reconcile(t *testing.T) {
	if v, _, ok := parseMemoryCommand("memory reconcile"); !ok || v != "reconcile" {
		t.Errorf("memory reconcile → (%q,%v)", v, ok)
	}
	// 帶尾綴不消費——避免誤吃使用者的話
	if _, _, ok := parseMemoryCommand("memory reconcile 一下記憶"); ok {
		t.Error("帶尾綴不該被消費")
	}
}

// 破壞性提案在清單裡要看得出【會動到什麼】——光一句「新值」是審不出來的。
func TestRenderProposedList_ShowsDestructiveDiff(t *testing.T) {
	out := renderProposedList([]evolve.ProposedMemoryEntry{
		{N: 1, Kind: "慣例", Op: evolve.OpAdd, Learning: "一般事實"},
		{N: 2, Kind: "整併", Op: evolve.OpUpdate, Target: "mem-aa", Old: "舊的說法", Learning: "新的說法", Why: "被推翻"},
		{N: 3, Kind: "整併", Op: evolve.OpDelete, Target: "mem-bb", Old: "過時內容", Why: "不再適用"},
	})
	for _, want := range []string{
		"1. [慣例] 一般事實",
		"舊：舊的說法", "新：新的說法", "因：被推翻", // UPDATE 要看得到 diff
		"mem-bb", "會歸檔（可復原）", "值：過時內容", // DELETE 要標明可復原
		"其中 2 條會【動到既有記憶】",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("清單缺少 %q:\n%s", want, out)
		}
	}
}

// 純 ADD 的清單不該冒出破壞性警告——那會讓警示失去意義。
func TestRenderProposedList_NoWarningWhenAllAdd(t *testing.T) {
	out := renderProposedList([]evolve.ProposedMemoryEntry{
		{N: 1, Kind: "慣例", Op: evolve.OpAdd, Learning: "甲"},
		{N: 2, Kind: "慣例", Learning: "乙"}, // Op 空＝舊格式，等同 add
	})
	if strings.Contains(out, "動到既有記憶") || strings.Contains(out, "⚠️") {
		t.Errorf("全是 ADD 不該有警告:\n%s", out)
	}
}
