package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
	"github.com/SIMPLYBOYS/cogito-agent/internal/tools"
)

// 同時實作 Generate + GenerateStream 的 stub。
type streamStub struct{ gen, stream int }

func (s *streamStub) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	s.gen++
	return &schema.Message{Role: schema.RoleAssistant, Content: "once"}, nil
}
func (s *streamStub) GenerateStream(_ context.Context, _ []schema.Message, _ []schema.ToolDefinition, onDelta func(string)) (*schema.Message, error) {
	s.stream++
	onDelta("he")
	onDelta("llo")
	return &schema.Message{Role: schema.RoleAssistant, Content: "hello"}, nil
}
func (s *streamStub) MaxContextTokens() int { return 1000 }
func (s *streamStub) ModelName() string     { return "stub" }

// 只實作 Generate（非串流 provider）。
type plainStub struct{ gen int }

func (s *plainStub) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	s.gen++
	return &schema.Message{Role: schema.RoleAssistant, Content: "plain"}, nil
}
func (s *plainStub) MaxContextTokens() int { return 1000 }
func (s *plainStub) ModelName() string     { return "plain" }

// 有 sink + provider 支援 → 走串流、delta 流到 sink；無 sink → 走 Generate。
func TestGenerateAction_StreamsOnlyWithSink(t *testing.T) {
	sp := &streamStub{}
	e := NewAgentEngine(sp, tools.NewRegistry(), false, false)

	if _, err := e.generateAction(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if sp.gen != 1 || sp.stream != 0 {
		t.Fatalf("無 sink 應走 Generate，got gen=%d stream=%d", sp.gen, sp.stream)
	}

	var got []string
	ctx := WithStreamSink(context.Background(), func(d string) { got = append(got, d) })
	if _, err := e.generateAction(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if sp.stream != 1 {
		t.Fatalf("有 sink 應走 GenerateStream，got stream=%d", sp.stream)
	}
	if strings.Join(got, "") != "hello" {
		t.Errorf("delta 應逐筆到 sink，got %v", got)
	}
}

// 非串流 provider 即使有 sink 也安全退回 Generate（bot/cli 用 provider 不受影響）。
func TestGenerateAction_FallsBackWhenNoStreamingSupport(t *testing.T) {
	pp := &plainStub{}
	e := NewAgentEngine(pp, tools.NewRegistry(), false, false)
	ctx := WithStreamSink(context.Background(), func(string) { t.Error("非串流 provider 不該吐 delta") })
	if _, err := e.generateAction(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if pp.gen != 1 {
		t.Errorf("應退回 Generate，got gen=%d", pp.gen)
	}
}
