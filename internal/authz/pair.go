package authz

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 配對（pairing）：讓「未授權者自助發起、管理員回應」取代「管理員先動」。
//
// 【為何要碼，不直接批准 user id】沒有碼的話，管理員要批准的是 U08KJ2M3X 這種原始 ID——
// 沒辦法確認那串字是不是他以為的那個人。碼是「剛才發問的那個人」的短期、人類可讀的把手，
// 且待審項一併顯示平台／顯示名／時間供交叉比對。
//
// 【為何與授權記錄分檔】待審是短命資料（十分鐘就過期），授權記錄是永久稽核軌跡。混在一起會讓
// 稽核檔被高頻改寫，且任何人都能觸發那些寫入。

// PendingFileName 是待審配對請求的檔名（與稽核用的 authorized-users.json 分開）。
const PendingFileName = "pairing-pending.json"

// PairTTL 是配對碼有效期。短是刻意的：碼一旦外洩，攻擊窗口就這麼長。
const PairTTL = 10 * time.Minute

// maxPending 是待審佇列上限。任何人都能發起請求，沒有上限就是一條免費的洗版管道；
// 額滿時拒絕新請求而非淘汰舊的——否則攻擊者可以把真人的請求擠掉。
const maxPending = 50

// pairAlphabet 刻意排除易混淆字元（0/O、1/I/L）——碼要靠人唸、靠人打。
const pairAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// PairRequest 是一筆待審的配對請求。
type PairRequest struct {
	Code      string `json:"code"`
	Entry     string `json:"entry"`    // 授權條目（platform:id），批准時直接寫進記錄
	Display   string `json:"display"`  // 顯示名，供管理員辨認是誰
	Platform  string `json:"platform"` // 冗餘但便於 UI 分組
	Created   string `json:"created"`
	ExpiresAt string `json:"expires_at"`
}

// Expired 判斷是否已過期（時間格式壞掉一律視為過期——fail-closed）。
func (p PairRequest) Expired(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, p.ExpiresAt)
	return err != nil || now.After(t)
}

func (s *Store) pendingPath() string { return filepath.Join(filepath.Dir(s.path), PendingFileName) }

// RequestPair 建立（或更新）一筆待審請求並回傳配對碼。
//
// 同一 entry 重複請求會【換發新碼並覆蓋舊的】，不會累積多筆——否則一個人狂發就能塞爆佇列，
// 且管理員會看到同一人的多個碼、不知道該批哪個。
func (s *Store) RequestPair(platform, userID, display string) (PairRequest, error) {
	platform, userID = strings.TrimSpace(platform), strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return PairRequest{}, fmt.Errorf("平台與使用者 id 皆不可為空")
	}
	entry := platform + ":" + userID

	// 已經有權的人不必配對——直接告訴他，免得管理員收到無意義的待審。
	if allowed, _, err := s.Sets(); err == nil && (allowed[entry] || allowed[userID]) {
		return PairRequest{}, fmt.Errorf("你已經有權限了，不需要配對")
	}

	code, err := newPairCode()
	if err != nil {
		return PairRequest{}, err
	}
	now := time.Now()
	req := PairRequest{
		Code: code, Entry: entry, Display: strings.TrimSpace(display), Platform: platform,
		Created: now.Format(time.RFC3339), ExpiresAt: now.Add(PairTTL).Format(time.RFC3339),
	}

	err = s.mutatePending(func(list []PairRequest) ([]PairRequest, error) {
		out := make([]PairRequest, 0, len(list)+1)
		for _, p := range list {
			if p.Entry == entry || p.Expired(now) {
				continue // 同一人換發新碼；順手清掉過期的
			}
			out = append(out, p)
		}
		if len(out) >= maxPending {
			return nil, fmt.Errorf("待審佇列已滿（%d 筆），請管理員先處理", maxPending)
		}
		return append(out, req), nil
	})
	if err != nil {
		return PairRequest{}, err
	}
	return req, nil
}

// Pending 列出未過期的待審請求（順手不回傳過期項，但不改檔——清理留給下次寫入）。
func (s *Store) Pending() ([]PairRequest, error) {
	list, err := s.readPending()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]PairRequest, 0, len(list))
	for _, p := range list {
		if !p.Expired(now) {
			out = append(out, p)
		}
	}
	return out, nil
}

// ApprovePair 以配對碼批准一筆請求：寫進授權記錄並從待審移除。by 是批准者，寫入軌跡。
// 回傳被批准的請求，供呼叫端通知當事人。
func (s *Store) ApprovePair(code, role, by string) (PairRequest, error) {
	req, err := s.takePending(code)
	if err != nil {
		return PairRequest{}, err
	}
	if err := s.Approve(req.Entry, role, by); err != nil {
		return PairRequest{}, err
	}
	return req, nil
}

// RejectPair 否決一筆請求：只從待審移除，不寫授權記錄。
func (s *Store) RejectPair(code string) (PairRequest, error) { return s.takePending(code) }

// takePending 取出並移除一筆未過期的待審請求。碼比對【不分大小寫】——人會用小寫打。
func (s *Store) takePending(code string) (PairRequest, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return PairRequest{}, fmt.Errorf("請提供配對碼")
	}
	var found PairRequest
	err := s.mutatePending(func(list []PairRequest) ([]PairRequest, error) {
		now := time.Now()
		out := make([]PairRequest, 0, len(list))
		for _, p := range list {
			switch {
			case p.Code == code && !p.Expired(now):
				found = p // 命中：不放回去
			case p.Expired(now):
				// 順手清掉過期的
			default:
				out = append(out, p)
			}
		}
		if found.Code == "" {
			return nil, fmt.Errorf("找不到有效的配對碼 %q（可能已過期或已處理）", code)
		}
		return out, nil
	})
	if err != nil {
		return PairRequest{}, err
	}
	return found, nil
}

func (s *Store) readPending() ([]PairRequest, error) {
	data, err := os.ReadFile(s.pendingPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Pending []PairRequest `json:"pending"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失敗: %w", PendingFileName, err)
	}
	return doc.Pending, nil
}

// mutatePending 讀-改-寫待審檔。用獨立的 pmu：ApprovePair 是「先取待審、再寫授權記錄」，
// 若共用 mu 會在 Approve 內層重入而自我死鎖。持有順序一律 pmu → mu。
func (s *Store) mutatePending(fn func([]PairRequest) ([]PairRequest, error)) error {
	s.pmu.Lock()
	defer s.pmu.Unlock()

	list, err := s.readPending()
	if err != nil {
		return err
	}
	next, err := fn(list)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(struct {
		Pending []PairRequest `json:"pending"`
	}{next}, "", "  ")
	if err != nil {
		return err
	}
	path := s.pendingPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newPairCode 產生 6 碼配對碼。用 crypto/rand——碼是短期授權憑證，可預測就等於任何人都能
// 冒領別人的待審請求。
func newPairCode() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("產生配對碼失敗: %w", err)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = pairAlphabet[int(b)%len(pairAlphabet)]
	}
	return string(out), nil
}
