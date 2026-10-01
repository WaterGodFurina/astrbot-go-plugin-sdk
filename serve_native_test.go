package sdk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeWriteRendezvous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rendezvous.json")
	if err := writeNativeRendezvous(path, "unix:///tmp/x.sock"); err != nil {
		t.Fatalf("writeNativeRendezvous: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rendezvous: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("rendezvous file empty")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file should be renamed away, stat err=%v", err)
	}
}

func TestNativeNewListener(t *testing.T) {
	lis, target, err := newNativeListener()
	if err != nil {
		t.Fatalf("newNativeListener: %v", err)
	}
	defer func() { _ = lis.Close() }()
	if target == "" {
		t.Fatal("target empty")
	}
	if addr := lis.Addr(); addr == nil || addr.Network() == "" {
		t.Fatalf("bad listener addr: %v", addr)
	}
}

func TestNativeServeNoPlugin(t *testing.T) {
	Register(nil) // 确保未注册
	t.Setenv(envNativeRendezvous, filepath.Join(t.TempDir(), "r.json"))
	if code := NativeServe(); code == 0 {
		t.Fatal("NativeServe with no registered plugin should return non-zero")
	}
}
