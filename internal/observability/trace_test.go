package observability

import (
	"context"
	"sync"
	"testing"
)

func TestSpan_ParentChildNesting(t *testing.T) {
	ctx := context.Background()
	ctx, root := StartSpan(ctx, "root")
	_, child := StartSpan(ctx, "child") // 在 root 的 ctx 下开启 → 应挂为 root 的子节点

	child.EndSpan()
	root.EndSpan()

	if len(root.Children) != 1 || root.Children[0] != child {
		t.Fatalf("child 应挂在 root 下，实际 children=%v", root.Children)
	}
	if root.DurationMs < 0 {
		t.Errorf("duration 不应为负: %d", root.DurationMs)
	}
}

// 验证并发在同一父节点下开 span 时，Children 追加是并发安全的（靠 Span.mu）。
func TestSpan_ConcurrentChildrenSafe(t *testing.T) {
	ctx := context.Background()
	ctx, root := StartSpan(ctx, "root")

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, s := StartSpan(ctx, "child")
			s.EndSpan()
		}()
	}
	wg.Wait()

	if len(root.Children) != n {
		t.Errorf("并发 %d 个子 span 应全部挂上，实际 %d", n, len(root.Children))
	}
}

func TestSpan_NoParentNoCrash(t *testing.T) {
	// ctx 中无父 span 时不应 panic，且不挂任何 children
	_, s := StartSpan(context.Background(), "orphan")
	s.AddAttribute("k", "v")
	s.EndSpan()

	if s.Attributes["k"] != "v" {
		t.Error("AddAttribute 未生效")
	}
	if len(s.Children) != 0 {
		t.Error("孤儿 span 不应有 children")
	}
}
