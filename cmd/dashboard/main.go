// cmd/dashboard 是 benchmark 跑分結果的輕量視覺化：一個自包含的 Go HTTP 服務，讀 cmd/bench
// 產出的 JSON 報告目錄（bench-*.json），渲染成功率/逐用例（回合·試錯·成本·耗時）表格與歷次趨勢。
// 零前端建置、零外部依賴。
//
//	go run ./cmd/bench -out ./bench-reports        # 先產生報告
//	go run ./cmd/dashboard -dir ./bench-reports    # 再開儀表板 → http://localhost:8090
package main

import (
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/eval"
)

func main() {
	dir := flag.String("dir", "./bench-reports", "benchmark JSON 報告所在目錄")
	addr := flag.String("addr", ":8090", "監聽位址")
	flag.Parse()

	http.HandleFunc("/", dashboardHandler(*dir))
	log.Printf("📊 Benchmark 儀表板已啟動：http://localhost%s （報告目錄：%s）", *addr, *dir)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// loadReports 讀目錄下所有 *.json 報告，解析為 SuiteReport，按產生時間新→舊排序。
func loadReports(dir string) ([]eval.SuiteReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var reports []eval.SuiteReport
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r eval.SuiteReport
		if json.Unmarshal(data, &r) == nil && r.GeneratedAt != "" {
			reports = append(reports, r)
		}
	}
	// GeneratedAt 是 RFC3339，字串字典序即時間序；反轉成新→舊。
	sort.Slice(reports, func(i, j int) bool { return reports[i].GeneratedAt > reports[j].GeneratedAt })
	return reports, nil
}

func dashboardHandler(dir string) http.HandlerFunc {
	tmpl := template.Must(template.New("dash").Funcs(template.FuncMap{
		"pct": func(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + "%" },
		"usd": func(f float64) string { return "$" + strconv.FormatFloat(f, 'f', 6, 64) },
		"pass": func(b bool) template.HTML {
			if b {
				return "✅"
			}
			return "❌"
		},
	}).Parse(pageTemplate))

	return func(w http.ResponseWriter, r *http.Request) {
		reports, err := loadReports(dir)
		data := pageData{Dir: dir}
		if err != nil {
			data.Err = "讀取報告目錄失敗：" + err.Error()
		} else if len(reports) == 0 {
			data.Err = "目錄中沒有報告（先跑 `go run ./cmd/bench -out " + dir + "`）"
		} else {
			data.Latest = &reports[0]
			data.History = reports
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := tmpl.Execute(w, data); e != nil {
			http.Error(w, e.Error(), http.StatusInternalServerError)
		}
	}
}

type pageData struct {
	Dir     string
	Latest  *eval.SuiteReport
	History []eval.SuiteReport
	Err     string
}

const pageTemplate = `<!doctype html>
<html lang="zh-Hant"><head><meta charset="utf-8"><title>cogito-agent · Benchmark 儀表板</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",monospace;margin:0;background:#0f1116;color:#e6e6e6}
  .wrap{max-width:980px;margin:0 auto;padding:32px 20px}
  h1{font-size:20px;margin:0 0 4px} .sub{color:#8b93a7;font-size:13px;margin-bottom:24px}
  .cards{display:flex;gap:16px;flex-wrap:wrap;margin-bottom:28px}
  .card{background:#1a1d26;border:1px solid #272b36;border-radius:10px;padding:16px 20px;min-width:140px}
  .card .k{color:#8b93a7;font-size:12px} .card .v{font-size:24px;font-weight:600;margin-top:4px}
  .bar{height:8px;background:#272b36;border-radius:4px;overflow:hidden;margin-top:10px}
  .bar>i{display:block;height:100%;background:linear-gradient(90deg,#3fb950,#56d364)}
  table{width:100%;border-collapse:collapse;font-size:13px;margin-bottom:32px}
  th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #272b36}
  th{color:#8b93a7;font-weight:500} td.num{text-align:right;font-variant-numeric:tabular-nums}
  .err{color:#f85149} h2{font-size:15px;margin:24px 0 10px;color:#c9d1d9}
  .muted{color:#6b7280;font-size:12px}
</style></head><body><div class="wrap">
<h1>📊 cogito-agent · Benchmark 儀表板</h1>
<div class="sub">報告目錄：{{.Dir}}</div>
{{if .Err}}<p class="err">{{.Err}}</p>{{else}}
{{with .Latest}}
<div class="cards">
  <div class="card"><div class="k">通過率</div><div class="v">{{pct .PassRate}}</div>
    <div class="bar"><i style="width:{{pct .PassRate}}"></i></div></div>
  <div class="card"><div class="k">通過 / 總數</div><div class="v">{{.Passed}} / {{.Total}}</div></div>
  <div class="card"><div class="k">總成本</div><div class="v">{{usd .TotalCostUSD}}</div></div>
  <div class="card"><div class="k">模型</div><div class="v" style="font-size:15px">{{.Model}}</div>
    <div class="muted">{{.GeneratedAt}}</div></div>
</div>
<h2>最新一次 · 逐用例</h2>
<table><thead><tr><th>用例</th><th>結果</th><th class="num">回合</th><th class="num">試錯</th>
  <th class="num">耗時(ms)</th><th class="num">成本</th><th>錯誤</th></tr></thead><tbody>
{{range .Results}}<tr><td>{{.TestCaseID}}</td><td>{{pass .Passed}}</td>
  <td class="num">{{.TurnCount}}</td><td class="num">{{.ToolErrorCount}}</td>
  <td class="num">{{.DurationMs}}</td><td class="num">{{usd .TotalCostUSD}}</td>
  <td class="err">{{.ErrorMsg}}</td></tr>{{end}}
</tbody></table>
{{end}}
<h2>歷次趨勢</h2>
<table><thead><tr><th>時間</th><th>模型</th><th class="num">通過率</th>
  <th class="num">通過/總數</th><th class="num">成本</th></tr></thead><tbody>
{{range .History}}<tr><td>{{.GeneratedAt}}</td><td>{{.Model}}</td>
  <td class="num">{{pct .PassRate}}</td><td class="num">{{.Passed}}/{{.Total}}</td>
  <td class="num">{{usd .TotalCostUSD}}</td></tr>{{end}}
</tbody></table>
{{end}}
</div></body></html>`
