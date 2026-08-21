# AstrBot Go 插件 SDK

为 AstrBot（Go 版）编写插件的 Go SDK。插件以**独立子进程**运行，与宿主通过 gRPC 通信。

SDK 是独立 module `github.com/WaterGodFurina/AstrBot-go-plugin-sdk`（作为依赖从 GitHub 拉取；开发时本地 clone 到 `~/astrbot-go-plugin-sdk`）。

## 快速开始

插件作者只需写一个 `main`，实现命令/过滤器/钩子。插件身份信息（名称/版本/描述/作者/仓库/是否 cgo）统一放在包根目录的 `metadata.json`，`main.go` 只保留代码逻辑：

```go
package main

import (
    sdk "github.com/WaterGodFurina/Astrbot-go-plugin-sdk"
)

func main() {
    sdk.Serve(&sdk.Plugin{
        OnLoad: setup, // 启动钩子，可在里面动态注册
    })
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
| `RegisterCommand(cmd Command)` | 注册命令 |
| `RegisterFilter(f Filter)` | 注册过滤器 |
| `RegisterHook(h Hook)` | 注册钩子 |
| `RegisterTool(t Tool)` | 注册 LLM 函数工具 |
| `RegisterLLMRequestHook(h)` / `RegisterResultHook(h)` / `RegisterMessageHook(h)` / `RegisterAfterMessageSentHook(h)` / `RegisterWaitingLLMRequestHook(h)` / `RegisterLLMResponseHook(h)` / `RegisterToolCallHook(h)` / `RegisterToolRespondHook(h)` / `RegisterPluginErrorHook(h)` / `RegisterAstrbotLoadedHook(h)` / `RegisterPlatformLoadedHook(h)` / `RegisterPluginLoadedHook(h)` / `RegisterPluginUnloadedHook(h)` / `RegisterAgentBeginHook(h)` / `RegisterAgentDoneHook(h)` | 注册各类钩子 |

## 事件结果语义

命令/过滤器/钩子 handler 返回的错误会被宿主捕获并打日志；过滤器返回 `false` 拦截事件；`on_llm_request` 钩子可通过 `ProviderRequest.Stop = true` 中止 LLM 调用。插件 handler 的 panic 会被 SDK 捕获（不会崩溃整个插件进程），转为错误日志上报。

## 开发

本地开发：clone 到 `~/astrbot-go-plugin-sdk`，宿主 go.mod 通过 `replace` 指向本地。提交后宿主切换到 GitHub 版本。

协议：`proto/plugin.proto` 是宿主↔插件 gRPC 契约（PluginService + HostService）。Go 生成代码在 `gen/sdkv1/`（`buf generate` 重新生成）。**注意 `proto/plugin.proto` 与 Python SDK 仓库的 `proto/plugin.proto` 必须逐字节一致**（同一契约两端），改动需两边同步。
