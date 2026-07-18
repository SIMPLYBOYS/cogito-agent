package context

import (
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/schema"
)

func TestSession_Reset(t *testing.T) {
	s := NewSession("s1", "/tmp")
	s.Append(schema.Message{Role: schema.RoleUser, Content: "hi"})
	s.RecordUsage(10, 5, 0.01)
	if s.HistoryLen() == 0 {
		t.Fatal("setup：應有歷史")
	}
	s.Reset()
	if s.HistoryLen() != 0 {
		t.Error("Reset 應清空歷史")
	}
	if p, c, cost := s.Usage(); p != 0 || c != 0 || cost != 0 {
		t.Errorf("Reset 應歸零用量，got %d/%d/%f", p, c, cost)
	}
}
