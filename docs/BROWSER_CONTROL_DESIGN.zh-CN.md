# WorkGround2 原生浏览器操作能力设计

状态：`implemented`
分支：`developping/browser-control+2026-08-12`
范围：第一版原生 CDP；不使用 MCP；不提供截图、下载、持久登录态或桌面专用 UI；本地文件上传通过专用 `browser_upload` 工具并在 Desktop 设置中可关闭。

实现验证：Chrome 151 真实双门集成已覆盖完整工具闭环、跨域 iframe Target 路由、取消隔离、空闲/Controller 回收、下载拒绝和临时 Profile 清理，并新增可见 Chrome 下 `navigator.webdriver === false`、随机非零回环调试端口 `/json/version` 可达、关闭后端口不可连的验收；默认单测不启动浏览器。

## 1. 目标

让所有通过 `control.Controller` 运行的前端获得一致的浏览器操作能力。Agent 可以：

1. 按当前 WorkGround2 Session 启动独立 Chrome 或兼容 Chromium 浏览器。
2. 导航到 HTTP/HTTPS 页面。
3. 获取适合模型读取的页面文本、标签页和带编号的交互元素。
4. 使用页面 revision 和元素编号点击、输入、滚动。
5. 新建、激活和关闭标签页。
6. 向 input[type=file] 上传本地文件（1-20 个，路径进入 transcript）。
7. 显式关闭浏览器；Controller 关闭后自动回收。空闲超时（默认 0 = 永不）仅在配置正数 `idle_timeout_seconds` 时回收。

该能力学习 browser-use 的两部分：

- 使用 CDP 获取 DOM、布局和 Accessibility 信息，构建紧凑页面状态。
- 使用 CDP Input、DOM 和 Runtime 域执行可靠动作。

WorkGround2 保留自己的 Agent 循环、Tool Registry、权限门、事件流、会话和配置体系。

## 2. 非目标

第一版明确不实现：

- browser-use MCP 或其他浏览器 sidecar。
- 截图、视觉模型输入、坐标视觉定位。
- 拖放、目录上传、下载管理、打印和 PDF；本地文件上传仅通过 `browser_upload`（受 `allow_file_upload` 开关控制）。
- 第一版不启用用户日常 Chrome Profile、Cookie、登录态和密码库自动填充；接口位置会预留，后续无需改动核心 Tool/Session 模型。
- 验证码、反机器人绕过、扩展管理、代理池。
- Desktop 浏览器面板或专用前端状态页。
- 多个 WorkGround2 Session 共用同一浏览器进程。

## 3. 用户入口和主流程

入口是模型可见的内置工具，无新增 UI：

```text
browser_open
  -> browser_state
  -> browser_click / browser_type / browser_scroll / browser_tab / browser_upload
  -> browser_state
  -> browser_close
```

正常流程：

1. `browser_open` 按当前 parent session 幂等创建浏览器，可选导航 URL。
2. `browser_state` 发布一个不可变页面快照，返回 `revision` 和 `[index]` 元素。
3. 写操作必须携带该 `revision`、目标 `index` 和 `request_id`。
4. 写操作完成后重新观察页面，发布新快照并返回新 revision 摘要。
5. 页面在动作前发生变化时，旧 revision 被拒绝，模型重新调用 `browser_state`。

## 4. 总体架构

```text
control.Controller
    |
    v
agent.Agent -> tool.Registry
                   |
                   v
          internal/tool/browser
                   |
                   v
          browser.Service interface
                   |
                   v
          internal/browser.Manager
                   |
          ownerID -> *Session
                   |
                   v
          internal/browser/cdp.Driver
                   |
                   v
              Chromium CDP
```

职责边界：

- `internal/tool/browser` 只处理 JSON Schema、参数校验、权限属性、Session owner 提取和结果编码。
- `internal/browser` 拥有核心模型、revision、幂等、并发、生命周期和恢复语义。
- `internal/browser/cdp` 只处理 Chromium 启动、CDP Target/DOM/AX/Input 细节。
- `internal/boot` 创建 Manager、注册工具、组合 cleanup。
- `internal/config` 提供稳定配置和默认值。

### 4.1 支持的浏览器

第一版支持安装在本机的 Google Chrome，并兼容其他使用 Chrome DevTools Protocol 的 Chromium 系浏览器：

```go
type BrowserKind string

const (
	BrowserAuto             BrowserKind = "auto"
	BrowserChrome           BrowserKind = "chrome"
	BrowserChromium         BrowserKind = "chromium"
	BrowserEdge             BrowserKind = "edge"
	BrowserChromeForTesting BrowserKind = "chrome_for_testing"
)
```

`BrowserAuto` 的默认发现顺序是 Chrome、Edge、Chromium、Chrome for Testing；显式 `executable_path` 始终优先。启动成功后 Driver 必须读取 Browser version/protocol version，不满足最低 CDP 能力时返回显式错误，不能假装成功。

Chrome 本身就是 Chromium 系浏览器，页面感知和动作使用同一套 CDP 域，无需 Chrome 专属 Tool。

### 4.2 调试端口与可检测性

生产启动不使用 chromedp 隐式的 `--remote-debugging-port=0`。每次启动前先监听 `tcp4 127.0.0.1:0` 选取一个当前空闲的非零回环端口，关闭临时 listener 后显式传入：

- `--remote-debugging-address=127.0.0.1`
- `--remote-debugging-port=<nonzero>`

严禁固定 9222、`0.0.0.0`、IPv6 wildcard 或公网地址；不启用 `--enable-automation`，不注入或覆写 `navigator.webdriver`，不引入 stealth/反指纹依赖。chromedp 在显式非零端口下仍从 Chrome stderr 解析 `DevTools listening on ws://...` 并连接，沿用现有 API，不另造 HTTP/CDP client。

端口选择到 Chrome bind 之间存在 TOCTOU：`internal/browser/cdp` 的 Factory 对“端口占用/监听失败”做最多 3 次有界重试，每次使用全新的 Driver/allocator/process 状态，失败实例完整 `Close` 后再试；非端口类启动错误立即返回；最终错误包含尝试次数和原因，允许上层安全重试。Driver 保存实际 `127.0.0.1:<port>` endpoint 仅供生命周期与包内集成测试取证，不进入 `BrowserInfo`/`ToolResult`/日志。

可检测性边界（明确承诺，不做夸大）：

- 去掉 `port=0` 只是移除了一个标准自动化信号：可见（非 headless）Chrome 不再因此暴露 `navigator.webdriver=true`。这不构成“网站不可检测”保证；自动化指纹、插件、请求头、网络特征和页面侧启发式仍然可能识别。
- headless 模式仍会暴露 `navigator.webdriver=true`，与端口无关，不承诺不可检测。
- Chrome 136 起，`--remote-debugging-port`/`--remote-debugging-pipe` 对默认数据目录不再生效，远程调试必须配合非默认 `--user-data-dir`；WorkGround2 总是使用独立临时 Profile，该约束不变（见 §8.6）。

### 4.3 隐身模式（incognito）

`[tools.browser].incognito`（默认 `false`，旧配置缺字段解析为 `false`）是浏览器启动偏好：显式 `true` 时，后续 `browser_open` 新建的 Chrome/Edge/Chromium 进程追加 chromedp `Flag("incognito", true)`（生成 `--incognito` 启动参数），以 Chromium 隐身模式运行，不保留该会话的历史与 Cookie；`false` 时启动参数不得包含 `--incognito`。Desktop 保存该设置会重建浏览器运行时并关闭其管理的现有浏览器进程，下次 `browser_open` 使用新模式。它不复用/共享 Profile、不启用反检测；隐身模式与现有非零回环调试端口、临时 Profile、Cookie/Profile 语义相互独立。

## 5. 目录和文件

```text
internal/browser/
    service.go          # Service、Driver、Factory 接口
    model.go            # 请求、结果、PageState、Element、Tab
    errors.go           # ErrorCode、Error
    manager.go          # ownerID -> Session、启动/关闭/空闲回收
    session.go          # revision、幂等、串行操作、事件失效
    idempotency.go      # 有界 request_id 结果缓存
    profile.go          # ProfileProvider、ProfileLease、默认临时 Profile
    credential.go       # 预留 CredentialProvider；第一版不注册凭据工具
    cdp/
        factory.go      # DriverFactory
        driver.go       # Chromium/Target 生命周期
        observe.go      # DOMSnapshot + AX + 页面文本
        action.go       # navigate/click/type/scroll
        tabs.go         # Target 列表、激活、创建、关闭
        discover.go     # Chromium executable 发现

internal/tool/browser/
    tools.go            # NewTools(service)
    common.go           # owner、JSON 响应、错误转换
    open.go
    navigate.go
    state.go
    click.go
    type.go
    scroll.go
    tab.go
    close.go

internal/config/
    config.go           # ToolsConfig.Browser 与默认值方法

internal/boot/
    boot.go             # Manager 创建、工具注册、cleanup
```

测试与实现文件同包放置，不新建通用规则引擎或浏览器事件总线。

## 6. 会话身份

工具通过现有接口获得 owner：

```go
ownerID := jobs.SessionFromContext(ctx)
```

`control.Controller` 已在工具调用前同时写入 `agent.WithParentSession` 和 `jobs.WithSession`。浏览器工具使用 `internal/jobs` 可以避免 `agent -> tool -> browser tool -> agent` import cycle。

规则：

- ownerID 为空时返回 `missing_session_scope`，禁止回退到全局默认 Session。
- 一个 ownerID 对应一个 Browser Session 和一个独立 Chromium 进程。
- 同一 Browser Session 内可以有多个标签页。
- Work 子任务若获得不同的 jobs session ID，则使用独立 Browser Session。
- `browser_close` 只关闭当前 owner。
- Controller cleanup 调用 `Manager.Close()` 关闭该 Manager 下全部 Session。
- Idle reaper 回收已废弃的子任务 Session。

## 7. 核心接口

工具层只依赖 `browser.Service`：

```go
type Service interface {
	Open(context.Context, string, OpenRequest) (OpenResult, error)
	Navigate(context.Context, string, NavigateRequest) (ActionResult, error)
	State(context.Context, string, StateRequest) (PageState, error)
	Click(context.Context, string, ClickRequest) (ActionResult, error)
	Type(context.Context, string, TypeRequest) (ActionResult, error)
	Scroll(context.Context, string, ScrollRequest) (ActionResult, error)
	Tab(context.Context, string, TabRequest) (ActionResult, error)
	CloseSession(context.Context, string) (CloseResult, error)
	Close() error
}
```

Manager 通过 Factory 创建每个 Session 的 Driver：

```go
type DriverFactory interface {
	New(context.Context, DriverOptions) (Driver, error)
}

type Driver interface {
	Info() BrowserInfo
	Navigate(context.Context, string) error
	Observe(context.Context, ObserveOptions) (Observation, error)
	Click(context.Context, NodeRef) error
	Type(context.Context, NodeRef, TypeInput) error
	Scroll(context.Context, ScrollInput) error
	NewTab(context.Context, string) (string, error)
	ActivateTab(context.Context, string) error
	CloseTab(context.Context, string) error
	Invalidations() <-chan Invalidation
	Close() error
}
```

约束：

- Driver 的生命周期 context 来自 Manager，不来自单次工具调用。
- 单次操作继续接受调用 context，以支持用户取消和超时。
- `Driver.Close` 必须幂等。
- Fake Driver 可以完整测试核心状态机，不启动真实浏览器。

## 8. 核心数据结构

### 8.1 页面状态

```go
type PageState struct {
	SessionID  string         `json:"session_id"`
	Revision   uint64         `json:"revision"`
	URL        string         `json:"url"`
	Title      string         `json:"title"`
	ActiveTab  string         `json:"active_tab"`
	Tabs       []TabInfo      `json:"tabs"`
	Text       string         `json:"text,omitempty"`
	Elements   []Element      `json:"elements"`
	Warnings   []StateWarning `json:"warnings,omitempty"`
	Truncated  bool           `json:"truncated"`
	CapturedAt time.Time      `json:"captured_at"`
}

type StateWarning struct {
	Code    string `json:"code"`
	FrameID string `json:"frame_id,omitempty"`
	Message string `json:"message"`
}

type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

type Element struct {
	Index       int     `json:"index"`
	Role        string  `json:"role,omitempty"`
	Tag         string  `json:"tag,omitempty"`
	InputType   string  `json:"input_type,omitempty"`
	Name        string  `json:"name,omitempty"`
	Placeholder string  `json:"placeholder,omitempty"`
	Href        string  `json:"href,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
	Checked     *bool   `json:"checked,omitempty"`
	Editable    bool    `json:"editable,omitempty"`
	Bounds      Rect    `json:"bounds"`
}

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
```

`Element` 不暴露内部 Node ID。Session 保存不可变 Snapshot：

```go
type Snapshot struct {
	State       PageState
	Nodes       map[int]NodeRef
	Fingerprint string
	Generation  uint64
}

type NodeRef struct {
	TargetID      string
	FrameID       string
	BackendNodeID int64
	Bounds        Rect
}
```

### 8.2 Driver 观察结果

```go
type Observation struct {
	URL         string
	Title       string
	ActiveTab   string
	Tabs        []TabInfo
	Text        string
	Nodes       []ObservedNode
	Warnings    []StateWarning
	Fingerprint string
	Truncated   bool
}

type ObservedNode struct {
	Ref         NodeRef
	Role        string
	Tag         string
	InputType   string
	Name        string
	Placeholder string
	Href        string
	Disabled    bool
	Checked     *bool
	Editable    bool
}
```

Driver 按 DOM/布局顺序返回 Nodes；Session 从 1 开始分配连续 Index。

### 8.3 请求

```go
type OpenRequest struct {
	URL       string
	RequestID string
}

type NavigateRequest struct {
	URL       string
	RequestID string
}

type StateRequest struct {
	Refresh  bool
	MaxChars int
}

type ClickRequest struct {
	Revision  uint64
	Index     int
	RequestID string
}

type TypeRequest struct {
	Revision   uint64
	Index      int
	Text       string
	Clear      bool
	PressEnter bool
	RequestID  string
}

type ScrollRequest struct {
	Revision  uint64
	Index     int
	DeltaY    int
	RequestID string
}

type TabAction string

const (
	TabNew      TabAction = "new"
	TabActivate TabAction = "activate"
	TabClose    TabAction = "close"
)

type TabRequest struct {
	Revision  uint64
	Action    TabAction
	TabID     string
	URL       string
	RequestID string
}
```

说明：

- 除天然幂等的 `browser_close` 外，所有写操作必须提供非空 `request_id`。
- `index=0` 在 Scroll 中表示滚动当前视口；正数表示先滚动目标元素。
- `delta_y` 限制在 `[-4000, 4000]`，不能为 0。
- Type.Text 第一版进入工具参数和 transcript，不用于密码或 Token。
- `browser_type` 在 Session 和生产 Driver 两层：`input_type=file` 始终拒绝并返回 `sensitive_input_blocked`（必须使用 `browser_upload`）；`input_type=password` 仅在 `allow_password_input=false` 时拒绝，开启时真实输入。任何拒绝路径都不能向页面派发输入动作。
- Tab close 拒绝关闭最后一个标签页，关闭整个浏览器使用 `browser_close`。

### 8.4 结果

```go
type OpenResult struct {
	SessionID string      `json:"session_id"`
	Created   bool        `json:"created"`
	Revision  uint64      `json:"revision"`
	URL       string      `json:"url"`
	Title     string      `json:"title"`
	Browser   BrowserInfo `json:"browser"`
}

type ActionResult struct {
	SessionID      string `json:"session_id"`
	RequestID      string `json:"request_id"`
	BeforeRevision uint64 `json:"before_revision"`
	AfterRevision  uint64 `json:"after_revision"`
	Changed        bool   `json:"changed"`
	Method         string `json:"method,omitempty"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	Next           string `json:"next"`
}

type CloseResult struct {
	SessionID string `json:"session_id"`
	Closed    bool   `json:"closed"`
}
```

成功动作完成后必须执行一次有界 Observe，发布新 Snapshot。`AfterRevision` 对应该 Snapshot；随后 `browser_state(refresh=false)` 返回同一 revision 和元素表。

工具层统一使用信封，不直接裸返回上述类型：

```go
type ToolResponse[T any] struct {
	OK     bool       `json:"ok"`
	Result *T         `json:"result,omitempty"`
	Error  *ErrorInfo `json:"error,omitempty"`
}

type ErrorInfo struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Recoverable  bool      `json:"recoverable"`
	OutcomeKnown bool      `json:"outcome_known"`
	Next         string    `json:"next,omitempty"`
}
```

成功只能设置 `Result`，失败只能设置 `Error`；禁止 `OK=true` 但携带 Error。时间统一编码为 RFC3339Nano UTC。

### 8.5 Runtime 和 Driver 选项

```go
type Options struct {
	Factory        DriverFactory
	Profiles       ProfileProvider
	Credentials    CredentialProvider
	BrowserKind    BrowserKind
	ExecutablePath string
	Headless        bool
	ProfileRoot     string
	IdleTimeout     time.Duration
	ActionTimeout   time.Duration
	StateTimeout    time.Duration
	SettleWindow    time.Duration
	MaxTextChars    int
	MaxElements     int
	AllowPasswordInput bool // browser_type 密码输入开关
	AllowFileUpload    bool // browser_upload 本地文件上传开关
	Incognito           bool // true 时新建进程以 Chromium 隐身模式启动（追加 --incognito）
}

type DriverOptions struct {
	BrowserKind    BrowserKind
	ExecutablePath string
	Headless        bool
	UserDataDir     string
	ProfileName     string
	DebugURL        string
	OwnProcess      bool
	DenyDownloads   bool
	SettleWindow    time.Duration
	AllowPasswordInput bool // 与 Options 一致，双保险拒绝
	AllowFileUpload    bool // 与 Options 一致，双保险拒绝
	Incognito           bool // 仅 true 时启动参数含 --incognito
}

type ObserveOptions struct {
	MaxTextChars int
	MaxElements  int
}

type TypeInput struct {
	Text       string
	Clear      bool
	PressEnter bool
}

type ScrollInput struct {
	Node   *NodeRef
	DeltaY int
}

type InvalidationKind string

const (
	InvalidationDocument InvalidationKind = "document"
	InvalidationFrame    InvalidationKind = "frame"
	InvalidationTarget   InvalidationKind = "target"
	InvalidationClosed   InvalidationKind = "closed"
)

type Invalidation struct {
	Kind     InvalidationKind
	TargetID string
	FrameID  string
	At       time.Time
}

type BrowserInfo struct {
	Kind            BrowserKind `json:"kind"`
	Product         string      `json:"product"`
	Version         string      `json:"version"`
	ProtocolVersion string      `json:"protocol_version"`
	ExecutablePath  string      `json:"executable_path,omitempty"`
}
```

构造器固定为：

```go
func NewManager(ctx context.Context, opts Options) (*Manager, error)
```

`NewManager` 只校验配置、建立自己拥有的 Profile root 和（当 `IdleTimeout > 0` 时）启动 idle reaper，不探测或启动浏览器。`Factory` 必填，nil 返回配置错误；`IdleTimeout` 为 0 表示禁用空闲回收（浏览器只由显式 Close/CloseSession 与生命周期 cleanup 关闭），为负数返回配置错误。生产环境由 `internal/boot` 注入 `cdp.NewFactory(...)`，测试注入 Fake Factory；`internal/browser` 不得反向 import `internal/browser/cdp`，避免 Go import cycle。

### 8.6 Profile 扩展接口

Profile 策略在 Driver 启动之前决定，因此抽象位于 `internal/browser/profile.go`，不进入 Tool 参数，也不污染 PageState：

```go
type ProfileMode string

const (
	ProfileEphemeral ProfileMode = "ephemeral"
	ProfileManaged   ProfileMode = "managed"
	ProfileAttach    ProfileMode = "attach"
)

type ProfileRequest struct {
	OwnerID   string
	Kind      BrowserKind
	Headless  bool
	Workspace string
}

type ProfileLease struct {
	ID          string
	Mode        ProfileMode
	UserDataDir string
	ProfileName string
	DebugURL    string
	OwnProcess  bool
	Persistent  bool
}

type ProfileProvider interface {
	Acquire(context.Context, ProfileRequest) (ProfileLease, error)
	Release(context.Context, ProfileLease) error
}
```

模式语义：

- `ephemeral`：第一版实现和默认模式。WorkGround2 创建临时 `user-data-dir`，关闭后安全删除。
- `managed`：预留。使用 WorkGround2 自己拥有的持久 Profile；用户可在可见 Chrome 中登录一次，后续安全复用 Cookie 和登录态。
- `attach`：预留。连接用户已经显式开启远程调试并授权的 Chrome，不启动、不关闭其进程；可继承该浏览器的登录态和 Cookie。

第一版实现 `EphemeralProfileProvider`。`Options.Profiles=nil` 等价于默认实现。接口和测试 Fake 同时落地；`managed`、`attach` 不进入第一版配置和工具 Schema，避免出现看似可用但实际退化的开关。

不设计“直接启动并占用日常 Chrome 默认 User Data 目录”的模式：

- 正在运行的 Chrome 会持有 Profile 锁，第二个进程可能启动失败或损坏状态。
- Chrome 136 起，`--remote-debugging-port`/`--remote-debugging-pipe` 对默认数据目录不再生效，远程调试必须配合非默认 `--user-data-dir`。
- Profile 复制涉及 Cookie/密码的加密密钥、迟到写入和文件一致性，不能作为可靠恢复机制。

参考：[Chrome 远程调试安全变更](https://developer.chrome.com/blog/remote-debugging-port)。

### 8.7 凭据自动填充扩展接口

未来的密码库接入不能让模型把明文密码写进 `browser_type.text`。接口放在 `internal/browser/credential.go`：

```go
type CredentialRequest struct {
	OwnerID      string
	Origin       string
	Reference    string
	UsernameHint string
	Reason       string
}

type CredentialLease interface {
	Username() string
	WithSecret(func([]byte) error) error
	Close() error
}

type CredentialProvider interface {
	Acquire(context.Context, CredentialRequest) (CredentialLease, error)
}

type SecureTyper interface {
	TypeSecret(context.Context, NodeRef, []byte) error
}
```

约束：

- 第一版只定义接口和 nil/disabled 语义，不实现凭据工具、不调用 Provider。
- 后续新增独立 `browser_fill_credential`，参数只携带 `credential_ref`、revision 和元素 index，不携带明文。
- `CredentialLease.WithSecret` 将秘密限制在回调生命周期内；实现必须在回调后清零缓冲区。
- 生产 Driver 通过可选 `SecureTyper` 执行，不把秘密转成普通日志字符串。
- ToolResult、progress、slog、历史和错误不得包含 secret。
- 凭据读取必须经过现有 permission gate 和用户明确授权；Provider 失败显式暴露且允许安全重试。
- 使用 `managed` 或受控 `attach` Profile 时，也允许由 Chrome 自己的密码管理器在浏览器界面中完成填充；WorkGround2 不读取浏览器密码库明文。

## 9. Session 状态和并发

```go
type Session struct {
	id      string
	driver  Driver

	opMu    sync.Mutex
	mu      sync.RWMutex
	state   SessionState
	revision uint64
	generation uint64
	snapshot *Snapshot
	requests *RequestCache
	lastUsed time.Time
}
```

状态：

```go
type SessionState string

const (
	SessionStarting SessionState = "starting"
	SessionReady    SessionState = "ready"
	SessionBroken   SessionState = "broken"
	SessionClosing  SessionState = "closing"
	SessionClosed   SessionState = "closed"
)
```

并发规则：

- Manager mutex 只保护 Session map，不包围 CDP 调用。
- 每个 Session 的 `opMu` 串行化 Observe 和全部写操作。
- 不同 ownerID 可以并行。
- Driver 的 invalidation watcher 只增加 generation、令 snapshot 失效，不执行长操作。
- Snapshot 发布后不可修改；读者拿副本。
- 同一 owner 的重复 Open 等待正在启动的结果，不能启动第二个进程。

## 10. Revision 规则

Revision 是页面元素映射的版本，不是普通调用次数。

1. 第一次 Observe 成功发布 revision 1。
2. CDP 收到 frame navigation、document updated、target changed/closed 等事件后增加 generation，并立即令 Snapshot 无效。
3. Observe 获取开始时记录 generation；捕获结束时若 generation 已变化，最多重试两次。
4. 新 Observation 与当前 Snapshot fingerprint 相同且期间未失效时，保留 revision。
5. fingerprint 不同或 Snapshot 已失效时，revision 单调加一。
6. 写操作执行前同时校验 `request.revision == snapshot.revision` 和 Snapshot 有效性。
7. 写操作成功后 Observe 并发布新 Snapshot；即使 fingerprint 偶然相同，写操作后也至少增加一次 revision。

旧 revision 返回 `stale_state`，不得自动使用同一个 Index 重试。

## 11. 幂等和结果不确定

Session 保存最近 256 个写请求：

```go
type RequestRecord struct {
	ID        string
	Signature string
	State     RequestState
	Result    ActionResult
	ErrCode   ErrorCode
}
```

规则：

- Signature 是工具名和规范化参数的 SHA-256，不包含 request_id。
- 同一 request_id、同一 Signature、已有成功结果：直接返回缓存结果。
- 同一 request_id、不同 Signature：返回 `request_id_conflict`。
- 只允许在确定尚未向浏览器派发动作时自动重试。
- Click/Type 已派发，但等待或重新观察失败：返回 `outcome_unknown`，令 Snapshot 失效，要求调用 `browser_state` 对账。
- `outcome_unknown` 不自动重复动作。
- `browser_close` 天然幂等；不存在 Session 时返回 `closed=false` 成功。

## 12. CDP 页面感知

Driver Observe 使用：

- `Page.getFrameTree`
- `DOMSnapshot.captureSnapshot`
- `DOM.getDocument`，`pierce=true`
- `Accessibility.getFullAXTree`
- `Target.getTargets`

合并原则：

1. 以 `backendNodeId` 关联 DOMSnapshot、DOM 和 AX 节点。
2. 保留可见且有有效 Bounds 的交互元素。
3. 交互候选包括 button、link、input、textarea、select、contenteditable、有效 tabindex 和具有交互 AX role 的节点。
4. 过滤 display:none、visibility:hidden、零面积、disabled 和明显位于视口外且不可滚动定位的节点。
5. 嵌套重复节点优先保留语义更完整、可命中的节点。
6. 页面 Text 从可见文本生成，去除连续空白和重复节点文本。
7. password 输入值永不进入 Observation；其他输入当前 value 第一版也不输出。
8. input 的 type 可以进入 Observation，用于在 Session 和 Driver 两层校验 password/file 输入（file 拒绝，password 受开关控制）；不得输出 value。
9. 其余属性只允许 role、tag、aria/name、placeholder、href、disabled、checked、editable。
9. URL、元素数和文本严格受配置上限约束。

跨域 iframe：第一版必须识别已附加 Target，并把 TargetID/FrameID 写入 NodeRef；无法附加的 frame 显式标为观察不完整，不允许把错误节点映射到主文档。

## 13. CDP 动作

### Click

1. 用 BackendNodeID 重新解析节点。
2. `scrollIntoViewIfNeeded`。
3. 获取最新 Box Model。
4. 计算中心点并做命中检查。
5. 依次发送 mouseMoved、mousePressed、mouseReleased。
6. CDP Input 明确失败时，可使用一次受控的 DOM `click()` fallback，并在结果中记录动作路径供日志观察。

### Type

1. 校验元素 Editable。
2. 聚焦节点。
3. `clear=true` 时使用全选和 Backspace 清空。
4. 普通文本优先 `Input.insertText`，特殊按键使用 `dispatchKeyEvent`。
5. `press_enter=true` 时最后发送 Enter。

### Scroll

- index 为 0 时滚动当前 viewport。
- index 大于 0 时先解析并滚动到目标元素，再应用 delta。
- 使用 CDP Input wheel event；执行后重新观察。

### 页面稳定

- 每个动作等待 DOM/CDP invalidation 进入短暂 quiet window。
- quiet window 默认 300ms，总等待受 settle timeout 限制。
- 不等待永不结束的 network idle。
- context 取消必须尽快终止等待，但不能连带杀死长期 Browser Session。

## 14. 工具定义

| 工具 | 关键参数 | ReadOnly | PlanModeSafe |
|---|---|---:|---:|
| `browser_open` | `url?`, `request_id` | false | false |
| `browser_navigate` | `url`, `request_id` | false | false |
| `browser_state` | `refresh?`, `max_chars?` | true | true |
| `browser_click` | `revision`, `index`, `request_id` | false | false |
| `browser_type` | `revision`, `index`, `text`, `clear?`, `press_enter?`, `request_id` | false | false |
| `browser_scroll` | `revision`, `index?`, `delta_y`, `request_id` | false | false |
| `browser_tab` | `revision`, `action`, `tab_id?`, `url?`, `request_id` | false | false |
| `browser_upload` | `revision`, `index`, `files`, `request_id` | false | false |
| `browser_close` | 无 | false | false |

工具全部返回 pretty JSON。运行错误返回结构化 JSON 作为 result，同时返回非 nil Go error，使 ToolResult 明确显示失败。

`browser_state` 实现适合其 JSON 结构的 `SnipHinter`；其他工具显式采用副作用工具的短结果策略。Schema 在注册时固定，不能根据浏览器状态动态变化，保证 prompt prefix 稳定。

### 14.1 Schema 精确约束

公共约束：

- 每个 Schema 根节点都是 `type=object` 且 `additionalProperties=false`。
- `request_id` 长度 1..128；描述要求同一次安全重试复用原值，新意图使用新值。
- URL 最大 8192 字符，工具层和 Service 层都必须再次验证。
- `revision` 最小 1；`index` 最小 1，只有 Scroll 的 index 允许 0。

各工具字段：

```text
browser_open
  required: request_id
  url: string, optional, default about:blank

browser_navigate
  required: url, request_id

browser_state
  required: none
  refresh: boolean, default true
  max_chars: integer, 1000..60000, omitted uses config;
             request value cannot exceed configured MaxTextChars

browser_click
  required: revision, index, request_id

browser_type
  required: revision, index, text, request_id
  text: string, 0..20000; empty is allowed only when clear=true or press_enter=true
  clear: boolean, default false
  press_enter: boolean, default false

browser_scroll
  required: revision, delta_y, request_id
  index: integer, 0..2147483647, default 0
  delta_y: integer, -4000..4000, value 0 forbidden

browser_tab
  required: revision, action, request_id
  action: enum(new, activate, close)
  new: url optional, default about:blank; tab_id forbidden
  activate/close: tab_id required; url forbidden

browser_close
  required: none
```

JSON Schema 应使用 `oneOf` 表达 `browser_tab` 的条件字段；Go 参数校验仍重复检查，不能只信模型满足 Schema。

## 15. 错误协议

```go
type ErrorCode string

type Error struct {
	Code        ErrorCode `json:"code"`
	Message     string    `json:"message"`
	Recoverable bool      `json:"recoverable"`
	OutcomeKnown bool     `json:"outcome_known"`
	Next        string    `json:"next,omitempty"`
	Cause       error     `json:"-"`
}
```

固定错误码：

| Code | Recoverable | OutcomeKnown | Next |
|---|---:|---:|---|
| `missing_session_scope` | false | true | host wiring |
| `browser_not_open` | true | true | `browser_open` |
| `browser_launch_failed` | true | true | 检查 executable/config 后重试 |
| `unsupported_browser` | true | true | 修改 kind/executable_path |
| `profile_unavailable` | true | true | 修正 Profile 配置或授权 |
| `credential_provider_disabled` | false | true | 配置凭据 Provider |
| `browser_disconnected` | true | true | `browser_open` |
| `invalid_url` | true | true | 修正 URL |
| `unsupported_scheme` | true | true | 使用 HTTP/HTTPS |
| `navigation_timeout` | true | false | `browser_state` |
| `state_timeout` | true | true | `browser_state` |
| `stale_state` | true | true | `browser_state` |
| `element_not_found` | true | true | `browser_state` |
| `element_not_interactable` | true | true | 选择其他元素 |
| `sensitive_input_blocked` | false | true | 使用未来的专用凭据/上传工具 |
| `target_closed` | true | true | `browser_state` |
| `last_tab` | true | true | `browser_close` 或保留标签页 |
| `request_id_conflict` | true | true | 使用新 request_id |
| `outcome_unknown` | true | false | `browser_state`，禁止盲重试 |

错误不得只 Log 后返回空结果。

## 16. 配置

配置放在 `ToolsConfig` 下：

```go
type ToolsConfig struct {
	// existing fields...
	Browser BrowserConfig `toml:"browser"`
}

type BrowserConfig struct {
	Enabled            *bool  `toml:"enabled"`
	Kind               string `toml:"kind"`
	ExecutablePath     string `toml:"executable_path"`
	Headless            *bool  `toml:"headless"`
	IdleTimeoutSeconds  *int   `toml:"idle_timeout_seconds"`
	ActionTimeoutSeconds *int  `toml:"action_timeout_seconds"`
	StateTimeoutSeconds *int   `toml:"state_timeout_seconds"`
	SettleMilliseconds  *int   `toml:"settle_milliseconds"`
	MaxTextChars        *int   `toml:"max_text_chars"`
	MaxElements         *int   `toml:"max_elements"`
	AllowPasswordInput  *bool  `toml:"allow_password_input"` // omitted -> true
	AllowFileUpload     *bool  `toml:"allow_file_upload"`    // omitted -> true
	Incognito           *bool  `toml:"incognito"`            // omitted -> false
}
```

默认值：

```toml
[tools.browser]
enabled = true
kind = "auto"
headless = false
idle_timeout_seconds = 0
action_timeout_seconds = 30
state_timeout_seconds = 15
settle_milliseconds = 300
max_text_chars = 20000
max_elements = 400
allow_password_input = true
allow_file_upload = true
incognito = false
```

约束：

- omitted `enabled` 表示启用；Chrome/Chromium 只在首次 `browser_open` 时启动。
- omitted `headless` 表示 false，方便用户观察；无图形环境显式配置 true。
- omitted `incognito` 表示 false；显式 `true` 时，新建的浏览器进程以 Chromium 隐身模式（incognito）启动（追加 `--incognito` 启动参数），不保留该会话的历史与 Cookie。它是启动配置而非权限；Desktop 保存时会重建浏览器运行时，下次 `browser_open` 使用新模式。
- `idle_timeout_seconds` 默认 0：合法特殊值，禁用空闲 reaper，浏览器永不因空闲自动关闭；正数范围 30..86400，`1..29` 与负数夹到 30，超过 86400 夹到 86400，非法值产生 config warning；负数不会解释为禁用。
- 数值设置必须有最小/最大夹取，非法值回退默认值并保持可诊断。
- `kind=chrome` 时只发现 Google Chrome；`kind=auto` 按 BrowserAuto 顺序发现。
- `executable_path` 为空时按 kind 的平台安装路径和 PATH 发现。
- 每个 Session 使用 Manager 创建的临时 Profile；只删除 Manager 明确创建并记录的目录。
- `managed`/`attach` 配置字段暂不暴露；后续由 ProfileProvider 扩展，不改变 Browser Service 和 Tool Schema。

## 17. 安全和权限

- 只有 `browser_state` 是只读工具；所有启动、导航和动作走现有 permission gate。
- URL 只允许绝对 `http`/`https`，禁止 URL 内嵌用户名密码。
- 内部初始页可以使用 `about:blank`，模型输入不能导航到 `file:`、`data:`、`javascript:`、`chrome:`。
- 第一版允许 localhost，支持本地 Web 测试。
- 下载行为通过 CDP 设置为 deny。
- 不读取或复用用户真实 Profile。
- 页面状态不输出 password value、Cookie、localStorage 或完整 HTML。
- 工具错误和 progress 不包含输入文本、页面敏感字段或 CDP 原始 payload。
- `browser_type.text` 和 `browser_upload.files` 路径都会出现在现有 ToolCall transcript；文档和 Schema 必须明确提醒不要传秘密、不要上传机密文件。
- 第一版存在 CredentialProvider 接口也不能视为已启用密码库；禁止静默回退到普通 Type。

## 18. 生命周期

Manager 在 boot 阶段创建，但不启动 Chromium：

```go
factory := cdp.NewFactory(cdp.Options{})
browserManager, err := browser.NewManager(rootCtx, browser.Options{
	Factory: factory,
	// normalized config...
})
for _, t := range browsertool.NewTools(browserManager) {
	if browserToolEnabled(cfg, t.Name()) {
		reg.Add(t)
	}
}
```

现有 cleanup 使用组合函数追加 `browserManager.Close()`，不能覆盖 plugin/work cleanup。

Idle reaper：

- `IdleTimeout = 0` 时 Manager 不启动 reaper goroutine，浏览器只由显式 `CloseSession`/`Close` 和 Controller/Work Task 生命周期 cleanup 回收；`IdleTimeout < 0` 在 `NewManager` 直接返回配置错误，不存在隐式语义。
- Manager 单一 goroutine，检查间隔不超过 idle timeout 的四分之一（仅正数时启动）。
- 只回收 `ready`/`broken` 且超过 idle timeout 的 Session。
- 正在 opMu 操作的 Session 不被关闭；先标记 closing，操作完成后关闭。
- Close 失败保持可观察，并在下一周期重试；Manager 最终 Close 返回聚合错误。

## 19. Boot 和工具启用

- Browser tools 是运行时绑定工具，不通过 `init()` 注册携带 Manager 的全局实例。
- `tools.enabled` 为空时注册全部 browser tools。
- `tools.enabled` 非空时，只注册名单中明确出现的 browser tool。
- `tools.browser.enabled=false` 时不创建 Manager、不注册工具。
- 只注册部分 browser tools是允许的，但配置文档应提示最小可用集合为 open/state/close。
- Work task 继续复用同一 Registry；ownerID 保证状态隔离。

## 20. 可观测性

- 启动、导航和稳定等待通过 `tool.WithProgress` 发送短进度，不发送页面内容。
- 错误返回 code、recoverable、outcome_known 和 next。
- 记录结构化 slog：session hash、动作类型、revision、耗时、错误码；不记录 URL query、输入文本和页面文本。
- Session 状态从 starting/ready/broken/closing/closed 显式转换。
- Driver invalidation channel 关闭视为 `browser_disconnected`，Session 进入 broken。

## 21. 测试

默认测试不得启动真实 Chrome。

### 核心单测

- 同一 owner 并发 Open 只创建一个 Driver。
- 不同 owner 状态完全隔离。
- owner 缺失显式失败。
- revision 正常增长、相同快照保持、事件失效、旧 revision 被拒绝。
- 同 request_id 同参数返回缓存结果。
- 同 request_id 不同参数返回 conflict。
- Click/Type 已派发后 Observe 失败返回 outcome_unknown，且不自动重复。
- Type 对 password/file 输入在 Session 层拒绝，Fake Driver action call count 保持 0；生产 Driver 再做一次防御校验。
- CloseSession 和 Manager.Close 幂等。
- idle reaper 不关闭活跃操作，能够重试失败 close。
- State 和 Elements 返回副本，外部修改不污染单一可信状态。

### 工具单测

- Schema 必填字段和范围。
- ReadOnly/PlanModeSafe 分类。
- jobs session owner 正确传入 Service。
- 所有 Error 都保留结构化结果和非 nil Go error。
- browser_state 输出受 max chars/elements 限制并适合 snip。

### CDP 纯逻辑单测

- DOMSnapshot/AX merge。
- 可见交互元素过滤、顺序和去重。
- password/value 脱敏。
- iframe/target NodeRef 映射。
- URL 验证和 executable discovery。
- Chrome/Edge/Chromium/Chrome for Testing 的 kind 过滤和发现优先级。
- 调试端口候选：返回非零回环端口且可重绑；候选器失败显式返回错误。
- 启动参数：显式 `--remote-debugging-address=127.0.0.1` 与 `--remote-debugging-port=<nonzero>`，拒绝 `0`/越界端口，不含 `port=0`、wildcard 地址或 `--enable-automation`。
- Factory 有界重试：端口冲突前两次失败第三次成功、三次冲突显式失败（错误含尝试次数与原因）、非端口错误不重试、失败实例全部 `Close`。

### Profile/凭据接口测试

- 默认 ProfileProvider 只创建临时目录，并且 Release/Manager.Close 幂等清理自己拥有的路径。
- Fake ProfileProvider 可以返回 managed/attach lease，证明 Manager/Driver 接口无需改动；第一版生产 Provider 不实现这两种模式。
- managed/attach 未配置时不能静默降级为 ephemeral。
- CredentialProvider 默认 disabled；第一版没有任何工具能够读取或填入 secret。
- Fake CredentialLease 的 secret 在回调后被清零，日志和 JSON 结果无明文。

### Boot/Config 测试

- 默认值、非法值回退、enabled false。
- tools.enabled 过滤。
- Manager cleanup 被组合执行。
- 浏览器未安装时 boot 仍成功，首次 open 显式失败。

### 真实集成测试

真实 Chromium 测试必须通过 build tag 或 `WORKGROUND2_BROWSER_INTEGRATION=1` 显式开启。测试只访问本地 `httptest.Server`，覆盖：

- open -> state -> type -> click -> state。
- DOM 变化后 stale revision。
- 新建、激活、关闭 tab。
- 取消导航、关闭浏览器和进程回收。
- 可见 Chrome 下 `navigator.webdriver === false`；实际调试端口非零回环且 `/json/version` 可达；关闭后端口不可连接、进程退出、Profile 回收（headless 不承诺 `webdriver=false`）。

## 22. 验收标准

全部满足才算第一版完成：

1. 分支中没有 MCP、截图、上传、下载或 UI 实现。
2. Google Chrome 和兼容 Chromium 浏览器可由 kind/executable_path 发现；浏览器未安装只在首次 open 显式失败。
3. 九个工具按本设计注册，Schema、ReadOnly 和 PlanMode 属性正确。
4. 同一 WorkGround2 parent session 可完成启动、导航、读取、点击、输入、滚动、tab 和关闭。
5. 不同 parent session 不共享 Driver、Tab、revision 或 request cache。
6. 页面变化后旧 revision 必须失败，不能落到其他节点。
7. 相同 request_id 可安全重复；冲突和结果不确定显式返回。
8. 工具取消不会误杀其他 Session；Controller cleanup 和空闲回收没有进程泄漏。
9. ProfileProvider 和 CredentialProvider 扩展点及安全测试存在，但第一版只启用 ephemeral profile；密码输入默认允许（`allow_password_input`），不接入密码库。
10. 默认测试不依赖已安装浏览器；真实集成测试显式 opt-in。
11. 定向测试、`go test ./...` 和 `go vet ./...` 通过；若全量存在独立既有失败，必须给出可重复证据。
12. Feature Map、配置示例和用户文档同步更新。

## 23. Worker 实现边界

Worker 接收一个完整任务，一次实现本设计的第一版，不拆成多个互相猜接口的小任务。

允许修改：

- `go.mod`、`go.sum`
- `internal/browser/**`
- `internal/tool/browser/**`
- `internal/config/**` 中浏览器配置相关代码和测试
- `internal/boot/**` 中浏览器装配相关代码和测试
- 浏览器配置/工具用户文档
- `Codex/KnowledgeBase/FeatureMap.md` 仅在范围变化时补充，最终状态由 Codex 收尾

禁止修改：

- Provider/Agent 的主循环和 Tool 接口。
- MCP、Plugin、Desktop UI、Bot 专属流程。
- 图片或 Artifact 协议。
- 权限策略的通用语义。
- 与浏览器能力无关的格式化、重构、依赖升级。
- git stage、commit、push、merge 和 release。

遇到设计无法实现时，Worker 必须停在明确失败点，报告冲突的接口、证据和最小替代方案，不得静默缩水。

## 24. 未决问题

以下问题不阻塞第一版，实现时按本设计默认值执行：

- CDP 依赖优先使用 `chromedp`/`cdproto`，具体兼容版本由 `go.mod` 和 Go toolchain 验证后固定。
- 页面文本的最佳压缩格式可在不改变 PageState 字段的前提下调整。
- Chromium 不同版本缺少个别 CDP 字段时允许显式降级，但必须保留 AX/DOM 合并的测试路径并输出能力提示。
