# WorkGround2 Room Relay Server 与跨网络协作协议设计

> 文档状态：首版已实现
>
> 日期：2026-08-04
>
> 范围：Relay Server、Relay 协议、可选多 Relay、Room 广告与发现、文件中转、WorkGround2 Desktop 适配
>
> 关联文档：[多人多 Agent 协作设计](./MULTI_CLIENT_AGENT_COLLABORATION.zh-CN.md)

## 实现状态（2026-08-05）

本文档对应的首版已落地：

- `workground2-relay` 独立命令行程序；无参数默认监听 `:8443`（所有 IP）并使用明文 WS/HTTP，配置 TLS 证书和私钥后使用 WSS/HTTPS；提供 Tunnel、Discovery、JoinRef、健康检查、指标、限流和优雅退出。
- Desktop 保留 LAN，支持创建 Room 时选择 LAN、0..N Relay、`private/unlisted/public` 及广告字段；V1 LAN 邀请继续兼容，V2 邀请携带 Host 身份与多 Route。
- Relay 业务帧使用 X25519 + HKDF + AES-GCM，并用 Ed25519 Host Room Key 验证身份；路由 Header 作为 AEAD AAD。
- Desktop Settings 可管理多个 Relay，并直接配置 `allow_insecure`。非回环 `ws://` 默认拒绝；回环地址用于本机开发时免开风险选项。
- 活跃 Room 查询会并发聚合 Discovery Relay，校验广告签名、去重并换取一次性 JoinRef。
- Relay 文件代理按 48 KiB Segment 传输，继续沿用 4 MiB 业务 Chunk、Hash 校验和 `.wg2part` 断点恢复。

首版 Relay Directory 和 Tunnel Registry 为单机内存状态；多实例共享目录、长期 Capability 撤销表、审核和排行属于后续扩展。

## 1. 摘要

现有 Room 由一台 WorkGround2 Desktop 作为 Host，通过局域网 HTTP/JSON、SSE 和文件源端口向其他成员提供协作服务。该模式保留为默认能力。

跨网络协作增加可选 Relay 路由：Host 和成员都只向 Relay 建立出站 WSS 连接，因而无需公网 IP、路由器端口映射或可入站防火墙规则。

本设计采用以下核心决策：

1. **Host 仍是唯一 Room Authority。** Timeline、成员、事件序号、幂等结果和 Room Token 继续由 Host 本机管理。
2. **Relay 是薄中转。** Relay 路由加密帧、维护在线 Tunnel 和可选 Room 广告，不保存 Room Timeline、文件或 Agent 内容。
3. **Relay 完全可选。** 一个 Room 可以只有 LAN、只有一个 Relay、同时使用 LAN 和多个 Relay，或者在创建后修改路由集合。
4. **多个 Relay 彼此独立。** Relay 之间不复制 Room 数据、不选主、不要求共享数据库；它们只是到同一 Host Authority 的不同路径。
5. **每个成员只有一条活动控制路径。** 初次连接可并发探测多条候选路径，选定成功路径后关闭其他控制连接；故障时再切换。
6. **Room 广告由 Host 显式选择。** 默认 `private`；Host 可以选择 `unlisted` 或 `public`，并对选定 Relay 幂等发布签名广告。
7. **端到端加密覆盖业务载荷。** WSS 保护网络链路，Host 身份固定和应用层会话加密进一步阻止 Relay 读取或篡改 Room 内容。
8. **现有可靠性语义保持不变。** `requestId` 幂等、`afterSequence` 补读、本地 Snapshot、持久 Outbox 和文件分块续传继续作为恢复基础。

## 2. 背景与现状

### 2.1 当前 Room 连接

当前 Host 在 Desktop 内创建：

- `collab.FileStore`：append-only Room journal。
- `collab.Service`：Room 创建、加入、鉴权、快照、事件和命令的唯一入口。
- `collab.Hub`：以“持久化后唤醒”的方式通知 SSE 订阅者。
- HTTP Handler：暴露 `/collab/v1/*`。
- 文件源和 Host 文件代理：用于直连分享者或经 Host 转发分块。

加入者使用：

- HTTP/JSON 执行 `Join`、`Snapshot`、`Events`、`Submit`、`Heartbeat`、`Leave`。
- SSE 接收实时 Room Event。
- `afterSequence` 在断线后补读事件。
- 本地持久化 Snapshot、消费游标、Outbox 和未完成 Agent Run。

Host 自身通过进程内 `serviceCollaborationPeer` 访问权威 Service，避免回环 HTTP 抖动影响本机状态。

### 2.2 当前跨网络障碍

- Host HTTP Listener 通常只有局域网地址，公网成员无法入站连接。
- NAT、运营商网络和防火墙通常阻止主动入站端口。
- 文件分享者的临时 HTTP Origin 同样无法被其他网络直接访问。
- 直接暴露现有 HTTP 端口会引入 TLS、证书、扫描、限流和攻击面问题。
- 多个公网入口如果分别承载 Room 状态，会引入分布式一致性和选主成本。

### 2.3 必须保留的现有语义

- Host 是 Room 公共事实的唯一写入者。
- Client 是本人 Personal Agent 和本地权限策略的唯一写入者。
- 同一 `requestId` 和相同请求体重复提交只产生一次副作用。
- 事件先持久化，再发布通知。
- 事件序号严格递增；发现缺口时重新补读或获取 Snapshot。
- 网络断开不清除 Snapshot，不阻断基于缓存的本地 Agent 工作。
- 离线公共更新进入所属 Room Outbox，连接恢复后安全重放。
- Agent 普通结果不会自动触发其他 Agent。
- Room Token 不提供远端 Agent 控制权。

## 3. 目标与非目标

### 3.1 目标

- 无公网入站能力的 Host 和成员可以跨局域网组成 Room。
- LAN 模式继续独立工作，不要求部署或配置 Relay。
- 用户可以配置多个 Relay，并为每个 Room 选择零个或多个 Relay。
- Host 可以同时接受来自不同 Relay 的成员连接。
- 活动 Relay 失败后，Client 可以切换到其他 Relay 或 LAN。
- Relay 故障、Host 重启、Client 重启和连接乱序均可恢复。
- Relay 无法读取 Timeline、Room Token、Agent 指令和文件内容。
- Relay 可以选择性提供当前活跃 Room 查询。
- Host 在创建 Room 时明确选择是否发布 Room 广告。
- 文件继续支持校验、暂停、失败重试和断点续传。
- Relay Server 保持单二进制、低状态、易自托管。

### 3.2 非目标

- 首版不实现账号系统、组织目录、好友关系或跨组织权限。
- 首版不实现 Relay 之间的状态同步或联邦搜索。
- 首版不实现 Host 自动迁移、选主或多写 Room Authority。
- 首版不保存 Room Timeline、文件、Agent 历史或原始推理到 Relay。
- 首版不提供通用 TCP/HTTP 反向代理。
- 首版不做历史在线、热门排行、推荐算法、审核后台或长期目录数据库。
- 首版不允许 Relay 绕过 Host 的 Room Token、成员会话和命令权限。

## 4. 术语与核心模型

| 术语 | 含义 |
|---|---|
| Room Authority | Host 本机的 `Service + Hub + Store`，Room 公共状态的唯一权威来源 |
| Reachability | 一个 Room 可以被访问的路由集合 |
| Route | 一条 LAN 或 Relay 候选连接路径 |
| Active Route | 当前 Client 正在使用的唯一控制路径 |
| Relay | 接收出站 WSS、校验 Relay Capability 并转发加密帧的服务 |
| Tunnel | Host 在某个 Relay 上绑定的逻辑入口 |
| Relay Binding | Host 与一个 Relay Tunnel 的活动绑定 |
| Peer | 通过 LAN 或 Relay 连接 Room 的成员 Client |
| RouteSet | Host 签名的 LAN 与 Relay 候选路由集合 |
| Capability | Relay 签发的有范围、有期限能力票据 |
| Room Authority Key | 每个 Hosted Room 的稳定 Ed25519 身份密钥 |
| Transport Epoch | Client 每次切换活动路径递增的本地代次 |
| RoomAdvertisement | Host 发布到 Relay Discovery 的签名公开元数据 |
| Directory | Relay 内存中的活跃 Room 广告索引 |
| Visibility | `private`、`unlisted` 或 `public` |
| JoinRef | Discovery 为一次加入流程签发的短期引用 |

### 4.1 可达性模型

~~~text
RoomReachability = {
    LAN:      0..1 LanRoute
    Relays:   0..N RelayRoute
    Active:   0..1 RouteID       // 每个 Client 本地状态
}
~~~

### 4.2 不变量

| 不变量 | 说明 |
|---|---|
| 单 Authority | 所有 Route 最终进入同一 Host `collab.Service` |
| 单活动控制路径 | 每个 Client 同时只通过一条路径提交 Room 命令和消费事件 |
| 多入口同序列 | 不同 Relay 成员看到的事件都来自同一 Room Sequence |
| Relay 无业务写权 | Relay 不能创建 Timeline Item、成员或 Agent Run |
| 路由故障不改业务状态 | Relay 断开只改变 Route 状态，不清空 Snapshot 或 Outbox |
| 密钥不进普通持久化 | Token、Capability 和私钥只进入凭据存储 |
| 广告与连接解耦 | 广告失败不导致 Room 创建或 Tunnel 连接失败 |

## 5. 总体架构

~~~mermaid
flowchart LR
    subgraph H["Host Desktop"]
        A["Room Authority\nService / Hub / Store"]
        L["LAN Endpoint\nHTTP / SSE"]
        RM["Relay Manager"]
        A <--> L
        A <--> RM
    end

    RM <-->|"出站 WSS"| RA["Relay A"]
    RM <-->|"出站 WSS"| RB["Relay B"]

    C1["Member 1"] -->|"LAN"| L
    C2["Member 2"] -->|"出站 WSS"| RA
    C3["Member 3"] -->|"出站 WSS"| RB
~~~

所有成员命令最终由 `Room Authority` 排序、校验和持久化。Relay A 与 Relay B 不交换 Room 数据。

### 5.1 数据归属

| 数据 | 权威位置 | Relay 是否保存 |
|---|---|---:|
| Room、成员、Timeline、Sequence | Host Room Authority | 否 |
| RequestID 幂等结果 | Host Room Authority | 否 |
| Room Token Hash | Host Room Authority | 否 |
| Client Snapshot、Outbox、游标 | 各 Client 本机 | 否 |
| Room Authority 私钥 | Host 凭据存储 | 否 |
| Host/Guest Capability | 各 Client 凭据存储或邀请 Fragment | 仅校验票据，不落业务日志 |
| Tunnel 在线绑定 | Relay 内存 | 是，临时 |
| RoomAdvertisement | Relay 内存 TTL 索引 | 是，临时公开元数据 |
| 文件内容 | 分享者与接收者本机 | 否 |
| Relay 指标 | Relay 监控系统 | 仅连接与流量元数据 |

## 6. Relay Server 功能

### 6.1 必需功能

1. 接受 Host 和 Peer 的 WSS 出站连接。
2. 创建 Tunnel 或恢复已有 Tunnel Capability。
3. 校验 `hostCapability` 并保证同一 Tunnel 只有一个活动 Host Binding。
4. 校验 `guestCapability` 或短期 `joinRef`，将 Peer Attach 转发给 Host。
5. 路由控制帧、RPC 帧、事件通知和文件流帧。
6. 对连接、Tunnel、Peer、并发 Stream、帧大小和带宽实施限额。
7. 提供心跳、空闲回收、背压和结构化错误。
8. 支持优雅关闭并促使 Client 切换路由。
9. 提供健康检查、就绪检查和不含内容的指标。

### 6.2 可选 Discovery 功能

1. 接受 Host 签名的 RoomAdvertisement `upsert/revoke`。
2. 按 TTL 维护当前可达 Room 的内存目录。
3. 列表和搜索 `public` Room。
4. 通过精确 Room Code 查询 `unlisted` Room。
5. 为公开发现流程签发短期 JoinRef。
6. 分页、限流、字段校验和自动过期。

Relay 可以通过配置完全关闭 Discovery。关闭后 Tunnel 中转继续工作。

### 6.3 明确禁止的能力

- 不接收任意目标 URL、IP 或端口，避免成为开放代理和 SSRF 跳板。
- 不解释或修改 E2E 业务 Payload。
- 不代替 Host 签发 `connectionSession`。
- 不持久化 Room 命令、事件或文件分块。
- 不为多个 Host 副本仲裁 Room 写权。

## 7. 配置设计

### 7.1 Desktop 用户级 Relay 配置

Relay 列表属于用户级全局配置。项目 `WorkGround2.toml` 不应覆盖用户的 Relay 接入凭据或桌面网络策略。

~~~toml
[collaboration]
prefer_lan = true
connect_timeout_seconds = 10
route_stable_seconds = 60

[[collaboration.relays]]
id = "official-sg"
name = "Official Singapore"
url = "wss://relay-sg.example.com/relay/v1/connect"
enabled = true
priority = 100
discovery = true
allow_insecure = false
access_token_env = "WG2_RELAY_SG_TOKEN"

[[collaboration.relays]]
id = "company-relay"
name = "Company Relay"
url = "wss://relay.company.example/relay/v1/connect"
enabled = true
priority = 80
discovery = false
allow_insecure = false
access_token_env = "WG2_COMPANY_RELAY_TOKEN"
~~~

字段语义：

| 字段 | 说明 |
|---|---|
| `id` | 用户配置内稳定唯一 ID，持久状态引用它 |
| `name` | UI 展示名 |
| `url` | 必须使用 `wss://`；仅测试环境允许回环 `ws://` |
| `enabled` | 允许 Room 选择该 Relay，不代表所有 Room 自动使用 |
| `priority` | 数值越大越优先 |
| `discovery` | Client 是否向该 Relay 查询/发布广告，仍受 Server Capability 限制 |
| `allow_insecure` | 默认 `false`；仅显式允许受信网络使用明文 `ws://` |
| `access_token_env` | 私有 Relay 的服务访问凭据环境变量，可空 |

### 7.2 Room 级可达性配置

Room 创建时保存用户意图：

~~~json
{
  "lanEnabled": true,
  "listenHost": "0.0.0.0",
  "port": 39170,
  "relayIds": ["official-sg", "company-relay"],
  "preferLan": true,
  "visibility": "public",
  "advertisement": {
    "name": "Profile 联调",
    "description": "前后端 Profile 功能联调",
    "tags": ["frontend", "backend"],
    "capacity": 16
  }
}
~~~

默认值：

- `lanEnabled = true`
- `relayIds = []`
- `visibility = private`
- Relay 列表为空时，现有 LAN 行为保持不变。

### 7.3 Relay Server 配置

~~~toml
[relay]
listen = ":8443"
public_url = "wss://relay.example.com/relay/v1/connect"
master_key_env = "WG2_RELAY_MASTER_KEY"
access_mode = "public"
access_token_env = "WG2_RELAY_ACCESS_TOKEN"
allow_discovery = true
advertisement_ttl_seconds = 120
host_heartbeat_seconds = 30
idle_timeout_seconds = 120
max_tunnels = 10000
max_peers_per_tunnel = 256
max_streams_per_peer = 32
max_frame_bytes = 1048576
metrics_listen = "127.0.0.1:9090"
~~~

`access_mode` 建议支持：

- `public`：允许创建 Tunnel，但执行严格的 IP、Tunnel 和带宽限流。
- `token`：Host 创建/绑定 Tunnel 前必须提供服务访问凭据。

生产环境必须通过 TLS 提供 WSS。Relay Master Key 和 Access Token 不写入 TOML 明文。

## 8. Capability 与 Tunnel 生命周期

### 8.1 Capability 类型

| 类型 | 持有者 | 权限 |
|---|---|---|
| `hostCapability` | Host | 创建或绑定指定 Tunnel、发布广告、管理 Peer Stream Grant |
| `guestCapability` | 邀请接收者 | Attach 指定 Tunnel，不包含 Room 加入权限 |
| `joinRef` | Discovery 加入者 | 在短期内换取一次 Attach，不可长期复用 |

Relay Capability 使用 Relay Master Key 进行 HMAC 签名，Claims 至少包括：

~~~json
{
  "version": 1,
  "relayId": "official-sg",
  "tunnelId": "tun_...",
  "role": "host|guest|join",
  "issuedAt": 1785850000,
  "expiresAt": 1786454800,
  "limits": {
    "maxStreams": 32,
    "maxBytesPerSecond": 8388608
  },
  "nonce": "..."
}
~~~

### 8.2 无数据库恢复

Capability 自包含并由 Relay 签名。Relay 重启后可以重新校验现有 Capability，Host 使用原 TunnelID 恢复绑定，不依赖 Room 数据库。

能力撤销依赖：

- 短有效期和自动续签。
- Host 主动创建新 Tunnel 并停止旧 Tunnel。
- Relay 更换 Master Key 撤销全部旧票据。

需要细粒度长期撤销时，可以后续加入小型撤销表；首版不依赖该能力。

### 8.3 Tunnel 状态

~~~mermaid
stateDiagram-v2
    [*] --> Created
    Created --> Bound: Host bind
    Bound --> Degraded: heartbeat timeout warning
    Degraded --> Bound: heartbeat restored
    Bound --> Offline: Host disconnected
    Degraded --> Offline: timeout
    Offline --> Bound: Host rebind
    Offline --> Expired: capability expired
    Bound --> Revoked: Host revoke
    Revoked --> [*]
    Expired --> [*]
~~~

同一 Relay 内一个 Tunnel 只允许一个活动 Host Binding。第二个 Host 尝试绑定时返回 `host_conflict`，避免单 Relay 内出现双 Authority 路径。

## 9. 端到端安全设计

### 9.1 威胁模型

Relay 可能记录连接 IP、TunnelID、时间、帧大小和流量。业务内容、Room Token、成员会话和文件内容需要对 Relay 保密。

需要防止：

- Relay 中间人伪装 Host。
- Capability 被放入查询参数或普通日志。
- E2E 帧重放、乱序和跨连接复用。
- 一个 Guest 冒用另一个成员的 `connectionSession`。
- 恶意 Client 使用 Relay 访问任意目标。

### 9.2 Room Authority Key

每个 Hosted Room 创建稳定 Ed25519 密钥对：

- 私钥保存到 OS Credential Store。
- 公钥指纹进入邀请和签名 RouteSet。
- Room 生命周期内保持稳定，Host 重启后恢复。
- 每 Room 独立密钥减少不同 Room 之间的可关联性。

### 9.3 Peer 与 Host 握手

1. Peer 通过 Relay Attach Tunnel。
2. Peer 生成临时 X25519 Key Pair 和随机 Client Nonce。
3. Host 生成临时 X25519 Key Pair 和随机 Host Nonce。
4. Host 使用 Room Authority Key 签名完整握手 Transcript。
5. Peer 根据邀请或广告中的 Host 公钥指纹验证签名。
6. 双方通过 X25519 Shared Secret 和 HKDF-SHA256 派生：
   - `clientToHostKey`
   - `hostToClientKey`
   - 两个方向独立的 Nonce Prefix
7. 后续业务 Payload 使用 AES-GCM 加密。

每个方向使用单调 64 位 Frame Counter，与 Nonce Prefix 组合生成唯一 Nonce。连接重建必须重新握手和生成新 Key，Counter 不跨连接复用。

### 9.4 Room Token 与 ConnectionSession

- Room Token 只在 E2E 握手完成后的 `collab.join` Payload 内发送。
- Relay Guest Capability 只允许到达 Host，不能通过 Host 的 Room 鉴权。
- Host `collab.Service.Join` 继续校验 Room Token。
- Host 签发的 `connectionSession` 只在 E2E Payload 内传输。
- Client 按现有规则把 ConnectionSession 保存到 OS Credential Store。

### 9.5 邀请秘密

V2 邀请使用 URL Fragment 承载 Capability 和 Room Token，避免进入 HTTP 请求、反向代理日志和 Referer：

~~~text
workground2://join#v2.<base64url-encoded-invite>
~~~

邀请 Payload 包含：

~~~json
{
  "version": 2,
  "room": "profile-integration",
  "hostKey": "ed25519-public-key-or-fingerprint",
  "routes": [
    {"kind": "lan", "host": "192.168.1.8", "port": 39170},
    {
      "kind": "relay",
      "relayId": "official-sg",
      "url": "wss://relay-sg.example.com/relay/v1/connect",
      "tunnelId": "tun_...",
      "guestCapability": "...",
      "priority": 100
    }
  ],
  "roomToken": "optional-room-token"
}
~~~

V1 `workground2://host:port/room` 邀请继续支持 LAN 兼容。

## 10. Relay Wire Protocol

### 10.1 传输

- Client 与 Relay：WSS。
- 每个 CollaborationRuntime 对活动 Relay 保持一个连接。
- 控制帧和业务帧共享连接并通过 StreamID 多路复用。
- WebSocket Message 使用 Binary Frame。

### 10.2 Binary Envelope

每个 WebSocket Binary Message：

~~~text
+----------------------+----------------------+--------------------+
| HeaderLength uint32  | Header JSON          | Payload bytes      |
+----------------------+----------------------+--------------------+
~~~

`HeaderLength` 使用网络字节序（Big Endian）。

Header 最大 8 KiB：

~~~json
{
  "v": 1,
  "type": "rpc.request",
  "relayRequestId": "rr_...",
  "tunnelId": "tun_...",
  "peerId": "peer_...",
  "streamId": 7,
  "epoch": 3,
  "seq": 42,
  "flags": ["encrypted", "end"]
}
~~~

Relay 只读取 Header 中的路由字段。标记 `encrypted` 的 Payload 是 Host 与 Peer 的 AEAD Ciphertext。

加密帧必须将规范化后的 `v/type/tunnelId/peerId/streamId/epoch/seq/flags` 作为 AEAD Additional Authenticated Data。Relay 可以读取这些路由字段，但修改任何字段都会导致接收方认证失败。

### 10.3 请求 ID 分层

| 字段 | 所属层 | 用途 |
|---|---|---|
| `relayRequestId` | Relay Transport | 关联 Bind、Attach、Grant、广告和 Stream 控制 ACK |
| `requestId` | Collab 业务 | 现有 Room Command 幂等键，位于加密 Payload |

Relay 重试不能生成新的业务 `requestId`。Transport 重连后，Client 使用原业务请求重放 Outbox。

### 10.4 帧类型

| Type | 方向 | Payload 可见性 | 说明 |
|---|---|---|---|
| `relay.hello` | 双向 | Relay 可见 | 协议版本、能力协商、访问认证 |
| `tunnel.create` | Host → Relay | Relay 可见 | 创建 Tunnel 和 Capability |
| `host.bind` | Host → Relay | Relay 可见 | 使用 Host Capability 绑定 Tunnel |
| `host.bound` | Relay → Host | Relay 可见 | 返回绑定结果与 Server Capability |
| `guest.attach` | Peer → Relay | Relay 可见 | 使用 Guest Capability 或 JoinRef Attach |
| `peer.opened` | Relay → Host | Relay可见 | 通知 Host 新 Peer Route |
| `peer.closed` | Relay → Host | Relay 可见 | 通知 Host Peer Route 断开 |
| `e2e.hello` | Peer → Host | 部分可见 | 临时公钥和握手 Nonce，不含 Room Token |
| `e2e.accept` | Host → Peer | 部分可见 | Host 临时公钥、身份公钥和 Transcript Signature |
| `rpc.request` | Peer ↔ Host | 加密 | Join、Snapshot、Events、Submit、Heartbeat、Leave |
| `rpc.response` | Peer ↔ Host | 加密 | 业务响应或 `collab.Error` |
| `event.notify` | Host → Peer | 加密 | 有新 Room Event 的唤醒或事件批次 |
| `route.update` | Host → Peer | 加密且签名 | 当前 RouteSet 和迁移宽限期 |
| `stream.grant` | Host → Relay | Relay 可见最小路由信息 | 授权两个 Peer 建立短期文件 Stream |
| `stream.open` | Peer → Relay/Peer | 路由可见、业务加密 | 打开文件或未来其他流式通道 |
| `stream.data` | Peer ↔ Peer/Host | 加密 | 有窗口控制的数据帧 |
| `stream.window` | 接收方 → 发送方 | 增量值加密 | 增加端到端接收窗口 |
| `stream.end` | 双向 | Relay 可见 | 正常结束 Stream |
| `stream.reset` | 双向 | Relay 可见 | 失败并释放 Stream |
| `advertisement.upsert` | Host → Relay | 公开且 Host 签名 | 创建或更新 Room 广告 |
| `advertisement.revoke` | Host → Relay | 公开且 Host 签名 | 撤销 Room 广告 |
| `ping` / `pong` | 双向 | Relay 可见 | Transport 心跳和 RTT |
| `error` | Relay → Client | Relay 可见 | 结构化 Relay 错误 |

### 10.5 业务 RPC 映射

加密 RPC 方法保持现有 Collab Service 语义：

| Method | 对应现有能力 |
|---|---|
| `collab.join` | `Service.Join` |
| `collab.snapshot` | `Service.Snapshot` |
| `collab.snapshot_manifest` | `Service.SnapshotManifest`，固定 `baseSequence`、块描述与完整性 Hash |
| `collab.snapshot_chunk` | `Service.SnapshotChunk`，独立鉴权、校验和重试的 Snapshot 块 |
| `collab.events` | `Service.Events(afterSequence)` |
| `collab.submit` | `Service.Submit` |
| `collab.heartbeat` | `Service.Heartbeat` |
| `collab.leave` | `Service.Leave` |
| `file.origin.register` | 注册分享者当前可用文件路由 |
| `file.ticket` | Host 校验并签发短期文件传输 Ticket |

Relay Host Bridge 只做 Transport 到 `collab.Service` 的适配，不复制业务验证逻辑。

### 10.6 版本协商

`relay.hello` 携带：

~~~json
{
  "minVersion": 1,
  "maxVersion": 1,
  "capabilities": ["rpc", "event", "file_stream", "discovery_v1"]
}
~~~

双方选择共同最高版本。没有交集时返回 `protocol_mismatch`，Client 可以尝试其他 Relay 或 LAN。

### 10.7 顺序、重复与迟到

- WebSocket 连接内的同一 Stream 使用单调 `seq`。
- Relay 可以在断线边界重复最后一个 Transport Frame，接收方按 `(epoch, streamId, seq)` 去重。
- Client 每次更换活动路径递增 `transportEpoch`。
- 旧 Epoch 的 RPC Response、Event 和 Window Update 全部丢弃。
- Room Event 最终仍按 `RoomEvent.Sequence` 校验。
- 收到 `Sequence > afterSequence + 1` 时停止增量应用，执行 Events 补读；补读失败再获取 Snapshot。

### 10.8 背压

- `stream.data` 必须消耗接收窗口。
- 接收方持久化或处理数据后发送 `stream.window`；Window 增量位于认证加密 Payload 内，Relay 不能扩大接收额度。
- Relay 使用独立的有界发送队列和 WebSocket 写超时控制单跳背压。
- 控制帧队列和数据帧队列分离，文件不能饿死心跳和 Room 命令。
- 达到队列上限时返回 `backpressure`，发送方暂停并可重试。
- Relay 不静默丢弃业务帧。
- 慢 Peer 不阻塞 Host 向其他 Peer 持久化和发布事件。

建议 V1 默认值：

| 限制 | 建议默认值 |
|---|---:|
| Header | 8 KiB |
| 单 WebSocket Message | 1 MiB |
| `stream.data` Segment | 64 KiB |
| 单 Peer 并发 Stream | 32 |
| 初始 Stream Window | 1 MiB |
| 广告 TTL | 120 秒 |
| Relay Ping | 30 秒 |

现有 4 MiB 文件 Chunk 在 Relay 层拆分为多个 64 KiB Segment；业务 Chunk 校验语义不变。

## 11. 连接与恢复流程

### 11.1 Host 创建 Room

~~~mermaid
sequenceDiagram
    participant UI as Desktop UI
    participant H as Host Runtime
    participant A as Room Authority
    participant R1 as Relay A
    participant R2 as Relay B

    UI->>H: 创建 Room + LAN + relayIds + visibility
    H->>A: CreateRoom(requestId)
    A-->>H: Room created/restored
    par 可选 LAN
        H->>H: 启动 LAN Listener
    and Relay A
        H->>R1: tunnel.create / host.bind
    and Relay B
        H->>R2: tunnel.create / host.bind
    end
    H->>H: 生成签名 RouteSet
    opt visibility != private
        H->>R1: advertisement.upsert
        H->>R2: advertisement.upsert
    end
    H-->>UI: Room 已创建 + 各 Route/广告状态
~~~

部分失败语义：

- Authority 创建成功即表示 Room 存在。
- LAN Listener 失败只标记 LAN Route 失败。
- 单个 Relay Bind 失败只标记该 Relay 失败。
- 广告失败只标记 Advertisement 失败。
- 至少一条 Route 成功时 Room 可以连接。
- 没有 Route 成功时 Host 本机仍保留 Authority，状态显示“当前无远端可达路径”。

### 11.2 通过邀请加入

1. 解析 V1 或 V2 邀请。
2. 验证 RouteSet 签名和 Host Key 指纹。
3. 根据 `preferLan`、用户优先级和近期健康度选择候选路径。
4. LAN 与最高优先级 Relay 可以错峰并发探测。
5. 使用同一个稳定 Join Attempt ID，避免多路径竞争产生重复成员事件。
6. 每条 Relay 路径先 Attach，再完成 E2E 握手。
7. 第一条完成 Host 身份校验、Join 和 Snapshot 同步的路径成为 Active Route。
8. 关闭竞争失败路径。
9. 使用 `afterSequence` 启动事件增量同步。

### 11.3 多 Relay 选择策略

建议顺序：

1. 可达 LAN，且配置 `preferLan=true`。
2. 最高用户 Priority 的健康 Relay。
3. 同 Priority Relay 使用 `hash(memberId, relayId)` 稳定分散连接。
4. 最近处于熔断期的 Relay 暂不选择。

切换成功后设置稳定窗口，避免刚恢复的高优先级 Relay 导致频繁回切。

### 11.4 Relay 故障切换

1. 当前路径出现网络失败、心跳超时或 Server Shutdown。
2. 将 Route 标记 `degraded/failed`，保留 Snapshot 和 Outbox。
3. 递增 Transport Epoch 并取消旧路径所有 Pending Transport Request。
4. 选择下一候选 Route。
5. 完成 Host 身份验证。
6. 尝试沿用现有 `connectionSession`。
7. 如果 Session 已失效，使用持久化 ResumeSession 重新 Join。
8. 调用 `Events(afterSequence)`；发现缺口时获取 Snapshot。
9. 顺序重放 Outbox，业务 `requestId` 保持不变。

### 11.5 Relay 重启

- 在线 WSS 会断开。
- Relay 不恢复旧内存 Connection，但可以用 Master Key 重新验证 Capability。
- Host 以同一 TunnelID 重新 Bind。
- Host 自动重新发布 Advertisement。
- Client 根据退避策略重新 Attach。
- Advertisement 在 Host 重连前暂时不可查询，TTL 不产生幽灵 Room。

### 11.6 Host 重启

- Host 回放现有 Room EventStore。
- 恢复 Room Authority Key、Relay Capability Ref 和 Room Reachability 配置。
- 重启 LAN Listener，并对选中 Relay 幂等 Rebind。
- 恢复 RouteSet 和 Advertisement Revision。
- Client 继续使用本地 Snapshot 和 Outbox，Host 恢复后补读和重放。

### 11.7 动态 RouteSet 更新

Host 增删 Relay 后生成更高 Revision 的签名 RouteSet：

~~~json
{
  "room": "profile-integration",
  "revision": 8,
  "routes": [],
  "removeAfter": "2026-08-04T16:00:00Z",
  "signature": "..."
}
~~~

- Client 只接受 Host 签名且 Revision 更高的 RouteSet。
- 删除活动 Relay 前提供迁移宽限期。
- Client 先连接新 Route，再关闭旧 Route。
- 已移除 Route 的迟到数据由 Transport Epoch 拒绝。

## 12. Room 广告与活跃 Room 查询

### 12.1 创建时选择可见性

创建 Room 表单提供：

| Visibility | UI 文案 | 行为 |
|---|---|---|
| `private` | 私有，仅邀请加入 | 不登记广告，默认值 |
| `unlisted` | 不公开列出，可用房间码查找 | 登记精确查询项，不进入列表 |
| `public` | 公开到 Room 列表 | 允许列表、搜索和标签筛选 |

只有选择了至少一个支持 Discovery 的 Relay 时才能选择 `unlisted/public`。LAN-only Room 保持 `private`。

公开时要求 Room Name，可选：

- Description
- Tags
- Capacity
- 是否展示近似在线人数

多 Relay 默认向该 Room 选中的所有 Discovery Relay 发布；高级设置允许逐个关闭广告。

Desktop 的 Room 连接弹窗提供独立的 `Relay Server` 页签，与“加入 Room / 创建 Room”并列。该页签直接读写用户级 Relay 配置，支持增删多个 Relay、启用 Discovery、设置优先级和显式确认非回环 `ws://` 风险；活跃 Room 查询也集中在该页。选择查询结果后，Desktop 回填 RouteSet、Host Key 和 JoinRef，并切回“加入 Room”继续连接。全局 Settings 与该页签使用同一份配置，不维护第二份 Relay 状态。

### 12.2 活跃定义

Relay 上的 Room 被视为当前可达，需要同时满足：

- Tunnel 存在活动 Host Binding。
- Host Relay 心跳未超时。
- Advertisement 未过 TTL。
- Host 未撤销广告。

目录状态表示“Host 当前可达”，不承诺 Room 中存在其他成员。

### 12.3 Advertisement Schema

~~~json
{
  "publicRoomId": "room_public_...",
  "name": "Profile 联调",
  "description": "Profile 前后端联调",
  "tags": ["frontend", "backend"],
  "visibility": "public",
  "requiresToken": true,
  "onlineCount": 3,
  "capacity": 16,
  "hostPublicKey": "ed25519-public-key",
  "hostKeyFingerprint": "sha256:...",
  "advertisementRevision": 12,
  "expiresAt": "2026-08-04T15:32:00Z",
  "signature": "..."
}
~~~

`publicRoomId` 使用 Host Room Public Key 与 Room ID 稳定派生，使多个 Relay 查询结果可以去重。查询方使用 `hostPublicKey` 验证签名，并检查其摘要与 `hostKeyFingerprint` 一致。

广告严禁包含：

- Room Token
- Guest/Host Capability
- LAN Host IP
- 成员名单
- Timeline
- 文件信息
- Agent 指令、状态详情或结果内容

### 12.4 广告幂等与状态

Host 本地 Room 配置是广告意图的单一可信源。

每个 Relay 维护独立发布状态：

~~~text
disabled → pending → published
                  ↘ failed → retrying → published
published → revoking → revoked
~~~

- `advertisementRevision` 单调递增。
- `upsert/revoke` 使用稳定 Relay Request ID。
- 旧 Revision 不能覆盖新广告。
- 单 Relay 发布失败不会影响其他 Relay、Room 连接或 LAN。
- 切换到 `private` 后对全部已登记 Relay 幂等 Revoke。
- Relay 离线时 Revoke 进入待重试队列；TTL 同时保证最终下架。

### 12.5 查询接口

~~~http
GET /relay/v1/rooms?query=profile&tag=backend&cursor=...&limit=20
GET /relay/v1/rooms/{publicRoomId}
POST /relay/v1/rooms/{publicRoomId}/join-ref
~~~

规则：

- `public` 可列表和搜索。
- `unlisted` 只接受高熵精确 Room Code 查询。
- 列表必须分页并限制 `limit`。
- JoinRef 短期有效、一次加入流程使用，并受 IP/Room 限流。
- 获取 JoinRef 不代替 Host Room Token 校验。

### 12.6 多 Relay 查询聚合

Desktop 并发查询所有 `enabled && discovery` 的 Relay：

1. 验证 Advertisement 的 Host Signature。
2. 按 `publicRoomId` 去重。
3. 合并同 Room 的 Relay Routes。
4. 标记每条 Route 的来源 Relay、Priority 和健康度。
5. 展示一个 Room 结果，加入时执行正常候选路径选择。

Relay 之间不执行目录同步。

## 13. 文件中转设计

### 13.1 现有语义

- 文件内容保留在分享者本机。
- Host 只持久化文件 Offer 元数据。
- 4 MiB Chunk 具有独立 Hash，完整文件具有 SHA-256。
- 接收者显式选择保存位置。
- 支持暂停、失败重试、断线和应用重启续传。
- 分享者离线时显示 `waiting_sender`。

### 13.2 路径优先级

1. **LAN Direct**：接收者可以访问分享者 Origin 时继续直连。
2. **Common Relay Direct Stream**：分享者和接收者临时 Attach 到共同 Relay，由 Host 授权 Relay 拼接文件流。
3. **Host Proxy Fallback**：找不到共同 Relay 时，经 Host 的两条活动路径流式代理；该路径可配置禁用或限速。

### 13.3 Host 授权

1. 分享者发布 FileOffer，并通过加密 RPC 注册当前 File Origin Route。
2. 接收者向 Host 请求 FileTicket。
3. Host 校验：Room Session、FileOffer、Owner 在线、未撤销、接收者身份。
4. Host 签发短期 FileTicket，绑定 Room、FileID、OwnerID、ReceiverID 和过期时间。
5. Host 在选定 Relay 创建短期 `stream.grant`，只允许指定两个 Peer 和 FileID。
6. Host 生成一次性 `streamSecret`，分别通过双方已有的 E2E 控制通道发送；Relay 只能看到 GrantID。
7. 分享者验证 FileTicket，双方执行临时 X25519 握手，并用 `streamSecret` 认证握手 Transcript。
8. 文件 Segment 使用新派生的流密钥端到端加密并通过 Relay Stream 传输。

Relay 只知道参与连接、Stream 和字节量，不能读取文件名、Hash 或内容。

### 13.4 不同 Relay 上的成员

如果 Owner 和 Receiver 的活动控制路径位于不同 Relay：

- Host 从 RouteSet 中选择双方都能 Attach 的共同 Relay。
- 文件传输建立独立短期数据连接，不改变双方活动控制路径。
- 如果没有共同 Relay，按策略回退 Host Proxy。

### 13.5 断点续传

- Receiver 继续持久化 Completed Chunk Bitmap。
- 重连后重新获取短期 FileTicket 和 StreamGrant。
- 已完成 Chunk 不重新下载。
- 每个业务 Chunk 在落盘前校验 Chunk Hash。
- 完成后校验完整 SHA-256，再执行原子改名。
- `stream.reset` 只结束当前 Transport Stream，不清除可恢复进度。

## 14. WorkGround2 Desktop 适配

### 14.1 职责拆分

当前 `openHostedRoom` 同时创建 Store、Service、LAN Listener 和本机 Peer。应拆为：

~~~text
RoomAuthority
├── FileStore
├── Service
├── Hub
└── FileAuthorization

ReachabilityManager
├── LANEndpoint        0..1
├── RelayBinding       0..N
├── RouteSet
├── AdvertisementState
└── RouteHealth
~~~

`internal/collab` 继续保持 Transport Agnostic。

### 14.2 建议新增包和文件

~~~text
internal/relayproto/
├── envelope.go        帧头、类型、版本和限制
├── capability.go      Capability Claims 与签名
├── handshake.go       E2E 握手消息
├── advertisement.go   广告和查询模型
└── errors.go          Relay 错误码

internal/relayserver/
├── server.go          WSS、健康检查、生命周期
├── tunnel.go          Tunnel/Peer 内存注册表
├── router.go          帧路由和背压
├── discovery.go       TTL 广告目录
├── limits.go          配额和限流
└── metrics.go         无内容指标

cmd/workground2-relay/
└── main.go            Relay Server 入口

desktop/
├── collab_authority.go       RoomAuthority 生命周期
├── collab_reachability.go    LAN/Relay Route 管理
├── collab_relay.go           Relay Client 和 Peer
├── collab_relay_host.go      Host Bridge
├── collab_relay_crypto.go    Room Key 与 E2E Session
├── collab_relay_discovery.go Room 广告与查询
└── collab_relay_file.go      Relay 文件 Stream
~~~

### 14.3 Transport 接口

现有 `collaborationPeer` 可以继续作为 Room 业务传输接口：

~~~go
type collaborationPeer interface {
    Snapshot(context.Context) (collab.Snapshot, error)
    Events(context.Context, uint64) ([]collab.RoomEvent, error)
    Stream(context.Context, uint64, func(collab.RoomEvent) error) error
    Submit(context.Context, collab.CommandEnvelope) (collab.CommandReceipt, error)
    Heartbeat(context.Context, string) error
    Leave(context.Context, string) error
}
~~~

新增实现：

- `serviceCollaborationPeer`：Host 本机，保持现状。
- `httpCollaborationPeer`：LAN Client，保持兼容。
- `relayCollaborationPeer`：Relay RPC/Event 适配。

新增 Reachability 抽象：

~~~go
type collaborationRoute interface {
    ID() string
    Kind() RouteKind
    Priority() int
    Connect(context.Context) (collaborationPeer, RouteSession, error)
    Probe(context.Context) RouteHealth
}

type reachabilityManager interface {
    StartHost(context.Context, RoomAuthority, ReachabilitySpec) error
    Connect(context.Context, RouteSet, JoinContext) (collaborationPeer, ActiveRoute, error)
    UpdateRoutes(context.Context, RouteSet) error
    States() []RouteState
    Close(context.Context) error
}
~~~

公共接口只要求调用方表达意图，内部完成探测、竞争、切换和恢复。

### 14.4 配置模型

`internal/config.Config` 增加用户级 `Collaboration CollaborationConfig` 字段：

~~~go
type CollaborationConfig struct {
    PreferLAN     bool          `toml:"prefer_lan"`
    ConnectTimeout int          `toml:"connect_timeout_seconds"`
    RouteStable    int          `toml:"route_stable_seconds"`
    Relays         []RelayConfig `toml:"relays"`
}

type RelayConfig struct {
    ID             string `toml:"id"`
    Name           string `toml:"name"`
    URL            string `toml:"url"`
    Enabled        bool   `toml:"enabled"`
    Priority       int    `toml:"priority"`
    Discovery      bool   `toml:"discovery"`
    AllowInsecure  bool   `toml:"allow_insecure"`
    AccessTokenEnv string `toml:"access_token_env"`
}
~~~

该配置在项目 TOML 合并后恢复用户级值，避免项目文件强制 Desktop 使用特定公网 Relay 或凭据。

### 14.5 Wails/Desktop 输入接口

现有 `HostCollaborationRoomInput` 向后兼容地增加：

~~~go
type HostCollaborationRoomInput struct {
    // 现有字段保持
    ListenHost string
    Port       int
    Room       string
    Token      string
    SessionID  string

    LANEnabled   *bool
    RelayIDs     []string
    PreferLAN    *bool
    Visibility   string
    Advertisement *RoomAdvertisementInput
}
~~~

`JoinCollaborationRoomInput` 增加：

~~~go
type JoinCollaborationRoomInput struct {
    // 现有 LAN 字段保持
    Host      string
    Port      int
    Room      string
    Token     string
    SessionID string

    Invite  string
    Routes  []CollaborationRouteInput
    HostKey string
}
~~~

建议新增 Desktop API：

| API | 作用 |
|---|---|
| `GetCollaborationRelayConfig` | 返回不含 Secret 的 Relay 配置 |
| `SaveCollaborationRelayConfig` | 保存、校验并规范化用户级 Relay 配置 |
| `ProbeCollaborationRelay` | 测试 TLS、协议版本、Discovery Capability 和 RTT |
| `ListCollaborationRooms` | 并发查询多个 Relay 并聚合活跃 Room |
| `GetCollaborationRoomAdvertisement` | 获取当前 Room 广告意图和各 Relay 状态 |
| `UpdateCollaborationRoomAdvertisement` | 幂等修改 Visibility 和广告字段 |
| `UpdateCollaborationRoomRoutes` | 增删 LAN/Relay Route 并安全迁移 |

### 14.6 Frontend Transport 接口

~~~ts
interface CollaborationTransport {
  getState(): Promise<CollaborationState>;
  retry(): Promise<CollaborationState>;
  host(input: HostCollaborationRoomInput): Promise<CollaborationState>;
  join(input: JoinCollaborationRoomInput): Promise<CollaborationState>;
  invite(): Promise<CollaborationInvite>;
  leave(): Promise<void>;

  getRelayConfig(): Promise<CollaborationRelayConfig>;
  saveRelayConfig(input: CollaborationRelayConfig): Promise<void>;
  probeRelay(id: string): Promise<RelayProbeResult>;
  listRooms(input: RoomQueryInput): Promise<RoomQueryResult>;
  updateRoutes(input: UpdateRoomRoutesInput): Promise<CollaborationState>;
  updateAdvertisement(input: UpdateRoomAdvertisementInput): Promise<CollaborationState>;
}
~~~

### 14.7 Desktop 状态投影

~~~ts
interface CollaborationRouteState {
  id: string;
  kind: "lan" | "relay";
  relayId?: string;
  status: "disabled" | "connecting" | "connected" | "degraded" | "failed";
  active: boolean;
  priority: number;
  latencyMs?: number;
  lastError?: string;
  retryable?: boolean;
}

interface CollaborationAdvertisementState {
  visibility: "private" | "unlisted" | "public";
  revision: number;
  relays: Array<{
    relayId: string;
    status: "disabled" | "pending" | "published" | "failed" | "revoking";
    lastError?: string;
    retryable?: boolean;
  }>;
}
~~~

Room 总连接状态规则：

- 任一活动 Route 已完成同步：`connected`。
- 当前 Route 失败且存在候选：`reconnecting`。
- 所有 Route 失败：`failed`，但保留缓存和 Outbox。
- 广告失败不改变 Room 总连接状态。

### 14.8 本地持久化

`collaborationPersistedState` 增加：

- Room Reachability Intent。
- RelayID 列表和 RouteSet Revision。
- 上次成功 RouteID，仅作为下次连接偏好。
- Room Authority Key Credential Ref。
- 每个 Relay 的 Host/Guest Capability Credential Ref。
- Advertisement Intent、Revision 和待撤销 Relay。
- Transport Epoch 不持久化；进程启动时重新生成。

不得写入 JSON：

- Room Authority Private Key
- Room Token 明文
- Host/Guest Capability 明文
- ConnectionSession 明文

### 14.9 UI 入口

Room 创建表单：

~~~text
连接方式
☑ 局域网
☑ Official Singapore Relay
☑ Company Relay

Room 可见性
● 私有，仅邀请加入
○ 不公开列出，可用房间码查找
○ 公开到 Room 列表

公开信息
名称 / 简介 / 标签 / 人数上限
~~~

规则：

- 默认 LAN 开、Relay 未选、Visibility Private。
- Public 且 Token 为空时显示明确高风险提示。
- 每条 Route 显示独立状态和“重试”。
- Room 已创建后可以在设置中增删 Relay 和修改 Visibility。
- 网络回包先进入 Runtime 状态；Panel 只订阅状态，不直接承接 Transport 回调。

## 15. 错误模型

### 15.1 Relay 错误码

| Code | Retryable | 含义 |
|---|---:|---|
| `relay_unreachable` | 是 | WSS 连接失败 |
| `relay_auth_failed` | 否/配置修复后可重试 | Relay Access Token 无效 |
| `tunnel_not_found` | 是 | Tunnel 未绑定或已过期 |
| `host_offline` | 是 | Tunnel 存在但 Host 当前未连接 |
| `host_conflict` | 否 | 同 Tunnel 已有活动 Host Binding |
| `protocol_mismatch` | 否 | 无共同协议版本 |
| `host_identity_mismatch` | 否 | Host Key 与邀请/广告不一致 |
| `e2e_handshake_failed` | 是 | 握手损坏、超时或签名失败 |
| `capability_expired` | 是 | Capability 过期，需要 Host 续签或新邀请 |
| `rate_limited` | 是 | 超出请求或带宽配额，携带 RetryAfter |
| `backpressure` | 是 | 队列或接收窗口不足 |
| `route_exhausted` | 是 | 所有候选 Route 当前都失败 |
| `advertisement_rejected` | 视原因 | 广告字段、签名、Revision 或配额错误 |
| `file_owner_offline` | 是 | 文件分享者未连接或未恢复 Origin |
| `stream_not_granted` | 否 | 文件 Stream 未获 Host 授权 |

### 15.2 错误边界

- Transport Error 更新对应 Route。
- Collab Error 继续使用现有 `collab.Error`。
- Advertisement Error 更新对应 Relay 广告状态。
- File Stream Error 更新对应 File Transfer。
- 任一子系统失败不能覆盖其他子系统更具体的状态。
- 所有可重试错误提供 Retryable 和可选 RetryAfter。

## 16. 可靠性与容错

### 16.1 幂等

- Tunnel Create/Bind：稳定 Relay Request ID。
- Advertisement Upsert/Revoke：`publicRoomId + revision + relayId` 稳定请求键。
- Route Update：Revision 单调递增。
- Room Command：继续使用现有业务 RequestID。
- File Transfer：FileID、Chunk Index 和 Hash 决定可重复传输。

### 16.2 乱序与迟到

- Relay Control 用 Revision/Epoch 拒绝旧状态。
- Room Event 用 Sequence 拒绝迟到和发现缺口。
- Advertisement 用 AdvertisementRevision 拒绝旧广告。
- 文件 Chunk 可乱序到达，但只在 Hash 校验后标记完成。

### 16.3 退避和熔断

- 每 Route 独立指数退避并加入抖动。
- 一个 Relay 失败不延迟其他 Relay 探测。
- 连续失败进入短时熔断。
- 成功稳定一段时间后再恢复优先级，避免来回切换。

### 16.4 资源上限

- 所有内存队列有界。
- Tunnel、Peer、Stream、广告和查询均有限额。
- 达到限制显式返回错误。
- Relay 不为离线 Peer 无限缓存业务数据。
- Host/Client 的 Outbox 继续承担可恢复离线写入。

## 17. Relay 部署与运维

### 17.1 部署形态

- `workground2-relay` 独立 Go 二进制。
- 可以直接终止 TLS，也可以监听回环端口并由 Caddy/Nginx 提供 TLS。
- 对公网只暴露 WSS/HTTPS 443。
- 单实例即可满足首版；部署多个独立 Relay 用于路径冗余。
- 首版不需要 Redis、关系数据库或共享文件系统。

### 17.2 命令行与无参数默认行为

Relay Server 是独立命令行程序：

~~~text
workground2-relay
~~~

在没有命令行参数、环境变量或配置文件覆盖时，等价于：

~~~text
workground2-relay serve --listen :8443
~~~

`:8443` 表示监听操作系统支持的全部本机 IPv4/IPv6 网卡。实现需要记录实际成功建立的 Listener；如果平台无法用一个双栈 Listener 覆盖 IPv4 和 IPv6，则分别建立 `0.0.0.0:8443` 与 `[::]:8443`，任一失败都要显式报告。

内置默认值：

| 项目 | 默认值 |
|---|---|
| Command | `serve` |
| Listen | `:8443`，全部本机 IP |
| Access Mode | `public`，启用默认限流 |
| Discovery | 启用 |
| Data Directory | OS 用户配置目录下的 `relay/` |
| Master Key | 首次启动原子生成，权限 `0600`，后续重用 |
| TLS | 未配置证书时为 HTTP/WS，并输出高可见安全警告 |

配置优先级：

~~~text
命令行参数 > 环境变量 > Relay 配置文件 > 内置默认值
~~~

TLS 规则：

- 配置 `--tls-cert` 和 `--tls-key` 时直接提供 HTTPS/WSS。
- 未配置证书时提供 HTTP/WS，适合受信网络、测试或由反向代理终止 TLS。
- Desktop 默认拒绝非回环地址的 `ws://` Relay；用户必须在对应 Relay 配置中显式设置 `allow_insecure = true`。
- 公网部署推荐 WSS；监听全部 IP 只表示网络绑定范围，不表示连接已经加密或端口已经被公网路由。

建议 CLI 参数：

| 参数 | 作用 |
|---|---|
| `serve` | 启动 Relay；省略子命令时默认执行 |
| `--listen` | 监听地址，默认 `:8443` |
| `--config` | Relay 配置文件路径 |
| `--data-dir` | Master Key 等本机状态目录 |
| `--public-url` | 对外公布的 WSS URL |
| `--tls-cert` / `--tls-key` | 直接 TLS 证书和私钥 |
| `--access-mode` | `public` 或 `token` |
| `--allow-discovery` | 是否启用 Room Directory |
| `--metrics-listen` | 指标监听地址 |
| `--version` | 输出版本后退出 |
| `--help` | 输出参数、默认值和安全说明 |

启动日志必须显示实际 Listener、协议 `ws/wss`、Discovery 状态、Access Mode 和数据目录，但不能输出 Master Key 或 Access Token。配置无效、端口全部绑定失败或 Master Key 无法安全加载时，以非零退出码结束，不能静默降级。

### 17.3 HTTP 端点

| Endpoint | 作用 |
|---|---|
| `GET /healthz` | 进程存活 |
| `GET /readyz` | Master Key、Listener 和内存 Registry 就绪 |
| `GET /metrics` | 受保护的 Prometheus 指标 |
| `GET /relay/v1/rooms` | 可选 Discovery 查询 |
| `GET /relay/v1/rooms/{id}` | 可选 Room 精确查询 |
| `POST /relay/v1/rooms/{id}/join-ref` | 可选短期 JoinRef |
| `GET /relay/v1/connect` | WebSocket Upgrade |

### 17.4 指标

建议指标：

- Active Tunnels
- Active Host Bindings
- Active Peers
- Active Streams
- Bind/Attach/Handshake 成功率
- Relay RTT
- Reconnect/Failover 次数
- Frame/Byte 吞吐
- Backpressure/RateLimit 次数
- Advertisement 数量和过期数
- Room Query 延迟
- File Stream 字节和失败数

禁止在指标 Label 中放入 Room Name、Member Name、Token、Capability、文件名或消息文本。

### 17.5 日志

结构化日志可以包含：

- Relay Request ID
- TunnelID 截断摘要
- PeerID
- Frame Type
- Error Code
- Byte Count
- Duration

日志不得包含业务 Payload、完整 Capability、Room Token、ConnectionSession 或解密密钥。

### 17.6 优雅关闭

1. 停止接受新 Tunnel/Peer。
2. 向现有连接发送 `server_shutdown` 和建议 RetryAfter。
3. 给控制帧短暂排空时间。
4. Reset 文件 Stream，接收端保留 Chunk 进度。
5. 关闭连接；Client 自动选择其他 Route。

## 18. 兼容与迁移

### 18.1 LAN 零回归

- V1 Host IP/Port/Room/Token 输入继续工作。
- `httpCollaborationPeer` 和现有 HTTP/SSE Handler 保留。
- 用户不配置 Relay 时不创建 WSS、不生成 Capability、不显示 Discovery 查询。
- Existing persisted state 缺少 Reachability 字段时迁移为 `LAN enabled + private`。

### 18.2 邀请版本

- V1：LAN 地址邀请。
- V2：Host Key、RouteSet、Relay Capability 和可选 Room Token。
- Parser 按版本解码；未知版本显式报错。
- V2 邀请可以同时包含 LAN 与 Relay Routes。

### 18.3 渐进交付

建议阶段：

1. **Authority 解耦**：拆出 RoomAuthority，LAN 行为不变。
2. **Relay 控制面**：Server、Capability、Tunnel、WSS、限流和健康检查。
3. **E2E 与 Room RPC**：Join、Snapshot、Events、Submit、Heartbeat、Leave。
4. **可选多 Relay**：RouteSet、选择、熔断、故障切换和恢复。
5. **Room 广告发现**：Visibility、Advertisement、查询、JoinRef 和多 Relay 聚合。
6. **文件流**：共同 Relay Direct、Host Proxy Fallback、断点续传。
7. **运维加固**：负载、混沌、安全测试和部署文档。

## 19. 测试计划

### 19.1 Relay Protocol 单元测试

- Envelope 编解码和大小限制。
- Capability 签名、篡改、过期和错误 Role。
- Tunnel 单 Host Binding。
- Peer Attach 和关闭清理。
- Stream Window、Reset 和背压。
- Advertisement Revision、TTL 和签名。
- Query 分页和限流。

### 19.2 E2E 安全测试

- Host Signature 正确和错误指纹。
- X25519/HKDF 派生一致。
- AES-GCM 篡改检测。
- Nonce/Counter 重放拒绝。
- 旧 Connection Ciphertext 不能用于新连接。
- Room Token 不出现在 Relay 日志和抓取的明文 Payload。

### 19.3 Desktop 集成测试

- LAN-only 创建和加入。
- Relay-only 创建和加入。
- LAN + Relay 竞争，首个健康路径胜出。
- 多 Relay 不同成员同时加入同 Room。
- 活动 Relay 断开后切换并补读。
- 旧 Route 迟到 Event/RPC 被拒绝。
- Host/Client/Relay 重启恢复。
- Outbox 重放不重复 Timeline 或 Agent Run。
- 配置缺失、Capability 过期和 Host Key 不匹配显式失败。

### 19.4 Discovery 测试

- Private Room 不可列表和查询。
- Unlisted Room 只可精确查询。
- Public Room 可分页搜索。
- 多 Relay 同一 PublicRoomID 去重并合并 Routes。
- 广告 Upsert/Revoke 幂等。
- 单 Relay 广告失败不影响 Room 连接。
- Relay 重启后 Host 自动重新发布。
- Public 且 Token 为空显示安全警告。

### 19.5 文件测试

- LAN Direct 保持现有行为。
- Common Relay Direct Stream。
- 不同活动 Relay 通过共同 Relay 建立文件数据路径。
- Host Proxy Fallback。
- 传输中 Relay 断开后按 Chunk 恢复。
- Chunk Hash、Manifest Hash 和完整 SHA-256 校验。
- Owner 离线/重启后的 `waiting_sender` 和恢复。
- Stream Backpressure 不阻塞 Room 消息。

### 19.6 故障与负载测试

- 慢 Peer、慢 Host 和慢文件接收者。
- 大量并发 Tunnel、Peer 和空闲连接。
- Relay Graceful Shutdown 和异常退出。
- 网络丢包、延迟、短时断连和频繁抖动。
- 限流和队列耗尽时无静默数据丢失。
- 内存占用与连接数、Stream 数保持可预测关系。

## 20. 验收标准

- Host 和成员均无公网入站端口时可以加入同一 Room。
- 不配置 Relay 时，LAN 创建、邀请、消息、Agent 和文件功能保持现有行为。
- Room 可以配置零个、一个或多个 Relay。
- Host 可以同时接收经不同 Relay 连接的成员。
- 一个 Relay 故障后，Client 可以切换其他 Route，Timeline 不重复、不丢失。
- Relay、Host 或 Client 重启后可以恢复 Snapshot、Sequence 和 Outbox。
- Relay 无法读取 Room Token、Timeline、Agent 指令和文件内容。
- Private Room 不出现在任何 Relay 查询结果中。
- Host 创建 Room 时可以选择 Private、Unlisted 或 Public。
- 多 Relay 查询可以按 PublicRoomID 去重并合并 Routes。
- 广告登记失败不影响 Room 创建、LAN 或其他 Relay。
- 文件保持分块校验、暂停、重试、断点续传和发送者离线恢复。
- 所有失败提供结构化 Code、Retryable 和可观察状态。
- Transport、广告、文件和 Room 业务错误不会互相覆盖。

## 21. 风险与未决问题

| 项目 | 当前决策 / 待确认 |
|---|---|
| 官方 Relay 运营方 | 待确认；协议同时支持官方和自托管 Relay |
| 公共 Relay 配额 | 待负载测试后确定默认值 |
| Public Room 审核 | 首版无长期目录和推荐；公开服务上线前需确认滥用处理策略 |
| Tokenless Public Room | 技术上允许，UI 必须强提示；是否在官方 Relay 禁用待产品决定 |
| File Host Proxy | 作为无共同 Relay 的回退，可由 Host/Relay 配置关闭或限速 |
| Capability 有效期 | 建议短期并自动续签，具体默认值待可用性测试 |
| Horizontal Scaling | 首版单 Relay 实例；需要单域多实例时另行设计 Connection Affinity |
| Host 迁移 | 不在本设计范围，Host 仍是单点 Authority |
| 历史目录与推荐 | 后续独立 Directory Service，不进入轻量 Relay |

## 22. 最终边界

Relay 扩展的是 Room 的网络可达性和可选发现能力。Room 的状态模型、Agent 权限、事件顺序、幂等、离线恢复和本地数据所有权继续由现有 Host/Client 边界负责。

该边界允许 WorkGround2 在保留局域网零依赖体验的同时，按用户配置增加一个或多个公网中转路径；Relay 可以单独部署、独立失败和独立恢复，且不会演变成第二套 Room 后端。
