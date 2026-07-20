package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

type fakeProvider struct{ content string }

func (f *fakeProvider) Generate(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{Role: schema.RoleAssistant, Content: f.content}, nil
}
func (f *fakeProvider) MaxContextTokens() int { return 200000 }
func (f *fakeProvider) ModelName() string     { return "fake" }

func TestInjectLessons(t *testing.T) {
	if got := injectLessons("做事", nil); got != "做事" {
		t.Errorf("無教訓應原樣回傳，got %q", got)
	}
	got := injectLessons("做事", []string{"用 printf 不要用 echo", "先 build 再 test"})
	for _, want := range []string{"做事", "教訓", "1. 用 printf", "2. 先 build"} {
		if !strings.Contains(got, want) {
			t.Errorf("回注結果應含 %q\n---\n%s", want, got)
		}
	}
}

func TestReflectOnFailure(t *testing.T) {
	fp := &fakeProvider{content: "```json\n{\"lesson\": \"macOS 的 echo 不解釋 \\\\n，請改用 printf\"}\n```"}
	lesson, err := ReflectOnFailure(context.Background(), fp, "寫多行檔",
		[]schema.Message{{Role: schema.RoleUser, Content: "寫多行檔"}}, "檔案內容是字面 \\n")
	if err != nil {
		t.Fatalf("ReflectOnFailure 失敗: %v", err)
	}
	if !strings.Contains(lesson, "printf") {
		t.Errorf("應萃取出教訓，got %q", lesson)
	}
}

func TestReflectOnFailure_BadJSON(t *testing.T) {
	fp := &fakeProvider{content: "我覺得應該用 printf"}
	if _, err := ReflectOnFailure(context.Background(), fp, "t", nil, "fail"); err == nil {
		t.Error("非 JSON 輸出應回 error")
	}
}
