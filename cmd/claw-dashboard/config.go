package main

import (
	"net/http"
	"strconv"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

// configSave 就地寫入執行時護欄（.claw/config.json）。這是【寫入】——CSRF 防護同 chat；值一律經
// evolve.WriteActiveKnobs 的 clamp（越界夾回、0=不覆蓋），故即使表單被繞過送 max_cost=99999 也擋在界內。
func (s *server) configSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "跨站請求被拒（CSRF 防護）", http.StatusForbidden)
		return
	}
	k := evolve.Knobs{
		MaxTurns:           atoiOr0(r.FormValue("max_turns")),
		MaxConcurrentTools: atoiOr0(r.FormValue("max_concurrent")),
		MaxCostUSD:         atofOr0(r.FormValue("max_cost")),
	}
	if _, err := evolve.WriteActiveKnobs(s.workspace, k); err != nil {
		http.Error(w, "寫入護欄失敗："+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/platform", http.StatusSeeOther)
}

func atoiOr0(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}

func atofOr0(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	if f < 0 {
		return 0
	}
	return f
}
