package evolve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

// fakeProvider 回傳預設內容，模擬反思 LLM 的輸出。
type fakeProvider struct{ content string }

func (f *fakeProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: f.content}, nil
}
func (f *fakeProvider) MaxContextTokens() int { return 200000 }
func (f *fakeProvider) ModelName() string     { return "fake" }

func TestReflect_WorthSaving_WritesToProposed(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{content: `{"worth_saving": true, "name": "Run Go Tests", "description": "當需要驗證 Go 變更時", "body": "1. go build\n2. go test ./..."}`}
	s := NewSkillSynthesizer(fp, dir)

	path, err := s.Reflect(context.Background(), "幫我跑測試", []schema.Message{{Role: schema.RoleUser, Content: "幫我跑測試"}})
	if err != nil {
		t.Fatalf("Reflect 失敗: %v", err)
	}
	if path == "" {
		t.Fatal("應寫出提案技能檔")
	}
	// folder-per-skill：<slug>/SKILL.md
	if filepath.Base(path) != "SKILL.md" {
		t.Errorf("檔名應為 SKILL.md，got %s", filepath.Base(path))
	}
	if filepath.Base(filepath.Dir(path)) != "run-go-tests" {
		t.Errorf("技能資料夾名應為 run-go-tests，got %s", filepath.Base(filepath.Dir(path)))
	}
	data, _ := os.ReadFile(path)
	body := string(data)
	for _, want := range []string{"name: run-go-tests", "description: 當需要驗證 Go 變更時", "version: 1", "go test ./...", "需人工 review"} {
		if !strings.Contains(body, want) {
			t.Errorf("提案技能檔應含 %q\n---\n%s", want, body)
		}
	}
}

func TestReflect_NotWorthSaving_NoFile(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{content: `{"worth_saving": false}`}
	s := NewSkillSynthesizer(fp, dir)

	path, err := s.Reflect(context.Background(), "刪個暫存檔", nil)
	if err != nil {
		t.Fatalf("Reflect 失敗: %v", err)
	}
	if path != "" {
		t.Errorf("不值得保存時不應寫檔，got %s", path)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("目錄應為空，got %d 個檔", len(entries))
	}
}

func TestReflect_StripsJSONFences(t *testing.T) {
	dir := t.TempDir()
	fp := &fakeProvider{content: "```json\n{\"worth_saving\": true, \"name\": \"x\", \"description\": \"d\", \"body\": \"step\"}\n```"}
	s := NewSkillSynthesizer(fp, dir)
	path, err := s.Reflect(context.Background(), "t", nil)
	if err != nil {
		t.Fatalf("應能解析帶 ```json 圍欄的輸出: %v", err)
	}
	if path == "" {
		t.Fatal("應寫出檔案")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Run Go Tests":   "run-go-tests",
		"  A/B  c ":      "a-b-c",
		"技能":             "proposed-skill", // 全非 ASCII → 退回預設
		"deploy_to_prod": "deploy-to-prod",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q)=%q，want %q", in, got, want)
		}
	}
}
