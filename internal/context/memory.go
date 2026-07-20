package context

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// maxIndexEntries 是 System Prompt 常駐記憶索引的條數上限（依最近使用排序取前 N）；其餘記憶不列入
// 索引，但仍可被 recall 檢索到——避免記憶一多連「索引」本身都把上下文撐爆。
const maxIndexEntries = 30

// MemoryRecord 是一筆離散的長期記憶（.claw/memory/<slug>.md）：frontmatter 帶 name/description/tags，
// body 是正文。與技能（SKILL.md）同構——差別在「記憶」是沉澱的事實/慣例/教訓，「技能」是操作流程。
type MemoryRecord struct {
	Name        string
	Description string
	Tags        []string
	Body        string

	Path   string    // 記錄檔路徑（recall 命中時記帳、Prune 歸檔用）
	usedAt time.Time // 最近使用時間：優先取自使用帳本，帳本無則退回檔案 mtime——排序/淘汰依據
}

// MemoryLoader 是長期記憶的漸進式載入端（對齊 SkillLoader）：System Prompt 只放索引（名稱+描述+標籤），
// 正文由 recall 工具按需檢索載入——避免記憶一多就把上下文撐爆（取代「AGENTS.md 整檔每輪全載」）。
type MemoryLoader struct {
	workDir string
}

func NewMemoryLoader(workDir string) *MemoryLoader {
	return &MemoryLoader{workDir: workDir}
}

func (m *MemoryLoader) dir() string { return filepath.Join(m.workDir, ".claw", "memory") }

// ── 使用帳本（sidecar usage ledger）─────────────────────────────────────────
// app 自己記每筆記憶的「最近使用時間 + 命中次數」，取代用檔案 mtime 當 last-used。mtime 會被備份 /
// rsync / 編輯器等外部碰檔而誤更新——害「其實沒被 agent 用到」的記憶被當成剛用過、逃過淘汰（反之
// 常用記憶若某天沒被 recall、檔案又被冷落，也可能被誤歸檔）。帳本是 app 私有訊號，不受外部碰檔影響。
// 存 .claw/memory-usage.json（basename → 使用資料）。Hits 目前只記著，供日後 LFU/頻率淘汰（#3）；
// 現階段淘汰仍看 recency（LastUsed）。

const memUsageFile = "memory-usage.json"

// memUsageMu 序列化帳本的 read-modify-write：同一 bot 行程內多頻道共用同一記憶目錄、recall 可能併發；
// 不同「員工」是不同 workDir、各自帳本，無跨行程競爭。recall 非熱路徑，單一全域鎖足矣。
var memUsageMu sync.Mutex

type memoryUsage struct {
	LastUsed time.Time `json:"last_used"`
	Hits     int       `json:"hits"`
}

func (m *MemoryLoader) usagePath() string {
	return filepath.Join(m.workDir, ".claw", memUsageFile)
}

// loadUsage 讀帳本；缺檔 / 壞檔一律回空 map——帳本是輔助訊號，絕不因它壞掉而讓 recall 失效。
func (m *MemoryLoader) loadUsage() map[string]memoryUsage {
	data, err := os.ReadFile(m.usagePath())
	if err != nil {
		return map[string]memoryUsage{}
	}
	var u map[string]memoryUsage
	if json.Unmarshal(data, &u) != nil || u == nil {
		return map[string]memoryUsage{}
	}
	return u
}

// saveUsage 原子寫（temp + rename，避免半截檔被讀到）。呼叫端須持有 memUsageMu。
func (m *MemoryLoader) saveUsage(u map[string]memoryUsage) {
	data, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return
	}
	tmp := m.usagePath() + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, m.usagePath())
}

// recordHits 把一批命中的記錄標記為「剛用到」：LastUsed=now、Hits++。批次做一次 load-modify-save
// （不是每筆一次），全程持鎖避免併發遺失計數。取代舊的 os.Chtimes——不再寫/讀檔案 mtime 當訊號。
func (m *MemoryLoader) recordHits(paths []string) {
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != "" {
			names = append(names, filepath.Base(p))
		}
	}
	if len(names) == 0 {
		return
	}
	now := time.Now()
	memUsageMu.Lock()
	defer memUsageMu.Unlock()
	u := m.loadUsage()
	for _, n := range names {
		e := u[n]
		e.LastUsed = now
		e.Hits++
		u[n] = e
	}
	m.saveUsage(u)
}

// lastUsed 解析一筆記錄的最近使用時間：優先帳本，帳本無則退回檔案 mtime（僅在 seedMissing 尚未把它
// 寫進帳本前的短暫窗口會走到，之後一律走帳本）。
func lastUsed(u map[string]memoryUsage, name string, fileMtime time.Time) time.Time {
	if e, ok := u[name]; ok && !e.LastUsed.IsZero() {
		return e.LastUsed
	}
	return fileMtime
}

// seedMissing 把「還沒進帳本」的記錄首次觀察到的 mtime 凍進帳本（Hits=0＝從未被 recall）。
// 這是 #2 的關鍵一步：若不 seed，一筆從未被 recall 的記錄永遠 fallback 到即時 mtime，於是「把全部
// 檔案 mtime 設成 now」的批次污染（git checkout / rsync / 備份還原）會讓沒用到的記錄看起來剛用過、
// 贏過帳本裡記著真實使用時間的常用記錄——淘汰決策整個反掉。凍住創建時間後，這筆的 recency 就是
// app 私有的、免疫日後任何外部碰檔。代價：純內容編輯（罕見，記憶通常寫一次）不會再自動抬升 recency。
func (m *MemoryLoader) seedMissing(seeds map[string]time.Time) {
	if len(seeds) == 0 {
		return
	}
	memUsageMu.Lock()
	defer memUsageMu.Unlock()
	u := m.loadUsage()
	changed := false
	for name, mt := range seeds {
		if _, ok := u[name]; !ok { // 鎖內重查：期間可能已被別的 goroutine seed 或 recall
			u[name] = memoryUsage{LastUsed: mt}
			changed = true
		}
	}
	if changed {
		m.saveUsage(u)
	}
}

func (m *MemoryLoader) loadAll() []MemoryRecord {
	base := m.dir()
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil
	}
	u := m.loadUsage()
	seeds := map[string]time.Time{}
	var recs []MemoryRecord
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			if content, e := os.ReadFile(path); e == nil {
				rec := parseMemoryMD(string(content))
				if rec.Name == "" {
					rec.Name = strings.TrimSuffix(d.Name(), ".md") // 無 frontmatter name 時退回檔名
				}
				rec.Path = path
				if info, e := d.Info(); e == nil {
					mt := info.ModTime()
					rec.usedAt = lastUsed(u, d.Name(), mt)
					if _, ok := u[d.Name()]; !ok {
						seeds[d.Name()] = mt // 首次觀察：把當下 mtime 凍進帳本當創建時間（見 seedMissing）
					}
				}
				recs = append(recs, rec)
			}
		}
		return nil
	})
	m.seedMissing(seeds)
	return recs
}

// LoadIndex 把記憶的【元資料】放進 System Prompt（漸進式）；正文不載入，模型需要時用 recall 取回。
func (m *MemoryLoader) LoadIndex() string {
	recs := m.loadAll()
	if len(recs) == 0 {
		return ""
	}
	// 依最近使用排序（帳本優先，缺則檔案 mtime），索引只常駐前 maxIndexEntries 條；其餘仍可被 recall 檢索到。
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].usedAt.After(recs[j].usedAt) })
	hidden := 0
	if len(recs) > maxIndexEntries {
		hidden = len(recs) - maxIndexEntries
		recs = recs[:maxIndexEntries]
	}
	var b strings.Builder
	b.WriteString("\n### 長期記憶索引 (Long-term Memory)\n")
	b.WriteString("以下是你過往沉澱的長期記憶【索引】（僅標題與摘要，依最近使用排序）。當前任務若與某條相關，先用 `recall` 工具按關鍵字取回正文再參考，不要憑空臆測。\n")
	for _, r := range recs {
		tag := ""
		if len(r.Tags) > 0 {
			tag = " [" + strings.Join(r.Tags, ", ") + "]"
		}
		fmt.Fprintf(&b, "- **%s**：%s%s\n", r.Name, r.Description, tag)
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "- …（另有 %d 條未列於索引，需要時直接用 `recall` 關鍵字檢索）\n", hidden)
	}
	return b.String()
}

// Recall 依關鍵字/標籤對記憶評分，回傳最相關的前 k 筆。零依賴的關鍵字檢索。
// ponytail: 關鍵字/CJK bigram 評分；若精度不夠再換 embedding 餘弦（介面不變、只動 score/tokenize）。
func (m *MemoryLoader) Recall(query string, k int) []MemoryRecord {
	recs := m.loadAll()
	terms := tokenize(query)
	if len(recs) == 0 || len(terms) == 0 {
		return nil
	}
	type scored struct {
		rec   MemoryRecord
		score int
	}
	var ranked []scored
	for _, r := range recs {
		if s := scoreRecord(r, terms); s > 0 {
			ranked = append(ranked, scored{r, s})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if k > 0 && len(ranked) > k {
		ranked = ranked[:k]
	}
	out := make([]MemoryRecord, len(ranked))
	hits := make([]string, len(ranked))
	for i, s := range ranked {
		out[i] = s.rec
		hits[i] = s.rec.Path
	}
	m.recordHits(hits) // 命中即記帳（最近使用 + 命中次數），讓常用記憶留在索引、冷門的被淘汰
	return out
}

// Prune 把超過 keep 上限的「最久未用」記錄歸檔到 .claw/memory-archive/（可復原，非刪除——記憶操作
// 是新的失控控制面，寧可歸檔不硬刪）。回傳被歸檔的檔名。keep<=0 或未超量則不動。
func (m *MemoryLoader) Prune(keep int) []string {
	base := m.dir()
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	u := m.loadUsage()
	type rec struct {
		name   string
		usedAt time.Time
	}
	var files []rec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if info, ierr := e.Info(); ierr == nil {
			files = append(files, rec{e.Name(), lastUsed(u, e.Name(), info.ModTime())}) // 帳本優先，缺則檔案 mtime
		}
	}
	if keep <= 0 || len(files) <= keep {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].usedAt.After(files[j].usedAt) }) // 新→舊
	archiveDir := filepath.Join(m.workDir, ".claw", "memory-archive")
	if os.MkdirAll(archiveDir, 0o755) != nil {
		return nil
	}
	var archived []string
	for _, old := range files[keep:] { // 超出 keep 的最舊者
		if os.Rename(filepath.Join(base, old.name), filepath.Join(archiveDir, old.name)) == nil {
			archived = append(archived, old.name)
		}
	}
	// 歸檔者從帳本剔除（避免帳本無限長）。檔案可復原；若日後 restore，第一次會用檔案 mtime 重新入帳。
	if len(archived) > 0 {
		memUsageMu.Lock()
		cur := m.loadUsage()
		for _, n := range archived {
			delete(cur, n)
		}
		m.saveUsage(cur)
		memUsageMu.Unlock()
	}
	return archived
}

// Records 回傳所有記憶記錄（供 evolve 的 LLM 關係抽取等外部使用）。
func (m *MemoryLoader) Records() []MemoryRecord { return m.loadAll() }

// Vectors 回傳節點向量快取（供記憶檢索評測等外部使用）；無快取則為空 map。
func (m *MemoryLoader) Vectors() map[string][]float32 { return readVectors(EmbedCachePath(m.workDir)) }

// RecallGraph 是 KG 檢索：種子→k 跳子圖→序列化；命中節點記帳（最近使用 + 命中次數）。回傳空字串＝無命中。
// 取代「平面 top-k」：回傳的是連通鄰域 + 明確關係，讓 LLM 能做多跳關係推理（RAG 做不到）。
// emb 非 nil 且有向量快取時用 embedding 語意選種子（混合）；否則退回關鍵字（emb=nil 為預設、零依賴）。
func (m *MemoryLoader) RecallGraph(query string, hops int, emb Embedder) string {
	if hops <= 0 {
		hops = 1
	}
	g := m.Graph()
	var seeds []string
	if emb != nil {
		if cache := readVectors(EmbedCachePath(m.workDir)); len(cache) > 0 {
			if qv, err := emb.EmbedQuery(query); err == nil {
				seeds = g.SeedsEmbed(qv, cache, recallSeeds)
			}
		}
	}
	if len(seeds) == 0 {
		seeds = g.Seeds(query, recallSeeds) // 退回關鍵字（embedding 未配置/失敗/快取缺）
	}
	if len(seeds) == 0 {
		return ""
	}
	nodes, edges := g.Subgraph(seeds, hops, recallBudget)
	hits := make([]string, 0, len(nodes))
	for _, n := range nodes {
		hits = append(hits, n.Path) // stub 節點 Path="" → recordHits 內部略過
	}
	m.recordHits(hits)
	return RenderSubgraph(nodes, edges)
}

// scoreRecord：tags > name > description > body 加權的關鍵字命中加總。
func scoreRecord(r MemoryRecord, terms []string) int {
	tagStr := strings.ToLower(strings.Join(r.Tags, " "))
	name := strings.ToLower(r.Name)
	desc := strings.ToLower(r.Description)
	body := strings.ToLower(r.Body)
	score := 0
	for _, t := range terms {
		if strings.Contains(tagStr, t) {
			score += 4
		}
		if strings.Contains(name, t) {
			score += 3
		}
		if strings.Contains(desc, t) {
			score += 2
		}
		if strings.Contains(body, t) {
			score++
		}
	}
	return score
}

// tokenize 把查詢切成檢索詞：英數整詞；中文無詞界，退化成 bigram（標準零依賴 CJK n-gram 技巧），
// 單字則保留該字。回傳去重後的小寫詞。
func tokenize(s string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if runes := []rune(tok); hasCJK(tok) && len(runes) >= 2 {
			for i := 0; i+1 < len(runes); i++ {
				add(string(runes[i : i+2]))
			}
		} else {
			add(tok)
		}
	}
	return out
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// parseMemoryMD 解析記憶記錄（frontmatter name/description/tags + body）。沿用技能 frontmatter 的格式。
func parseMemoryMD(content string) MemoryRecord {
	rec := MemoryRecord{Body: strings.TrimSpace(content)}
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) == 3 {
			rec.Body = strings.TrimSpace(parts[2])
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "name:"):
					rec.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				case strings.HasPrefix(line, "description:"):
					rec.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				case strings.HasPrefix(line, "tags:"):
					rec.Tags = parseTags(strings.TrimPrefix(line, "tags:"))
				}
			}
		}
	}
	return rec
}

func parseTags(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(strings.Trim(t, `"'`)); t != "" {
			out = append(out, t)
		}
	}
	return out
}
