// protocol.go — P1 协议版本集中管理。
//
// P1 把 Event / Component / Response Chain 收敛为原生 protobuf data plane
//（0 次 JSON），并删除 legacy event_json / chain_json RPC 路径。协议版本在
// Register 握手期协商：SDK 与 Host 版本不一致 → 明确失败并提示升级，不做
// Legacy 回退（当前无第三方 Go Plugin 生态需要维护历史 wire format）。
package sdk

// P1ProtocolVersion 是 P1 原生 Event/Component/Chain 协议的版本。
// 行为不兼容的变更必须 bump 此值；新增字段/RPC 不需要。
const P1ProtocolVersion = 2
