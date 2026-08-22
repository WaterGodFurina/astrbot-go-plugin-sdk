package sdk

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
)

// TestNormalizePermission 验证权限归一化（子任务 F2）：admin / everyone / 空串
// （大小写不敏感、忽略首尾空白）静默返回；仅真正未知的值才 Warn 并回退
// everyone。原实现对合法的 everyone / 空串也打 Warn，属误报。
func TestNormalizePermission(t *testing.T) {
	var buf bytes.Buffer
	logMu.Lock()
	prev := serviceLogger
	logMu.Unlock()
	setServiceLogger(hclog.New(&hclog.LoggerOptions{Level: hclog.Info, Output: &buf}))
	defer func() {
		logMu.Lock()
		serviceLogger = prev
		logMu.Unlock()
	}()

	cases := []struct {
		in   string
		want string
		warn bool
	}{
		{"admin", "admin", false},
		{"  ADMIN ", "admin", false},
		{"everyone", "everyone", false},
		{"Everyone", "everyone", false},
		{"", "everyone", false},
		{"   ", "everyone", false},
		{"root", "everyone", true},
		{"超级管理员", "everyone", true},
	}
	for _, c := range cases {
		buf.Reset()
		if got := normalizePermission(c.in); got != c.want {
			t.Errorf("normalizePermission(%q) = %q, want %q", c.in, got, c.want)
		}
		if logged := strings.Contains(buf.String(), "未知"); logged != c.warn {
			t.Errorf("normalizePermission(%q): want warn=%v, log=%q", c.in, c.warn, buf.String())
		}
	}
}
