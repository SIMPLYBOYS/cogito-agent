package context

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// MemoryRecord 是一筆離散的長期記憶（.claw/memory/<slug>.md）：frontmatter 帶 name/description/tags，
// body 是正文。與技能（SKILL.md）同構——差別在「記憶」是沉澱的事實/慣例/教訓，「技能」是操作流程。
type MemoryRecord struct {
	Name        string
	Description string
	Tags        []string
	Body        string
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

func (m *MemoryLoader) loadAll() []MemoryRecord {
	base := m.dir()
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil
	}
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
				recs = append(recs, rec)
			}
		}
		return nil
	})
	return recs
}

// LoadIndex 把記憶的【元數據】放進 System Prompt（漸進式）；正文不載入，模型需要時用 recall 取回。
func (m *MemoryLoader) LoadIndex() string {
	recs := m.loadAll()
	if len(recs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n### 長期記憶索引 (Long-term Memory)\n")
	b.WriteString("以下是你過往沉澱的長期記憶【索引】（僅標題與摘要）。當前任務若與某條相關，先用 `recall` 工具按關鍵字取回正文再參考，不要憑空臆測。\n")
	for _, r := range recs {
		tag := ""
		if len(r.Tags) > 0 {
			tag = " [" + strings.Join(r.Tags, ", ") + "]"
		}
		b.WriteString(fmt.Sprintf("- **%s**：%s%s\n", r.Name, r.Description, tag))
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
	for i, s := range ranked {
		out[i] = s.rec
	}
	return out
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
