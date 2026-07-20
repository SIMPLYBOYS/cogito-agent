// Package policy 是工具調用的權限模型：Deny > Ask > Allow。
//
// 【為何需要 DENY 這一層】原本只有兩種結果：命中危險黑名單→找人審批（ASK），否則放行（ALLOW）。
// 這在「有人在現場」時夠用，但排程任務是【無人值守】跑的——沒有人可以問，ASK 就退化成「阻塞到
// 逾時再拒絕」，既慢又只是碰巧安全。DENY 是不依賴任何人在場的那一層。
//
// 【為何用宣告式政策檔】黑名單寫死在程式裡，換個部署情境（例如對外開放的 agent 想永遠禁掉 bash）
// 就得改程式重編。政策檔讓「這個部署允許什麼」與程式碼分離。
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Action 是對一次工具調用的裁決。
type Action string

const (
	Allow Action = "allow" // 自動放行
	Ask   Action = "ask"   // 需人工確認；無人可問時視為 Deny
	Deny  Action = "deny"  // 永遠拒絕，不問任何人
)

// rank 決定合併多條命中規則時誰說了算：Deny > Ask > Allow。
// 刻意不採「第一條命中就贏」——規則順序不該影響安全性，寫錯順序就破防的設計太脆。
func (a Action) rank() int {
	switch a {
	case Deny:
		return 3
	case Ask:
		return 2
	case Allow:
		return 1
	}
	return 0
}

// Rule 是一條政策。Tool 為工具名（"*" 代表全部）；Match 是對參數的正則（選填，空＝只看工具名）。
type Rule struct {
	Tool   string `json:"tool"`
	Match  string `json:"match,omitempty"`
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`
}

type compiled struct {
	Rule
	re *regexp.Regexp
}

// Policy 是一組規則。零值（或 nil）代表「無政策」——Decide 一律回空裁決，由呼叫端走內建預設。
type Policy struct{ rules []compiled }

// ConfigPath 回政策檔路徑（<workspace>/.claw/policy.json）。
func ConfigPath(workspace string) string {
	return filepath.Join(workspace, ".claw", "policy.json")
}

// Load 讀政策檔。缺檔＝無政策（不是錯誤——多數部署不需要自訂）。
// 格式錯／正則編不過則回錯，由呼叫端決定要不要 fail-closed；不靜默忽略，免得以為有保護其實沒有。
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Policy{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rules []Rule `json:"rules"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 policy.json 失敗: %w", err)
	}

	p := &Policy{}
	for i, r := range doc.Rules {
		switch r.Action {
		case Allow, Ask, Deny:
		default:
			return nil, fmt.Errorf("policy.json 第 %d 條規則的 action 需為 allow/ask/deny，實得 %q", i+1, r.Action)
		}
		if strings.TrimSpace(r.Tool) == "" {
			return nil, fmt.Errorf("policy.json 第 %d 條規則缺少 tool（用 \"*\" 代表全部）", i+1)
		}
		c := compiled{Rule: r}
		if r.Match != "" {
			re, err := regexp.Compile(r.Match)
			if err != nil {
				return nil, fmt.Errorf("policy.json 第 %d 條規則的 match 正則無效: %w", i+1, err)
			}
			c.re = re
		}
		p.rules = append(p.rules, c)
	}
	return p, nil
}

// Decide 對一次工具調用裁決。無任何規則命中時回 ("", "")，代表「政策沒意見」，由呼叫端走內建預設。
// 參數比對前一律 lowercase，故政策檔的正則寫小寫即可涵蓋大小寫變體。
func (p *Policy) Decide(tool, args string) (Action, string) {
	if p == nil {
		return "", ""
	}
	low := strings.ToLower(args)
	var best Action
	var reason string
	for _, c := range p.rules {
		if c.Tool != "*" && c.Tool != tool {
			continue
		}
		if c.re != nil && !c.re.MatchString(low) {
			continue
		}
		if c.Action.rank() > best.rank() {
			best, reason = c.Action, c.Reason
		}
	}
	return best, reason
}
