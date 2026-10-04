# AstrBot Go 插件 SDK

为 AstrBot（Go 版）编写插件的 Go SDK。插件支持两种**运行方式**（宿主侧按插件选择，同一份源码无需修改）：

- **gRPC 子进程（默认）**：插件编译为可执行文件，以独立子进程运行，与宿主通过 gRPC（go-plugin）通信。进程隔离、可独立重启/卸载/闲置休眠。
- **Native 进程内**：插件编译为进程内动态库（Unix `.so`），由宿主 `plugin.Open` 加载并**直接函数调用**——不经 gRPC、不经 protobuf-RPC、不启动子进程。性能更高、内存更省，但**无进程隔离**——崩溃可能影响宿主；更新/禁用/卸载需重启 AstrBot；不支持闲置休眠。仅支持 Unix（Windows 无 Go `plugin`，`c-shared` 无法传 Go 接口）。

SDK 是独立 module `github.com/WaterGodFurina/AstrBot-go-plugin-sdk`（作为依赖从 GitHub 拉取；开发时本地 clone 到 `~/astrbot-go-plugin-sdk`）。

## 快速开始

插件作者只需写一个 `main`，实现命令/过滤器/钩子。插件身份信息（名称/版本/描述/作者/仓库/是否 cgo）统一放在包根目录的 `metadata.json`，`main.go` 只保留代码逻辑：

```go
package main

import (
    sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk"
)

// plugin 是插件定义（包级变量，见下文「Native 运行方式」）
var plugin = &sdk.Plugin{
    OnLoad: setup, // 启动钩子，可在里面动态注册
}

func main() {
    sdk.Serve(plugin)
}
```

```json
{
  "name": "echo",
  "desc": "Echoes your message back",
  "author": "AstrBot Devs",
  "version": "1.0.0",
  "repo": "https://github.com/AstrBotDevs/AstrBot",
  "cgo": false
}
```

插件包（zip/Git 仓库）根目录**必须**包含 `metadata.json` 与 `main.go`，缺任一即安装失败。`cgo` 字段声明该插件是否需要 C 编译器：为空/缺省视为 `false`（纯 Go，`CGO_ENABLED=0`）。

## Native 运行方式

除了默认的 gRPC 子进程，宿主还支持把同一份插件源码构建为 **Native 进程内动态库**（Unix `-buildmode=plugin` 出 `.so`）用 `plugin.Open()` 加载到宿主进程，并**直接函数调用**插件接口——没有 gRPC、没有 protobuf-RPC、没有子进程、没有回环 socket。这就是 README 快速开始把 `&sdk.Plugin{...}` 提升为包级 `var plugin` 的原因：

- gRPC 构建：`func main()` 执行 → `sdk.Serve(plugin)`（宿主在编译时注入 `grpc_entry.go` 链接 gRPC 传输）。
- Native 构建：`func main()` **不执行**。宿主在编译时注入生成文件 `native_entry.go`（package main，不改动作者源码），把包级 `plugin` 交给 Native 运行时：

  ```go
  // native_entry.go（宿主 Native 构建时自动注入，作者无需编写）
  package main

  import native "github.com/WaterGodFurina/Astrbot-go-plugin-sdk/native"

  func AstrBotNativePlugin(pluginID string) (native.Plugin, error) {
      return native.Serve(plugin, pluginID)
  }
  ```

宿主 `plugin.Open()` 后 `Lookup("AstrBotNativePlugin")` 并调用它：`native.Serve` 跑 `OnLoad`、在进程内构造绑定该插件身份的 HostService，并返回 `PluginService` 供宿主**直接调用**。插件上层 API 与 gRPC 运行方式完全一致，无需感知运行方式。

> 要求 SDK **v1.9.0+**。宿主按插件选择运行方式（`plugins.json` manifest 的 `runtime` 字段），不写入插件的 `metadata.json`。

### Native 依赖边界

Native 插件只链接 SDK core + `gen/sdkv1`（protobuf 消息）。core 不导入 `google.golang.org/grpc`、`hashicorp/go-plugin`、`hashicorp/go-hclog`，因此 Native 插件构建**不下载、不链接**这些依赖（`go list -deps` 可验证）。protobuf 版本需与宿主一致（Go `plugin` 要求宿主与插件对共同链接包使用相同版本）。

### Native 生命周期注意事项

- 动态库加载进宿主进程后**不可卸载、不可安全重载同一路径的新版本**（Go `plugin` 语义）。
- 因此 Native 插件的重载只做可安全注销/注册的功能刷新（Command / Filter 等）；更新、禁用、卸载后新状态需**重启 AstrBot** 才完整生效。
- Native 插件**不参与闲置休眠**（无法 kill/唤醒）。
- Native 插件崩溃或内存问题可能影响宿主主进程——宿主侧会先弹风险警告，用户确认后才切换。
- **仅支持 Unix**：Windows 无 Go `plugin`，`c-shared` 的 C ABI 无法传递 Go 接口，宿主会明确报错，请改用 gRPC 运行方式。

## 命令

命令处理函数可以拆到独立文件，通过 `setup()`（OnLoad 钩子）或 `init()` 注册：

```go
func setup() error {
    sdk.RegisterCommand(sdk.Command{
        Name:    "echo",
        Aliases: []string{"repeat"},
        Handler: func(e *sdk.Event, args []string) (string, error) {
            return strings.Join(args, " "), nil
        },
    })
    return nil
}
```

SDK 也支持声明式写法（直接在 `sdk.Plugin{Commands: []sdk.Command{...}}` 里声明），两者等价。

### 命令返回富文本（消息链）

`ChainHandler` 优先于 `Handler`，可返回完整消息链（文本 + 图片 + 文件组件）：

```go
sdk.RegisterCommand(sdk.Command{
    Name: "pic",
    ChainHandler: func(e *sdk.Event, args []string) ([]sdk.Component, error) {
        return []sdk.Component{
            sdk.Text("看这张图："),
            sdk.ImageURL("https://example.com/a.png"),
        }, nil
    },
})
```

### 命令权限

`Permission` 限制谁能执行命令：`"everyone"`（默认）或 `"admin"`。大小写不敏感，非法值归一化为 `"everyone"`。

```go
sdk.Command{Name: "admin-cmd", Permission: "admin", Handler: ...}
```

### 子指令（命令分组）

`ParentGroup` + `IsSubCommand` 可把命令挂到某个命令组下作为子指令（`ParentGroup` 为空表示顶层命令）。宿主侧会按组聚合展示/匹配：

```go
// 组命令本身照常声明
sdk.Command{Name: "weather", Description: "天气相关", Handler: ...}
// 子指令：归属 weather 组
sdk.Command{Name: "now", ParentGroup: "weather", IsSubCommand: true, Handler: ...}
```

## 过滤器

过滤器返回 `false` 时拦截该事件（不再进入后续管线）：

```go
sdk.RegisterFilter(sdk.Filter{
    Name: "block-bad",
    Handler: func(e *sdk.Event) bool {
        return !strings.Contains(e.MessageStr, "bad")
    },
})
```

## 钩子

钩子订阅各类生命周期/管线事件（`Event` 字符串见下文"事件"）：

```go
sdk.RegisterHook(sdk.Hook{
    Name:  "log-all",
    Event: sdk.EventOnMessage,
    Handler: func(e *sdk.Event) error {
        // 处理每条入站消息
        return nil
    },
})
```

### 钩子类型一览

| 类型 | Plugin 字段 | 事件 | 作用 |
|---|---|---|---|
| 普通钩子 | `Hooks` | `on_message` 等 | 通用事件回调 |
| LLM 请求钩子 | `LLMRequestHooks` | `on_llm_request` | LLM 调用前检查/修改 system prompt（可 `Stop` 中止） |
| 结果装饰钩子 | `ResultHooks` | `on_decorating_result` / `on_result_handling` | 回复发送前装饰消息链 |
| 消息钩子 | `MessageHooks` | `on_message` / `on_message_received` / `on_pre_process` | 观察入站消息 |
| 发送后钩子 | `AfterMessageSentHooks` | `on_after_message_sent` | 回复发送后回调 |
| LLM 响应钩子 | `LLMResponseHooks` | `on_llm_response` | LLM 回复产生后 |
| 工具钩子 | `ToolCallHooks` / `ToolRespondHooks` | `on_using_llm_tool` / `on_llm_tool_respond` | LLM 工具执行前后 |
| 错误钩子 | `PluginErrorHooks` | `on_plugin_error` | 插件 handler 出错时 |
| 生命周期钩子 | `AstrbotLoadedHooks` / `PlatformLoadedHooks` / `PluginLoadedHooks` / `PluginUnloadedHooks` | `on_astrbot_loaded` / `on_platform_loaded` / `on_plugin_loaded` / `on_plugin_unloaded` | 宿主/平台/插件加载卸载 |
| Agent 钩子 | `AgentBeginHooks` / `AgentDoneHooks` | `on_agent_begin` / `on_agent_done` | Agent 运行开始/结束 |

## LLM 函数工具

工具暴露给模型在聊天中调用（对齐 Python AstrBot 的 `@filter.llm_tool` / `register_llm_tool`）：

```go
sdk.RegisterTool(sdk.Tool{
    Name:        "get_weather",
    Description: "查询指定城市的天气",
    ParamsSchema: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "city": map[string]any{"type": "string", "description": "城市名"},
        },
        "required": []string{"city"},
    },
    Handler: func(e *sdk.Event, args map[string]any) (string, error) {
        city, _ := args["city"].(string)
        return "晴天 25°C", nil
    },
})
```

## Web API

插件可以注册 Dashboard Web UI 的 API 路由，宿主在 `/api/plug/<插件名>/<route>` 下代理：

```go
sdk.Plugin{WebAPIs: []sdk.WebAPI{
    {
        Route:   "/emoji/<category>",
        Methods: []string{"GET"},
        Desc:    "获取表情包列表",
        Handler: func(method, path string, query, headers map[string][]string, body []byte, pathParams map[string]string) (int, map[string]string, []byte, error) {
            cat := pathParams["category"]
            return 200, map[string]string{"Content-Type": "application/json"}, []byte(`{"ok":true}`), nil
        },
    },
}}
```

路由支持动态 `<param>` 段（如 `/emoji/<category>`），请求时匹配并传入 `pathParams`。

## 反向调用宿主（Host API）

插件 handler 内可通过全局 `sdk.Host` 反向调用宿主能力：

```go
// 发送消息到指定平台会话
err := sdk.Host.SendMessage("aiocqhttp", "123456", []sdk.Component{sdk.Text("你好")})

// 调用平台原生 action（OneBot 等）
data, err := sdk.Host.CallAction("aiocqhttp", "get_friend_list", nil)

// 撤回消息
err := sdk.Host.RecallMessage("aiocqhttp", "msg-id")

// 读取/写入插件配置
cfg, _ := sdk.Host.GetConfig("echo")
_ = sdk.Host.SetConfig("echo", map[string]any{"key": "value"})

// 请求宿主调用 LLM（非流式）
reply, err := sdk.Host.ChatLLM("你好", "你是助手", nil)

// 消息回应（QQ 等支持）
err := sdk.Host.React("aiocqhttp", "conv", "msg-id", "👍")

// 文本转图
url, err := sdk.Host.TextToImage("文字卡片", "default")
```

所有反向调用带 30s 默认超时。

## 会话等待（SessionWait）

等待"用户在该会话的下一条消息"（对齐 Python AstrBot 的 `session_waiter`，多轮确认/表单收集）。`RegisterSessionWait` 是 `*Plugin` 的方法，在 `OnLoad`/`setup` 中通过插件实例调用：

```go
var p = &sdk.Plugin{Name: "confirm"}

func setup() error {
    // 注册会话等待：用户在该会话的后续消息会触发 handler（消费一次后自动移除）
    p.RegisterSessionWait("aiocqhttp:GroupMessage:123", 90, func(e *sdk.Event) bool {
        // 返回 true 表示事件已被本等待消费；false 放行正常管线
        return true
    })
    return nil
}

func main() {
    p.OnLoad = setup
    sdk.Serve(p)
}
```

宿主收到该 umo 的后续消息时，经 `FeedSessionWait` 推送给插件，触发 `Handler` 一次后自动移除。`UnregisterSessionWait(umo)` 可手动注销；超时自动清理。

## 事件（Event）

`Event` 是入站消息的轻量序列化视图，字段：

```go
type Event struct {
    Type        string            // 事件类型
    Platform    string            // 平台类型名（aiocqhttp/qq_official/...）
    PlatformID  string            // 平台实例 id（config.id）
    MessageType string            // GroupMessage / FriendMessage / OtherMessage
    SelfID      string            // 机器人自身 id
    SenderID    string            // 发送者 id
    SenderName  string            // 发送者昵称
    ConvID      string            // 会话 id（群聊=群 id，私聊=发送者 id）
    GroupName   string            // 群名（如有）
    IsGroup     bool              // 是否群聊
    IsAtBot     bool              // 是否 @ 机器人
    IsAdmin     bool              // 发送者是否管理员
    MessageStr  string            // 原始消息文本
    PlainText   string            // 纯文本（剥离 @ 等）
    MessageID   string            // 消息 id
    Timestamp   int64             // 时间戳（Unix 秒）
    Metadata    map[string]any    // 附加元数据
    Chain       []Component       // 消息链
}
```

常用辅助方法：`GetSenderID()`、`GetGroupID()`、`IsGroupMessage()`、`IsAdminUser()`、`GetPlatformID()`、`GetMessageType()`、`GetMessageStr()`。

### 事件常量

| 常量 | 值 |
|---|---|
| `EventOnMessage` | `on_message` |
| `EventOnMessageReceived` | `on_message_received` |
| `EventOnPreProcess` | `on_pre_process` |
| `EventOnAfterMessageSent` | `on_after_message_sent` |
| `EventOnWaitingLLMRequest` | `on_waiting_llm_request` |
| `EventOnLLMRequest` | `on_llm_request` |
| `EventOnLLMResponse` | `on_llm_response` |
| `EventOnUsingLLMTool` | `on_using_llm_tool` |
| `EventOnLLMToolRespond` | `on_llm_tool_respond` |
| `EventOnDecoratingResult` | `on_decorating_result` |
| `EventOnResultHandling` | `on_result_handling` |
| `EventOnPluginError` | `on_plugin_error` |
| `EventOnAstrbotLoaded` | `on_astrbot_loaded` |
| `EventOnPlatformLoaded` | `on_platform_loaded` |
| `EventOnPluginLoaded` | `on_plugin_loaded` |
| `EventOnPluginUnloaded` | `on_plugin_unloaded` |
| `EventOnAgentBegin` | `on_agent_begin` |
| `EventOnAgentDone` | `on_agent_done` |

> 注意：Go SDK 的钩子事件面是 Python 插件（14 个钩子）的超集，跨语言移植插件时注意事件名差异。

## 消息组件（Component）

`Component` 表示消息链中的单个元素，用类型常量构造：

| 类型 | 构造辅助 | 说明 |
|---|---|---|
| `CompPlain` | `Text(text)` | 纯文本 |
| `CompImage` | `ImageURL(url)` / `ImageFile(path)` | 图片（URL 或本地路径） |
| `CompAt` | — | @某人（`TargetID`） |
| `CompAtAll` | — | @所有人 |
| `CompReply` | — | 引用回复（`ID`） |
| `CompRecord` | — | 语音 |
| `CompFile` | — | 文件 |
| `CompVideo` | — | 视频 |
| `CompNode` / `CompNodes` | — | 转发消息节点 |

## 插件配置

插件配置（`plugins/<name>/config.json`）通过 `sdk.Host.GetConfig` 读取、`SetConfig` 写入：

```go
cfg, _ := sdk.Host.GetConfig("echo")
if v, ok := cfg["key"]; ok {
    // 使用配置值
}
```

`Config` 类型提供 `Get` / `GetString` / `GetBool` 便捷访问。配置变更热推送（`OnConfig`）当前是已知缺口（宿主端配置变更暂不主动推送），插件可用 `Host.GetConfig` 主动读取。

## 注册 API

除 `Plugin` 结构体声明式注册外，还提供命令式注册（可在 `init()` / `OnLoad` 中调用，效果等价）：

| 函数 | 说明 |
|---|---|
| `Register(p *Plugin)` | 记录插件配置（幂等；`Serve` / `native.Serve` 内部自动调用，一般无需手动调用） |
| `RegisterCommand(cmd Command)` | 注册命令 |
| `RegisterFilter(f Filter)` | 注册过滤器 |
| `RegisterHook(h Hook)` | 注册钩子 |
| `RegisterTool(t Tool)` | 注册 LLM 函数工具 |
| `RegisterLLMRequestHook(h)` / `RegisterResultHook(h)` / `RegisterMessageHook(h)` / `RegisterAfterMessageSentHook(h)` / `RegisterWaitingLLMRequestHook(h)` / `RegisterLLMResponseHook(h)` / `RegisterToolCallHook(h)` / `RegisterToolRespondHook(h)` / `RegisterPluginErrorHook(h)` / `RegisterAstrbotLoadedHook(h)` / `RegisterPlatformLoadedHook(h)` / `RegisterPluginLoadedHook(h)` / `RegisterPluginUnloadedHook(h)` / `RegisterAgentBeginHook(h)` / `RegisterAgentDoneHook(h)` | 注册各类钩子 |

## 事件结果语义

命令/过滤器/钩子 handler 返回的错误会被宿主捕获并打日志；过滤器返回 `false` 拦截事件；`on_llm_request` 钩子可通过 `ProviderRequest.Stop = true` 中止 LLM 调用。插件 handler 的 panic 会被 SDK 捕获（不会崩溃整个插件进程），转为错误日志上报。

## 开发

本地开发：clone 到 `~/astrbot-go-plugin-sdk`，宿主 go.mod 通过 `replace` 指向本地。提交后宿主切换到 GitHub 版本。

协议：`proto/plugin.proto` 是宿主↔插件 gRPC 契约（PluginService + HostService）。**注意 `proto/plugin.proto` 与 Python SDK 仓库的 `proto/plugin.proto` 必须逐字节一致**（同一契约两端），改动需两边同步。

Go 生成代码分两个包，以便 Native 构建不链接 gRPC：

- `gen/sdkv1/`：protobuf 消息（无 grpc）。
- `gen/sdkv1grpc/`：生成的服务代码（gRPC 客户端/服务端接口）。

重新生成（`protoc-gen-go-grpc` 只能与消息同包生成，故用脚本把服务代码迁到独立包并生成类型别名）：

```sh
buf generate
python3 scripts/split_grpc_pkg.py     # gen/sdkv1/plugin_grpc.pb.go -> gen/sdkv1grpc/
python3 scripts/gen_core_ifaces.py    # 生成 core 的 PluginService/HostService 接口与 gRPC 反向适配
```

## 协议与数据路径（P1）

本 SDK 与宿主之间的事件 / 消息链 / 响应链走 **原生 protobuf data plane**（0 次
Event JSON 编解码）：

```
Core Event → SDKEvent protobuf（固定字段原生 + repeated Component + metadata_json）
Event 组件 → repeated Component（Plain/At/Image/Reply/Record/Video/File/Json…）
响应链   → repeated Component（HandleCommandResponse.chain / HookResponse.chain）
```

- **`event_json` / `chain_json` RPC 字段已移除**（P1，`reserved` 保留旧 field
  number 防复用）；无 legacy 回退、无双写。
- **协议版本协商**：`RegisterRequest`/`RegisterResponse.protocol_version`
  （`P1ProtocolVersion = 2`，见 `protocol.go`）。Host 与 SDK 版本不一致 →
  明确失败并提示升级，不做 silent fallback。
- **动态结构保留 JSON**：`SDKEvent.metadata_json`、`Component.data_json`、
  hook `payload_json`、工具 `args_json`（属扩展点，非 Message Chain）。
- **二进制路径**：媒体组件走 `bytes base64_data`（≤inline 阈值内联）或
  `BinaryPayload → FileReference`（大文件经宿主 Blob store，handle 制，宿主
  TTL/GC，插件不传任意路径）。
- `TextToImage`/`HtmlRender` 响应同时填 `image_base64`（旧）与 `image_bytes`
  （新，优先）——SDK `TextToImageBytes`/`HtmlRenderBytes` 直接取字节，免 base64。
- **Client 事件方法接收 `*sdkv1.SDKEvent`**（Host 用 `CoreEventToSDKEvent`
  直接构造原生事件，不再经 SDK struct + JSON）。

**版本纪律**：行为不兼容的变更（删除字段、字段语义变化）必须 bump
`P1ProtocolVersion` 与 SDK tag；新增字段/RPC 不需要。
