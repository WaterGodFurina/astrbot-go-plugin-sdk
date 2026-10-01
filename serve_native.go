package sdk

import (
	"encoding/json"
	"fmt"
	"os"

	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
	"github.com/hashicorp/go-hclog"
)

// Native 运行方式的宿主↔插件 rendezvous 环境变量。插件 .so/.dll 加载进宿主
// 进程后，二者共享同一进程环境变量，因此无需任何 IPC 即可交换地址。
const (
	// envNativeRendezvous 由宿主设置为 rendezvous 文件路径；Native runtime
	// 把本插件的 PluginService listener 地址写入该文件，宿主轮询后连接。
	envNativeRendezvous = "ASTRBOT_NATIVE_RENDEZVOUS"
	// envNativeHostAddr 由宿主设置为宿主 HostService 的 gRPC target；
	// Native runtime 据此建立插件→宿主的反向调用通道。
	envNativeHostAddr = "ASTRBOT_NATIVE_HOST_ADDR"
)

// nativeRendezvous 是写入党名 rendezvous 文件的结构。插件侧只写 PluginAddr；
// 宿主侧写入 HostAddr（供插件读取，目前经 envNativeHostAddr 传递，保留该
// 字段以便后续扩展为纯文件握手）。
type nativeRendezvous struct {
	PluginAddr string `json:"plugin_addr,omitempty"`
	HostAddr   string `json:"host_addr,omitempty"`
}

// NativeServe 是 Native 运行方式的插件侧入口。宿主 Loader 加载插件
// （Unix plugin.Open / Windows LoadDLL）后 Lookup 并调用注入的
// AstrBotNativeServe（package main），该入口必须先 sdk.Register(p) 再调用本
// 函数。
//
// 流程：跑 OnLoad → 合并注册 → 在本机回环 listener 上启动插件 PluginService
// （原生 gRPC，非 go-plugin）→ 把 listener 地址写入 rendezvous 文件 → 连接
// 宿主 HostService（反向调用）→ 阻塞服务。
//
// 返回进程退出码（0 = 正常服务，非 0 = 启动失败）。
func NativeServe() int {
	p := currentRegisteredPlugin()
	if p == nil {
		fmt.Fprintln(os.Stderr, "[ASTRBOT] NATIVE_ERROR: no plugin registered (call sdk.Register before sdk.NativeServe)")
		return 1
	}
	if p.OnLoad != nil {
		if err := p.OnLoad(); err != nil {
			// 与 gRPC 路径一致的 STARTUP_ERROR 协议行，宿主可解析展示。
			fmt.Fprintf(os.Stderr, "[ASTRBOT] STARTUP_ERROR phase=plugin_load type=%T plugin=%s error=%s\n",
				err, p.Name, singleLine(err.Error()))
			return 1
		}
	}
	global.drain(p)

	rendezvousPath := os.Getenv(envNativeRendezvous)
	if rendezvousPath == "" {
		fmt.Fprintln(os.Stderr, "[ASTRBOT] NATIVE_ERROR: "+envNativeRendezvous+" is not set")
		return 1
	}

	lis, target, err := newNativeListener()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ASTRBOT] NATIVE_ERROR: create listener: %v\n", err)
		return 1
	}
	defer func() { _ = lis.Close() }()

	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "astrbot-plugin." + p.Name,
		Level:  hclog.Info,
		Output: os.Stderr,
	})
	setServiceLogger(logger)

	srv := grpcServer(nil)
	sdkv1.RegisterPluginServiceServer(srv, &serviceServer{impl: p})
	go func() { _ = srv.Serve(lis) }()

	if hostTarget := os.Getenv(envNativeHostAddr); hostTarget != "" {
		// 记录宿主 HostService 地址：Host API 反向调用经 hostServiceClient()
		// 的 native 分支直接拨号（不走 go-plugin broker）。
		setNativeHostTarget(hostTarget)
	} else {
		logService().Warn(envNativeHostAddr + " is not set; plugin reverse calls (Host API) will fail")
	}

	if err := writeNativeRendezvous(rendezvousPath, target); err != nil {
		fmt.Fprintf(os.Stderr, "[ASTRBOT] NATIVE_ERROR: write rendezvous: %v\n", err)
		return 1
	}
	logService().Info("native plugin serving", "name", p.Name, "addr", target)

	// 与 gRPC 的 Serve 一样永久阻塞；宿主轮询 rendezvous 拿到地址后连接，
	// 之后所有 RPC 均经本机回环 listener。
	select {}
}

// writeNativeRendezvous 原子地把插件 listener target 写入 rendezvous 文件
// （先写临时文件再 Rename，避免宿主读到半截内容）。
func writeNativeRendezvous(path, target string) error {
	data, err := json.Marshal(nativeRendezvous{PluginAddr: target})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
