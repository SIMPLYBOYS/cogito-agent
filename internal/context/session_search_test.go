package context

import (
	"strings"
	"testing"
	"time"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// seed 造一個落地的 session。ago＝UpdatedAt 距今幾天。
func seed(t *testing.T, store SessionStore, id string, ago int, msgs ...string) {
	t.Helper()
	snap := &SessionSnapshot{
		ID:           id,
		UpdatedAt:    time.Now().AddDate(0, 0, -ago).Format(time.RFC3339),
		TotalCostUSD: 0.5,
	}
	for _, m := range msgs {
		snap.History = append(snap.History, schema.Message{Role: schema.RoleUser, Content: m})
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func newStore(t *testing.T) SessionStore {
	t.Helper()
	s, err := NewFileSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSearchSessions(t *testing.T) {
	store := newStore(t)
	seed(t, store, "s-old", 30, "我們在 redis 上遇到連線池耗盡")
	seed(t, store, "s-new", 1, "redis 又爆了", "這次是 redis 的 maxmemory 設定")
	seed(t, store, "s-none", 1, "今天天氣不錯")
	seed(t, store, "s-self", 1, "redis redis redis")

	t.Run("命中並依分數排序", func(t *testing.T) {
		hits, err := SearchSessions(store, SessionSearchOpts{Query: "redis", Exclude: "s-self"})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 2 {
			t.Fatalf("want 2 hits, got %d: %+v", len(hits), hits)
		}
		if hits[0].ID != "s-new" { // 命中 2 次 > s-old 的 1 次
			t.Errorf("want s-new first, got %s", hits[0].ID)
		}
		if hits[0].Turns != 2 || hits[0].CostUSD != 0.5 {
			t.Errorf("metadata 沒帶出來: %+v", hits[0])
		}
		if len(hits[0].Snippets) == 0 || !strings.Contains(hits[0].Snippets[0], "redis") {
			t.Errorf("片段沒含命中詞: %+v", hits[0].Snippets)
		}
	})

	t.Run("Exclude 跳過自己", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "redis", Exclude: "s-self"})
		for _, h := range hits {
			if h.ID == "s-self" {
				t.Fatal("Exclude 沒生效")
			}
		}
	})

	t.Run("Days 濾掉舊的", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "redis", Days: 7, Exclude: "s-self"})
		if len(hits) != 1 || hits[0].ID != "s-new" {
			t.Fatalf("want only s-new, got %+v", hits)
		}
	})

	t.Run("CJK bigram：查得到子字串", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "連線池"})
		if len(hits) != 1 || hits[0].ID != "s-old" {
			t.Fatalf("want s-old, got %+v", hits)
		}
	})

	t.Run("查無結果回空", func(t *testing.T) {
		if hits, _ := SearchSessions(store, SessionSearchOpts{Query: "kubernetes"}); len(hits) != 0 {
			t.Fatalf("want none, got %+v", hits)
		}
	})

	t.Run("空 query 回空", func(t *testing.T) {
		if hits, _ := SearchSessions(store, SessionSearchOpts{Query: "   "}); len(hits) != 0 {
			t.Fatalf("want none, got %+v", hits)
		}
	})

	t.Run("nil store 回空而非 panic", func(t *testing.T) {
		hits, err := SearchSessions(nil, SessionSearchOpts{Query: "redis"})
		if err != nil || hits != nil {
			t.Fatalf("want (nil,nil), got (%+v,%v)", hits, err)
		}
	})
}

func TestSearchSessionsBounded(t *testing.T) {
	store := newStore(t)
	long := strings.Repeat("背景說明。", 200) + "redis 爆了" + strings.Repeat("後續細節。", 200)
	for i := range 30 {
		seed(t, store, "s"+string(rune('a'+i)), 0, long, long, long, long, long)
	}

	t.Run("Limit 預設 5", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "redis"})
		if len(hits) != defaultSessionLimit {
			t.Fatalf("want %d, got %d", defaultSessionLimit, len(hits))
		}
	})

	t.Run("Limit 夾在硬上限", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "redis", Limit: 999})
		if len(hits) != maxSessionLimit {
			t.Fatalf("want %d, got %d", maxSessionLimit, len(hits))
		}
	})

	// 這條是這支工具存在的理由：不能把整份 session 灌進 context。
	t.Run("片段數與長度有界", func(t *testing.T) {
		hits, _ := SearchSessions(store, SessionSearchOpts{Query: "redis", Limit: 1})
		h := hits[0]
		if len(h.Snippets) > maxSessionSnippets {
			t.Fatalf("片段數 %d > 上限 %d", len(h.Snippets), maxSessionSnippets)
		}
		for _, s := range h.Snippets {
			body := strings.TrimPrefix(s, "[user] ")
			if n := len([]rune(body)); n > sessionSnippetRunes+4 { // +4：角色標籤/省略號的餘裕
				t.Fatalf("片段 %d runes 超過上限 %d", n, sessionSnippetRunes)
			}
			if !strings.Contains(s, "redis") {
				t.Fatalf("片段沒切在命中處: %q", s)
			}
		}
	})
}

// 一個壞檔不該擋掉整次檢索——sessions 目錄裡放什麼的都有。
func TestSearchSessionsSkipsBadFile(t *testing.T) {
	store := newStore(t)
	seed(t, store, "good", 0, "redis 爆了")
	hits, err := SearchSessions(badListStore{store}, SessionSearchOpts{Query: "redis"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "good" {
		t.Fatalf("want only good, got %+v", hits)
	}
}

// badListStore 在 List 裡多報一個不存在的 id，模擬「列到了但讀不出來」的壞檔。
type badListStore struct{ SessionStore }

func (b badListStore) List() ([]string, error) {
	ids, err := b.SessionStore.List()
	return append(ids, "ghost"), err
}
