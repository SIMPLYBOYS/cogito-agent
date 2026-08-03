package chatbot

import "testing"

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
