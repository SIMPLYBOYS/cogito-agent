package authz

import "testing"

// 「未設 admin ⇒ 可對話者即可審批」只該對人的平台成立；開關打開後 admin 只來自顯式來源。
func TestNoInheritAdmin(t *testing.T) {
	allowed := map[string]bool{"office-web": true}
	s := New(t.TempDir(), allowed, nil)
	if _, admin, _ := s.Sets(); !admin["office-web"] {
		t.Fatal("預設行為：沒設 admin 時可對話者即可審批")
	}
	s.SetNoInheritAdmin(true)
	if _, admin, _ := s.Sets(); len(admin) != 0 {
		t.Fatalf("開關打開後不該繼承 allowed 當 admin，實得 %v", admin)
	}
	// 記錄檔裡顯式給的 admin 仍然算——開關擋的是【隱含】繼承，不是顯式授權
	if err := s.Approve("office-boss", RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	if _, admin, _ := s.Sets(); !admin["office-boss"] || admin["office-web"] {
		t.Fatalf("顯式 admin 要生效、派工者仍不是 admin，實得 %v", admin)
	}
}
