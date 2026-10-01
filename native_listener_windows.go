//go:build windows

package sdk

import (
	"net"
)

// newNativeListener 在 Windows 上创建本机回环 listener：127.0.0.1 随机端口
// TCP（Go plugin 不支持 Windows，Native 用 c-shared DLL + 本机回环 gRPC）。
// 返回 listener 与其 gRPC target（"127.0.0.1:<port>"）。
func newNativeListener() (net.Listener, string, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	return lis, lis.Addr().String(), nil
}
