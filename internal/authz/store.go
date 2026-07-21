// Package authz 是入站授權的資料層：把「誰能使喚 agent、誰能批危險操作」從靜態 env 清單
// 升級成可增刪、帶稽核欄位的記錄檔（.claw/authorized-users.json）。
//
// 【為何要這層】env 清單只回答「現在誰有權」，不回答「誰批准的、何時、有沒有人被撤銷過」。
// 授權從【狀態】變成【事件】，才有稽核軌跡；而且加人／撤銷不必改 .env + 重啟——撤銷若要等
// 重啟，等於沒有撤銷。
//
// 【與 env 的關係】env 是 bootstrap 不是遺留：第一個 admin 必須從檔案外面來，否則雞生蛋
// （沒人有權批准第一個人）。故最終集合 = env ∪ 檔案中 status=approved 者。
//
// 【快取以 mtime 失效】每次查詢先 os.Stat（一次 syscall，不讀內容），mtime+size 沒變就重用
// 已解析的結果；變了才重讀重解。這同時拿到兩件事——不重複解析，且【任何行程】改檔後下一次查詢
// 立刻生效。撤銷若要等重啟或等 TTL，等於沒有撤銷，故不用時間型快取。
package authz

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileName 是授權記錄檔在 .claw/ 下的檔名。
const FileName = "authorized-users.json"

// 角色。admin 蘊含 user——一個能批准危險操作、卻不能使喚 agent 的帳號不是任何人要的狀態。
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// 狀態。撤銷【不刪記錄】：留著才有「誰在何時被撤銷」的軌跡，那正是本層存在的理由。
const (
	StatusApproved = "approved"
	StatusRevoked  = "revoked"
)

// Record 是一筆授權事件。Entry 直接沿用 env 的條目格式——"platform:id"（只在該平台生效）或
// 裸 "id"（任何平台皆生效）。沿用而非另立格式，是為了讓 chatbot 的 inSet 判定邏輯一行都不用改。
type Record struct {
	Entry      string `json:"entry"`
	Role       string `json:"role"`
	Status     string `json:"status"`
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

// Store 綁定一個 .claw 目錄與 bootstrap 用的 env 集合。零值不可用，請用 New。
// 可被多個 goroutine 併用（bot 的多個對話同時進來）。
type Store struct {
	path       string
	envAllowed map[string]bool
	envAdmin   map[string]bool

	mu     sync.Mutex
	cached []Record  // 上次解析結果
	stamp  fileStamp // 對應的檔案指紋；不符即重讀
	valid  bool      // 是否有可用的快取（含「檔案不存在」這個合法狀態）

	// pmu 保護待審配對檔（見 pair.go）。與 mu 分開：ApprovePair 會先改待審再改授權記錄，
	// 共用一把鎖會在 mutate 內層重入而自我死鎖。持有順序一律 pmu → mu，不得反向。
	pmu sync.Mutex
}

// fileStamp 是判斷「檔案有沒有變」的指紋。用 mtime+size 而非內容雜湊——後者要讀完整個檔，
// 那就失去快取的意義了。
//
// ponytail: mtime 精度受檔案系統限制（APFS/ext4 到奈秒，但某些 FS 只到秒）。同一秒內兩次寫入
// 且大小恰好相同才會漏判——授權是人手動作，撞上的機率可忽略。真要滴水不漏就改比 inode+ctime
// 或加 fsnotify。
type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

// New 建 Store。envAllowed／envAdmin 是 bootstrap 集合（呼叫端從環境變數解出），會被聯集進結果。
func New(clawDir string, envAllowed, envAdmin map[string]bool) *Store {
	return &Store{path: filepath.Join(clawDir, FileName), envAllowed: envAllowed, envAdmin: envAdmin}
}

// stampOf 取檔案當下的指紋。os.Stat 不讀內容，是本快取便宜的關鍵。
func (s *Store) stampOf() fileStamp {
	fi, err := os.Stat(s.path)
	if err != nil {
		return fileStamp{} // 不存在（或讀不到）：exists=false，本身也是個合法的可快取狀態
	}
	return fileStamp{exists: true, modTime: fi.ModTime(), size: fi.Size()}
}

// Path 回傳記錄檔位置（供面板顯示／錯誤訊息）。
func (s *Store) Path() string { return s.path }

// Records 讀出全部記錄（含已撤銷的，供稽核檢視）。檔案不存在＝空清單，非錯誤。
//
// 快取：指紋未變即回上次解析結果。回傳的 slice 是複本——呼叫端改它不該污染快取。
func (s *Store) Records() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.stampOf()
	if s.valid && s.stamp == now {
		return append([]Record(nil), s.cached...), nil
	}

	recs, err := s.readLocked()
	if err != nil {
		s.valid = false // 壞檔不進快取：修好後下一次查詢就該看到新結果
		return nil, err
	}
	s.cached, s.stamp, s.valid = recs, now, true
	return append([]Record(nil), recs...), nil
}

// readLocked 實際讀檔解析（不碰快取）。呼叫端須持有 s.mu。
func (s *Store) readLocked() ([]Record, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Users []Record `json:"users"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失敗: %w", FileName, err)
	}
	return doc.Users, nil
}

// Sets 回傳「可使喚」與「可審批」兩個集合，格式與 parseUserSet 產出的完全一致，
// 供呼叫端交給既有的 inSet 判定（限定平台優先、退回裸 id）。
//
// 壞檔【不靜默】：回 err 讓呼叫端記錄並退回 env-only。這是刻意的中間值——壞檔既不該
// 悄悄放行任何人，也不該悄悄撤銷所有人（那會把 bootstrap admin 一起鎖在門外，沒人能修）。
func (s *Store) Sets() (allowed, admin map[string]bool, err error) {
	allowed, admin = copySet(s.envAllowed), copySet(s.envAdmin)
	if len(admin) == 0 {
		admin = copySet(allowed) // 對齊既有語意：未單獨設 admin 時，可對話者即可審批
	}

	recs, err := s.Records()
	if err != nil {
		return allowed, admin, err // env-only，呼叫端據 err 記錄
	}
	for _, r := range recs {
		if r.Status != StatusApproved || r.Entry == "" {
			continue
		}
		allowed[r.Entry] = true
		if r.Role == RoleAdmin {
			admin[r.Entry] = true
		}
	}
	return allowed, admin, nil
}

// Approve 授權一個條目（已存在則更新為 approved 並改寫角色）。by 是批准者的 user id，寫進軌跡。
//
// ponytail: 讀-改-寫沒有跨行程鎖。授權是人手動作、頻率極低，兩個 admin 同一秒批准不同人才會
// 掉更新。真的在意就把 cron 那支 flock 抽成共用套件再套上來。
func (s *Store) Approve(entry, role, by string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return fmt.Errorf("條目不可為空")
	}
	if role != RoleUser && role != RoleAdmin {
		return fmt.Errorf("未知角色 %q（只接受 %s／%s）", role, RoleUser, RoleAdmin)
	}
	return s.mutate(func(recs []Record) []Record {
		now := time.Now().Format(time.RFC3339)
		for i := range recs {
			if recs[i].Entry == entry {
				recs[i].Role, recs[i].Status = role, StatusApproved
				recs[i].ApprovedBy, recs[i].ApprovedAt = by, now
				recs[i].RevokedBy, recs[i].RevokedAt = "", ""
				return recs
			}
		}
		return append(recs, Record{
			Entry: entry, Role: role, Status: StatusApproved,
			ApprovedBy: by, ApprovedAt: now,
		})
	})
}

// Revoke 撤銷一個條目。記錄保留（狀態改為 revoked）——刪掉就沒有軌跡了。
//
// 注意：只能撤銷【檔案裡】的授權。env 裡的 bootstrap 條目撤不掉——那是刻意的，
// 否則從 UI 就能把最後一個 admin 鎖死，且重啟後又活過來，狀態自相矛盾。
func (s *Store) Revoke(entry, by string) error {
	entry = strings.TrimSpace(entry)
	if s.envAllowed[entry] || s.envAdmin[entry] {
		return fmt.Errorf("%q 來自環境變數（bootstrap），無法從此處撤銷——請改 .env 後重啟", entry)
	}
	found := false
	if err := s.mutate(func(recs []Record) []Record {
		now := time.Now().Format(time.RFC3339)
		for i := range recs {
			if recs[i].Entry == entry && recs[i].Status == StatusApproved {
				recs[i].Status, recs[i].RevokedBy, recs[i].RevokedAt = StatusRevoked, by, now
				found = true
			}
		}
		return recs
	}); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("找不到生效中的授權 %q", entry)
	}
	return nil
}

// mutate 讀-改-寫記錄檔（原子：temp + rename，權限 0600——這是授權資料）。
//
// 全程持鎖：行程內的併發批准不會互相蓋掉。寫完清快取，下一次查詢重讀。
func (s *Store) mutate(fn func([]Record) []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.valid = false }()

	recs, err := s.readLocked()
	if err != nil {
		return err // 壞檔時不覆寫，免得把既有記錄一起弄丟
	}
	out, err := json.MarshalIndent(struct {
		Users []Record `json:"users"`
	}{fn(recs)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func copySet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		if v {
			out[k] = true
		}
	}
	return out
}
