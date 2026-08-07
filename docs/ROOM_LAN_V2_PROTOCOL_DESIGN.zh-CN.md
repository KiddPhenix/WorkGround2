# Room LAN V2 单端口复用设计

## 1. 目标用户价值

- 同一 WorkGround2 Desktop 进程内的多个 Room 共用一个 LAN 监听端口，避免逐 Room 监听造成端口冲突和额外资源占用。
- `room_id` 成为传输路由键；房间名称、描述、标签等展示信息与内部 ID 分离。
- 新建 Room 默认使用 V2；已有 V1 HTTP/JSON、SSE、邀请和持久化状态继续可用。

## 2. 入口与主流程

创建 Room 时，UI 将房间名称和描述放在表单上方，不再要求用户填写 `room_id`。后端以 Session ID 为稳定输入自动生成 `room_<hash>`，保证失败重试和恢复不会创建重复 Room。

V2 LAN 路径统一为：

```text
POST /collab/v2/rooms/{room_id}/join
POST /collab/v2/rooms/{room_id}/heartbeat
POST /collab/v2/rooms/{room_id}/leave
GET  /collab/v2/rooms/{room_id}/snapshot
GET  /collab/v2/rooms/{room_id}/events
GET  /collab/v2/rooms/{room_id}/stream
POST /collab/v2/rooms/{room_id}/commands
...  /collab/v2/rooms/{room_id}/files/*
```

同一 App 维护一个共享 LAN Host：一个 Listener、一个 HTTP Server、一个 Hub、一个 Service/FileStore，以及活动 `room_id` 注册表。请求先校验活动 Room，再由路径中的 `room_id` 进入共享 Service。关闭单个 Room 只注销该 Room；App 关闭时统一停止 Listener。

## 3. 协议兼容

- `NewHandler` 和全部 `/collab/v1/*` 行为保持不变。
- 旧直连邀请 `workground2://host:port/room` 继续走 V1。
- V2 RouteSet 的 LAN route 显式携带 `protocolVersion: 2`；Join 按 route 版本选择 V1/V2 Client。
- 旧持久化状态没有 `protocolVersion` 时按 V1 恢复；新建 Room 默认写入 V2。
- 显式请求 `protocolVersion: 1` 仍可创建独立 V1 Listener，用于兼容和回归验证。

## 4. 核心模型与状态入口

- `collaborationLANHost`：App 级共享 Listener/Server、活动 Room 注册与关闭入口。
- `collaborationAuthority`：V2 Room 引用共享 Service；V1 Room 保留独立 Service。
- `collaborationConnection.protocolVersion`：单一可信的当前 LAN 协议版本，并进入持久化状态和公开 Route。
- Room 注册/注销必须幂等；重复注册同一 Room 和同一 authority 成功，不同 owner 冲突显式失败。

## 5. 数据、配置与 UI

- 不新增用户配置项；首个 V2 Room 的 `listenHost/port` 建立共享 Listener，后续 Room 必须复用实际端口。`port=0` 表示接受当前共享端口或首次自动分配。
- Host 持久化增加 `protocolVersion`；缺失值迁移为 V1。
- 创建表单顶部展示 Room 名称、描述；Host 模式隐藏可编辑 Room ID。加入模式仍从邀请或手工字段读取 Room ID。
- 新建 Host 默认 V2，导出 V2 邀请；V1 Host 仍导出旧邀请。

## 6. 影响模块

- `internal/collab`：V2 HTTP/SSE Handler 与活动 Room 校验。
- `desktop/collab_lan_host.go`：App 级共享 Listener/Service 生命周期。
- `desktop/collab_transport.go`：V1/V2 Host 与 Client 路由。
- `desktop/collab_app.go`、`collab_persist.go`、`collab_invite.go`：版本、自动 ID、恢复和邀请。
- `desktop/frontend/src/collab`：创建表单、类型、邀请与回归测试。

## 7. 风险与验收

- 未知、已注销或路径/Body Room 不一致必须显式失败，不能串房。
- V2 文件票据返回 V2 ProxyPath，直连失败时仍在同一共享端口按 `room_id` 回退。
- 多 Room 并发创建、关闭、重复恢复不得重复监听、关闭其他 Room 或泄漏路由。
- 同一 App 请求不同显式 V2 端口时返回当前端口冲突，不静默开启第二个 Listener。
- V1 Handler、V1 Client、旧邀请、V1 恢复测试必须继续通过。
- 至少覆盖：两个 V2 Room 同端口、Room 数据隔离、关闭一间不影响另一间、V2 邀请自动生成、自动 Room ID 幂等、V1 回归。
