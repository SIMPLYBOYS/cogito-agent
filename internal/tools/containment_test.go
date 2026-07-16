package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 檔案工具的工作區圍堵（containment）端到端測試：重現 agent 的實際攻擊路徑：bash 種 symlink → 檔案工具（宿主機）跟著它逃出工作區。
func TestFileTools_ContainSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	wd := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("HOST-ONLY-SECRET"), 0o600)

	// 這一步在真實情境是容器內的 bash 幹的：ln -s /host/path /workspace/k
	os.Symlink(secret, filepath.Join(wd, "k"))
	os.Symlink(outside, filepath.Join(wd, "d"))

	call := func(tool BaseTool, args map[string]string) (string, error) {
		b, _ := json.Marshal(args)
		return tool.Execute(context.Background(), b)
	}

	out, err := call(NewReadFileTool(wd), map[string]string{"path": "k"})
	t.Logf("read_file 經檔案 symlink → err=%v out=%q", err, out)
	if err == nil {
		t.Error("read_file 讀到了工作區外的宿主機檔案")
	}

	_, err = call(NewWriteFileTool(wd), map[string]string{"path": "k", "content": "PWNED"})
	t.Logf("write_file 經檔案 symlink → err=%v", err)
	if err == nil {
		t.Error("write_file 寫進了工作區外")
	}
	if b, _ := os.ReadFile(secret); string(b) != "HOST-ONLY-SECRET" {
		t.Errorf("宿主機檔案被竄改了！現在是 %q", b)
	}

	out, err = call(NewReadFileTool(wd), map[string]string{"path": "d/secret.txt"})
	t.Logf("read_file 經目錄 symlink → err=%v out=%q", err, out)
	if err == nil {
		t.Error("read_file 經目錄 symlink 讀到了工作區外")
	}

	// 正常用法仍要能動
	if _, err := call(NewWriteFileTool(wd), map[string]string{"path": "sub/new.txt", "content": "hi"}); err != nil {
		t.Errorf("正常寫入（含建新目錄）被誤傷: %v", err)
	}
	out, err = call(NewReadFileTool(wd), map[string]string{"path": "sub/new.txt"})
	if err != nil || !strings.Contains(out, "hi") {
		t.Errorf("正常讀取被誤傷: err=%v out=%q", err, out)
	}
}
