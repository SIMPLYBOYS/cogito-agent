package observability

import (
	"context"
	"sync"
	"testing"
)

func TestSpan_ParentChildNesting(t *testing.T) {
	ctx := context.Background()
	ctx, root := StartSpan(ctx, "root")
	_, child := StartSpan(ctx, "child") // 在 root 的 ctx 下開啟 → 應掛為 root 的子節點

	child.EndSpan()
	root.EndSpan()

	if len(root.Children) != 1 || root.Children[0] != child {
		t.Fatalf("child 應掛在 root 下，實際 children=%v", root.Children)
	}
	if root.DurationMs < 0 {
		t.Errorf("duration 不應為負: %d", root.DurationMs)
	}
}

// 驗證併發在同一父節點下開 span 時，Children 追加是併發安全的（靠 Span.mu）。
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
		t.Errorf("併發 %d 個子 span 應全部掛上，實際 %d", n, len(root.Children))
	}
}

func TestSpan_NoParentNoCrash(t *testing.T) {
	// ctx 中無父 span 時不應 panic，且不掛任何 children
	_, s := StartSpan(context.Background(), "orphan")
	s.AddAttribute("k", "v")
	s.EndSpan()

	if s.Attributes["k"] != "v" {
		t.Error("AddAttribute 未生效")
	}
	if len(s.Children) != 0 {
		t.Error("孤兒 span 不應有 children")
	}
}
