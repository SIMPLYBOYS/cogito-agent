// cmd/skillgate 是「提案技能」的把關與晉升工具（Tier 4 安全閘）。
//
//	go run ./cmd/skillgate                      # 列出提案技能 + 逐一把關（review 輔助）
//	go run ./cmd/skillgate -promote run-go-tests.md   # 把關通過才晉升到 .claw/skills/（生效）
//
// 安全鐵律：提案技能不會自動啟用；晉升前一律過確定性把關（結構 + 安全黑名單），人選哪個、自動擋壞的。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SIMPLYBOYS/cogito-agent/internal/evolve"
)

func main() {
	proposed := flag.String("proposed", "./workspace/.claw/"+evolve.ProposedSkillsDirName, "提案技能目錄")
	active := flag.String("active", "./workspace/.claw/"+evolve.ActiveSkillsDirName, "生效技能目錄（晉升目標）")
	promote := flag.String("promote", "", "要晉升的提案技能檔名（把關通過才移到生效目錄）")
	flag.Parse()

	if *promote != "" {
		doPromote(filepath.Join(*proposed, *promote), *active)
		return
	}
	doReport(*proposed)
}

func doReport(proposedDir string) {
	entries, err := os.ReadDir(proposedDir)
	if err != nil {
		fmt.Printf("讀取提案技能目錄失敗：%v\n（尚無提案技能，或目錄不存在）\n", err)
		return
	}
	count := 0
	fmt.Printf("=== 提案技能把關報告（%s）===\n", proposedDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		count++
		res, err := evolve.Gate(filepath.Join(proposedDir, e.Name()))
		if err != nil {
			fmt.Printf("• %s  ⚠️ 讀取失敗: %v\n", e.Name(), err)
			continue
		}
		if res.Passed {
			fmt.Printf("• %s  ✅ 通過（可 -promote %s 晉升）\n", e.Name(), e.Name())
		} else {
			fmt.Printf("• %s  ❌ 不通過：\n", e.Name())
			for _, iss := range res.Issues {
				fmt.Printf("    - %s\n", iss)
			}
		}
	}
	if count == 0 {
		fmt.Println("（目錄中沒有提案技能）")
	}
}

func doPromote(path, activeDir string) {
	res, err := evolve.Promote(path, activeDir)
	if err != nil {
		fmt.Printf("晉升出錯：%v\n", err)
		os.Exit(1)
	}
	if !res.Passed {
		fmt.Printf("❌ 把關不通過，已拒絕晉升 %s：\n", filepath.Base(path))
		for _, iss := range res.Issues {
			fmt.Printf("  - %s\n", iss)
		}
		os.Exit(1)
	}
	fmt.Printf("✅ 把關通過，已晉升 %s → %s（現已生效）\n", filepath.Base(path), activeDir)
}
