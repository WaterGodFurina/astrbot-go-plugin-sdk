package sdk

import "testing"

func TestNativeRegisterCapturesPlugin(t *testing.T) {
	p := &Plugin{Name: "t", Version: "1.0.0"}
	Register(p)
	if got := currentRegisteredPlugin(); got != p {
		t.Fatalf("Register/currentRegisteredPlugin mismatch: got %v want %v", got, p)
	}
	Register(nil)
	if got := currentRegisteredPlugin(); got != nil {
		t.Fatalf("Register(nil) should clear: got %v", got)
	}
	Register(p)
	if got := currentRegisteredPlugin(); got != p {
		t.Fatalf("re-Register failed: got %v want %v", got, p)
	}
	Register(nil) // 清理，避免影响同包其他测试
}
