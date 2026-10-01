//go:build linux || darwin || freebsd

package sdk

import (
	"net"
	"os"
	"path/filepath"
)

// cleanupListener 包装 unix listener：Close 时一并删除临时目录（socket 文件
// 所在目录），避免每次 Native 加载泄漏一个临时目录。
type cleanupListener struct {
	net.Listener
	dir string
}

func (l *cleanupListener) Close() error {
	err := l.Listener.Close()
	_ = os.RemoveAll(l.dir)
	return err
}

// newNativeListener 在 Unix 上创建本机回环 listener：unix domain socket。
// 返回 listener 与其 gRPC target（"unix://<绝对路径>"，供宿主 grpc 拨号）。
func newNativeListener() (net.Listener, string, error) {
	dir, err := os.MkdirTemp("", "astrbot-native-*")
	if err != nil {
		return nil, "", err
	}
	sock := filepath.Join(dir, "plugin.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", err
	}
	return &cleanupListener{Listener: lis, dir: dir}, "unix://" + sock, nil
}
