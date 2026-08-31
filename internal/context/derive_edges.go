package context

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// taskRe 從記錄正文抽「來自哪個任務」。兩種 provenance 格式都吃：
//
//	新：〔來源 provenance〕由「慣例」反思、於 <ts> 從任務「X」沉澱。
//	舊：（來源：任務「X」）
//
// 共同點是 任務「X」，所以一條式子就夠——不必為了格式演進寫兩份解析。
var taskRe = regexp.MustCompile(`任務「(.+?)」`)

// deriveClusterCap 是「一個任務裡幾筆記錄以內才兩兩相連」。
//
// 超過就退化成【鏈狀】：n 筆只連 n-1 條而不是 n(n-1)/2 條。理由有二——把關對每個節點
// 有 8 條出邊的上限（超過會被拒，等於白推），而且大群組兩兩相連對多跳檢索沒有額外好處，
// 連通性一樣達成。上限取 8 是為了對齊 kg_gate 的 maxEdgesPerNode。
const deriveClusterCap = 8

// DeriveEdges 從既有記錄【推導】邊：同一個任務沉澱出來的記錄互相關聯。
//
// 【為何要有這條路】圖原本只有兩種邊：人工寫的 [[links]]（實庫 0 條）與 LLM 抽取
// （要花錢、要人審，實庫 9 條提案卡了兩個月）。推導邊是第三條——確定性、零成本、
// 可重跑，而且【端點用檔名 slug】所以永遠對得上，不受標題品質影響。
//
// 邊的方向按 slug 字典序固定（小→大），這樣重跑產出完全一致：既有的去重才有意義。
// 檢索端的 BFS 本來就是無向擴張，方向只影響顯示。
func DeriveEdges(root string) []StoredEdge {
	recs := NewMemoryLoaderAt(filepath.Join(root, ".claw", "memory")).List()

	byTask := map[string][]string{}
	for _, r := range recs {
		m := taskRe.FindStringSubmatch(r.Body)
		if m == nil {
			continue // 沒有 provenance 的記錄不參與：不知道來源就不該猜它跟誰相關
		}
		slug := strings.TrimSuffix(filepath.Base(r.Path), ".md")
		if slug != "" {
			byTask[m[1]] = append(byTask[m[1]], slug)
		}
	}

	var out []StoredEdge
	tasks := make([]string, 0, len(byTask))
	for t := range byTask {
		tasks = append(tasks, t)
	}
	sort.Strings(tasks) // 決定性輸出

	for _, t := range tasks {
		g := byTask[t]
		sort.Strings(g)
		if len(g) < 2 {
			continue
		}
		if len(g) > deriveClusterCap {
			for i := 0; i+1 < len(g); i++ { // 鏈狀
				out = append(out, sameOrigin(g[i], g[i+1]))
			}
			continue
		}
		for i := 0; i < len(g); i++ { // 兩兩相連
			for j := i + 1; j < len(g); j++ {
				out = append(out, sameOrigin(g[i], g[j]))
			}
		}
	}
	return out
}

// sameOrigin 造一條同源邊。Confidence 給 1.0——這是從落盤的 provenance 直接讀出來的
// 確定性事實，不是模型的判斷；跟 LLM 抽取的邊在同一個檔案裡，靠 Source 分辨得開。
func sameOrigin(a, b string) StoredEdge {
	return StoredEdge{From: a, To: b, Type: "same-origin", Confidence: 1.0, Source: "derived:provenance"}
}

// AppendProposedEdges 把推導出的邊寫進提案檔，走既有的 review → gate → apply 流程。
//
// 刻意【不】直接寫 edges.jsonl：雖然推導是確定性的、不可能幻覺，但「新的邊怎麼進生效檔」
// 只留一條路徑比較好稽核，而且 gate 的去重與 hub 封頂本來就該套用在它身上。
func AppendProposedEdges(root string, edges []StoredEdge) (int, error) {
	return appendEdges(proposedEdgesPath(root), edges)
}
