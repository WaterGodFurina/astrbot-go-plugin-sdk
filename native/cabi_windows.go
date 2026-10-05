//go:build windows

// Windows Native transport: the plugin is built as a c-shared DLL and the host
// calls it through a minimal C ABI (LoadLibrary + GetProcAddress). Go's stdlib
// `plugin` is unsupported on Windows, and c-shared cannot carry Go interface
// values across the boundary, so request/response cross as sdkv1 protobuf
// bytes. No loopback gRPC, no JSON, no second protocol is introduced.
//
// The C surface is intentionally tiny (4 symbols):
//
//	AstrBotPluginOpen(pluginID, handleOut)        load + init (OnLoad)
//	AstrBotPluginCall(handle, method, req, ...)   dispatch one PluginService call
//	AstrBotFree(p)                                free a response buffer
//	AstrBotPluginClose(handle)                    lifecycle teardown (OnUnload)
//
// Memory ownership: response buffers are allocated by the DLL and MUST be freed
// with AstrBotFree (the host calls it). Requests are owned by the host.
package native

/*
#include <stdint.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"sync"
	"unsafe"

	sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk"
	sdkv1 "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/gen/sdkv1"
)

// cabiPlugin 是宿主可见的插件句柄背后的状态。
type cabiPlugin struct {
	svc     sdk.PluginService
	stopped bool
}

var (
	cabiPluginsMu sync.Mutex
	cabiPlugins   = map[uintptr]*cabiPlugin{}
	cabiNextID    uintptr
)

// SetPlugin 由注入的 native_entry.go（Windows 分支）在初始化时调用，登记插件
// 配置；AstrBotPluginOpen 随后用于构建 PluginService。
func SetPlugin(p *sdk.Plugin) { nativeCABIPlugin = p }

// nativeCABIPlugin 是插件配置（main() 不会执行，native_entry 提前设置）。
var nativeCABIPlugin *sdk.Plugin

//export AstrBotPluginOpen
func AstrBotPluginOpen(pluginID *C.char, handleOut **C.uintptr_t) C.int {
	if handleOut == nil || nativeCABIPlugin == nil {
		return 1
	}
	pluginName := ""
	if pluginID != nil {
		pluginName = C.GoString(pluginID)
	}
	// 与 Unix native.Serve 对齐：绑定本 DLL 内的 host service，使插件反向调用
	// （Host.GetConfig 等）走同一代码路径。Windows DLL 是独立 runtime，宿主的
	// HostServiceHooks 不会跨 DLL 传入，未注入时反向调用返回不可用（与 Unix
	// 未注入 hooks 时一致），不影响命令/钩子/工具等主路径。
	hs := sdk.NewHostServiceServer(pluginName, pluginName)
	sdk.SetHostCallerFunc(func() (sdk.HostService, error) { return hs, nil })
	svc := sdk.NewPluginService(nativeCABIPlugin)
	cp := &cabiPlugin{svc: svc}
	cabiPluginsMu.Lock()
	cabiNextID++
	handle := cabiNextID
	cabiPlugins[handle] = cp
	cabiPluginsMu.Unlock()
	*handleOut = C.uintptr_t(handle)
	return 0
}

//export AstrBotPluginCall
func AstrBotPluginCall(handle C.uintptr_t, method *C.char, reqPtr *C.uint8_t, reqLen C.int, out **C.uint8_t, outLen *C.int) C.int {
	if method == nil || out == nil || outLen == nil {
		return 1
	}
	cabiPluginsMu.Lock()
	cp := cabiPlugins[uintptr(handle)]
	cabiPluginsMu.Unlock()
	if cp == nil {
		return 2
	}
	m := C.GoString(method)
	var req []byte
	if reqPtr != nil && reqLen > 0 {
		req = C.GoBytes(unsafe.Pointer(reqPtr), reqLen)
	}
	resp, err := cabiCall(cp.svc, m, req)
	if err != nil {
		// 错误路径：返回码 3，缓冲为错误文本（非 protobuf），宿主据返回码
		// 区分。主路径（返回码 0）始终是 sdkv1 protobuf bytes。
		msg := []byte(err.Error())
		if len(msg) > 0 {
			cb := C.CBytes(msg)
			*out = (*C.uint8_t)(cb)
			*outLen = C.int(len(msg))
		} else {
			*out, *outLen = nil, 0
		}
		return 3
	}
	if len(resp) == 0 {
		*out, *outLen = nil, 0
		return 0
	}
	cb := C.CBytes(resp)
	*out = (*C.uint8_t)(cb)
	*outLen = C.int(len(resp))
	return 0
}

//export AstrBotFree
func AstrBotFree(p *C.uint8_t) {
	if p != nil {
		C.free(unsafe.Pointer(p))
	}
}

//export AstrBotPluginClose
func AstrBotPluginClose(handle C.uintptr_t) {
	cabiPluginsMu.Lock()
	cp := cabiPlugins[uintptr(handle)]
	delete(cabiPlugins, uintptr(handle))
	cabiPluginsMu.Unlock()
	if cp == nil || cp.stopped {
		return
	}
	cp.stopped = true
	_, _ = cp.svc.Cleanup(context.Background(), &sdkv1.PluginRef{})
}
