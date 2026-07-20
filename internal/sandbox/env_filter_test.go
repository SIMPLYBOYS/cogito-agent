package sandbox

import (
	"context"
	"strings"
	"testing"
)

// host 執行器不得把本行程的金鑰交給 agent 的 bash。少了這層，一句 `env` 就外洩。
func TestHostExecutor_DoesNotLeakSecrets(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	t.Setenv("SLACK_BOT_TOKEN", "should-not-leak-either")
	t.Setenv("PATH", "/usr/bin:/bin")

	cmd, err := HostExecutor{}.Command(context.Background(), "true", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Env == nil {
		t.Fatal("cmd.Env 為 nil＝繼承全部環境變數，金鑰會外洩")
	}

	joined := strings.Join(cmd.Env, "\n")
	for _, leaked := range []string{"ANTHROPIC_API_KEY", "SLACK_BOT_TOKEN", "should-not-leak"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("環境變數含 %q，金鑰外洩", leaked)
		}
	}
	// 但基本變數要留著，否則命令根本跑不動
	if !strings.Contains(joined, "PATH=") {
		t.Error("PATH 被濾掉了——命令會找不到執行檔")
	}
}

// 白名單要能擴充，否則使用者的工具鏈變數被擋掉就只能改程式。
func TestFilteredEnv_ExtraPassthrough(t *testing.T) {
	t.Setenv("MY_TOOLCHAIN_VAR", "value-1")
	t.Setenv("MY_SECRET_VAR", "value-2")
	t.Setenv(passExtraKey, "MY_TOOLCHAIN_VAR")

	joined := strings.Join(filteredEnv(), "\n")
	if !strings.Contains(joined, "MY_TOOLCHAIN_VAR=value-1") {
		t.Error("列在 COGITO_SANDBOX_ENV_PASS 的變數應放行")
	}
	if strings.Contains(joined, "MY_SECRET_VAR") {
		t.Error("未列入的變數不該放行")
	}
}

// 端對端：真的跑一次 bash，確認金鑰讀不到、但基本環境仍在。
// 只檢查 cmd.Env 不夠——要確定它真的生效在子行程上。
func TestHostExecutor_SecretUnreadableFromBash(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-LIVE-SECRET-VALUE")

	out, err := HostExecutor{}.Run(context.Background(),
		`echo "KEY=[$ANTHROPIC_API_KEY]"; echo "PATH_SET=$([ -n "$PATH" ] && echo yes)"`, t.TempDir())
	if err != nil {
		t.Fatalf("執行失敗: %v（輸出：%s）", err, out)
	}
	s := string(out)

	if strings.Contains(s, "LIVE-SECRET") {
		t.Errorf("agent 的 bash 讀得到金鑰：%s", s)
	}
	if !strings.Contains(s, "KEY=[]") {
		t.Errorf("預期金鑰為空字串，實得：%s", s)
	}
	if !strings.Contains(s, "PATH_SET=yes") {
		t.Errorf("PATH 應保留，否則命令跑不動：%s", s)
	}
}
