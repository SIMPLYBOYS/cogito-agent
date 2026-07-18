package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// POST /config：跨站被擋；同源則 clamp 後落地 .claw/config.json（越界夾回、0=不覆蓋）。
func TestConfigSave_ClampsAndCSRF(t *testing.T) {
	ws := t.TempDir()
	srv := newServer(nil, "", ws, nil)

	// 跨站 → 403（不寫入）
	req := httptest.NewRequest("POST", "/config", strings.NewReader("max_cost=99999"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("跨站 POST /config 應 403，got %d", rec.Code)
	}

	// 同源 + 越界值 → 303 + clamp 落地
	body := "max_turns=9999&max_concurrent=0&max_cost=99999"
	req2 := httptest.NewRequest("POST", "/config", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Sec-Fetch-Site", "same-origin")
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)
	if rec2.Code != 303 {
		t.Fatalf("同源 POST /config 應 303，got %d", rec2.Code)
	}
	k, ok := evolve.LoadKnobs(ws)
	lim := evolve.Limits()
	if !ok {
		t.Fatal("應寫出 .claw/config.json")
	}
	if k.MaxCostUSD != lim.MaxCostUSD || k.MaxTurns != lim.MaxTurns {
		t.Errorf("越界值應夾回上限，got %+v（上限 turns=%d cost=%.1f）", k, lim.MaxTurns, lim.MaxCostUSD)
	}
	if k.MaxConcurrentTools != 0 {
		t.Errorf("0 應保留為不覆蓋，got %d", k.MaxConcurrentTools)
	}
}
