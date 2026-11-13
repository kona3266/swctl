# switch-ctl - 交换机配置控制器（设计现状）

## 定位与范围

控制器统一管理交换机配置。外部系统通过 REST API 用**业务语义**（创建 VRF / 配置 BGP 邻居......）
表达意图，控制器负责把意图落到设备上。

| 维度 | 现状 |
|---|---|
| 运行形态 | 独立常驻服务（standalone daemon）+ REST API，不依赖 Kubernetes |
| 目标平台 | **Cisco Nexus 9000（standalone NX-OS）**、**锐捷 RG-S6580（RGOS 12.5）** |
| 接入方式 | **模型驱动**：Nexus 走 NX-API，锐捷走 NETCONF |
| 规模 | 1-4 台交换机 -- 单进程、单二进制、SQLite，不引入 Redis/MQ/Postgres |
| 技术栈 | Go（环境 go1.26.4） |

## 当前实现状态

| 能力 | 状态 |
|---|---|
| 北向 REST API、权威模型、版本历史、操作记录 | 已实现 |
| 设备级串行队列、崩溃恢复、verified 版本、Prometheus 指标 | 已实现 |
| 南向下发通道（NX-API / NETCONF） | **未实现**：`internal/device/dialect` 注册表为空，`driver.Device` 只有接口 |
| SSH CLI 会话引擎与文本方言 | 已移除 |

设备清单中存在设备、但该平台没有对应南向后端时，控制器在启动阶段因设备初始化失败而
**退出**（fail-fast），不以“只记账”模式继续运行。

## 平台能力

### Cisco Nexus 9000（standalone）

| 能力 | 事实 |
|---|---|
| NX-API | `feature nxapi`，HTTPS+JSON；`cli_conf`（CLI over HTTP）与 **DME（模型驱动 MO 树）** 两种模式 |
| NETCONF | 支持（需 feature） |
| 事务 | **`configure session`（暂存 + commit/abort）**、`checkpoint` + `rollback running-config checkpoint`、`configure replace` |

### 锐捷 RG-S6580（RGOS 12.5(1)B04xx）

| 能力 | 事实（来源：锐捷官方产品页 + RGOS 12.5 配置指南） |
|---|---|
| NETCONF | 支持（仅 S6520-64CQ、S6920-4C 不支持） |
| 传输 | SSH（netconf subsystem），**默认端口 830**；`netconf enable` 默认已开 |
| 会话数 | `netconf max-sessions` **默认 1** |
| capability | **默认全关**：`netconf capability { candidate \| rollback \| validate }` 需显式开启 |
| RPC | get / get-config / edit-config / copy-config / delete-config / validate / commit / cancel-commit / discard-changes / lock / unlock / get-schema |
| 标准 | RFC 6241/6242/6243/6536(NACM)/5717/6022/5277 |
| 校验 | `netconf calibration-mode { lightweight(默认) \| strict }` |
| YANG 模型 | **私有 `rg-*` 命名空间**（示例 `/rg-snmp:snmp`），无 OpenConfig/IETF 配置模型 |
| 其他通道 | gNMI over gRPC dial-in（Get/Set/Subscribe，JSON，支持 OpenConfig + 私有模型）；SNMP；OpenFlow；ZTP；设备内 Python |
| 无 | RESTCONF / REST API |

控制器在锐捷设备上依赖的能力是 **candidate + commit + cancel-commit/discard-changes**；
配置模型为私有命名空间，映射逐路径编写。

## 架构与目录结构

依赖方向单向：`httpapi -> service -> orchestrator -> driver -> (南向实现)`；
`store` / `state` / `model` 是叶子。

```
cmd/switchctl/main.go          # 装配：配置->日志/DB->Runner->HTTP->优雅退出
internal/
+--- config/                    # YAML 加载/校验/默认值；凭据解析（env/file）
+--- logx/                      # slog + 脱敏 Handler
+--- httpapi/                   # 路由、DTO、handler、中间件、错误->状态码映射
+--- service/                   # 用例编排：校验 + 计算变更 + 提交期望态 + 入队
+--- orchestrator/              # 执行引擎：设备队列、收敛流程、步骤记录、崩溃恢复
+--- state/                     # 权威态管理器：内存模型 + DB 持久化 + 同步状态
+--- model/                     # 厂商无关模型：VRF/VLAN/SVI/BGP + 差异计算
+--- device/
|   +--- driver/                # Device 接口（南向边界），当前无实现
|   +--- dialect/               # 平台能力与后端注册表（当前为空）
|   +--- conn/                  # 按设备清单与注册表装配后端
+--- store/                     # SQLite DAO + 迁移
```

### 南向接口

```go
type Device interface {
    Open(ctx) error
    Close() error
    // Pull 拉取设备配置：解析后的模型 + 原始快照（含归一化摘要）
    Pull(ctx) (*Snapshot, error)
    // Apply 应用模型差异（渲染、错误判定在实现内部完成）
    Apply(ctx, *model.Diff) (*ApplyResult, error)
    // Commit 持久化（各平台语义不同：commit / copy run start / write memory）
    Commit(ctx) error
}
```

平台差异的归属：

| 关注点 | 归属 |
|---|---|
| 连接与认证（NETCONF / NX-API / ...） | 后端实现 |
| 渲染（命令 / XML / JSON）与错误判定 | 后端实现 |
| 配置归一化与摘要（`Snapshot.Digest`） | 后端实现 |
| 标记编码（写 `description` 还是名称后缀） | 后端实现 |
| 平台约束（保留 VLAN 区间、名称长度上限） | 方言注册表以 `model.Constraints` 声明 |
| 业务模型与差异计算 | `model`（不含任何厂商知识） |

## 核心机制

### 收敛流程

任何变更都走同一条路径（apply 与 reconcile 相同）：

```
connect -> pull(拉设备配置 + 留存快照) -> diff(目标模型 vs 实际)
        -> push(在一次原子提交内应用整批差异) -> verify(再拉一次比对)
        -> 成功：持久化 + 标记 verified
        -> 失败：设备未变（原子性保证），期望态回退到上一版本，标记 drifted
```

原子性来自设备侧：NETCONF 的 `candidate` + `<commit>`（锐捷需
`netconf capability candidate`），NX-OS 的 `configure session` commit/abort。
后端在 `Apply` 中把整批差异放进一次提交（`dialect.Impl.AtomicApply` 声明该能力）；
控制器不做回滚，职责为“算差异 + 校验结果 + 决定是否持久化”。

### 提交边界

- 一个北向操作 = 一次设备提交；
- 操作记录 `base_version`，执行时目标模型 = `base_version + 1` 的历史版本，
  下发的差异只包含本操作的参数；
- 需要多次提交的场景（如“先删 SVI 再删 VLAN”）由调用方拆成多个北向操作，
  各自原子，顺序由设备队列保证。

### 版本语义

- `desired_version`：北向每次提交推进；
- `verified_version`：最后一次“收敛成功 + 验证通过”追平到的版本；
- op 完成判定 = `verified_version >= op.desired_version`；
- 漂移不回退版本：`desired > verified` 即存在未落地变更。

### 逐 op 串行

同一设备一次只执行一个 op，不合并多个 op 的差异；op 之间的顺序由该设备的 FIFO 队列保证。
不同设备之间并行。

### 管理标记与删除规则

控制器只管理自己创建的对象，靠设备侧标记识别。业务侧只提供标记值（`marking.value`），
如何写进设备（description 字段 / 名称后缀 / 其他）由平台后端决定。

| 设备上 | 在权威模型里 | 动作 |
|---|---|---|
| 带标记 | 否 | 删除 |
| 带标记 | 是 | 核对字段（含标记），缺什么补什么 |
| 无标记 | 否 | 只报告“未托管对象”，不删 |

`DELETE` 接口只允许删除管理中的对象（未托管对象返回 409）。

### BGP 邻居参数

邻居地址是 URL 路径 `{ip}`。`PUT` 的 body 字段（`remote_as` 之外均可选，未给出则保持原值）：

| 字段 | 约束 |
|---|---|
| `remote_as` | 1~4294967295；创建时必填 |
| `source_ip` | 本端建连源地址（update-source），单播 IPv4；`""` 清除 |
| `password` | BGP TCP-MD5，1~80 个可打印 ASCII（不含空格）；`""` 清除 |
| `bfd` | 启用 BFD |
| `bfd_min_rx_ms` / `bfd_min_tx_ms` / `bfd_multiplier` | 0 = 平台默认；定时器 50~30000 毫秒、倍数 1~50；需 `bfd: true` |
| `max_prefix` | 0 = 不限制，否则 1~4294967295 |
| `max_prefix_warning_only` | 需 `max_prefix > 0` |
| `route_reflector_client` | 仅 iBGP（`remote_as` = 本地 AS），否则 400 |

密码是**只写字段**：`GET` 只返回 `password_set`，操作记录的 `changes`/`delta` 里为 `***`；
由于设备不回显密钥，`ComputeVerifiable`（收敛验证）忽略该字段，而 `Compute`（下发差异）包含它，
因此带密码的邻居每次收敛都会重新下发一次密码（幂等）。

### 物理口与子接口

| 对象 | 字段 | 约束 |
|---|---|---|
| 物理口 | `name`、`mode`（`l2`/`l3`） | `mode` 缺省：新建 l3、已存在不修改；有子接口时禁止切换模式；无删除操作 |
| 子接口 | `port` + `vlan`（1~4094，dot1q）、`ip`（CIDR，必填）、`vrf` | 父口须已纳管且为三层；`vrf` 非空时须在管理中；绑定可用专用端点（与 SVI 一致） |

`{port}` 中的 `/` 必须 URL 编码（`Ethernet1%2F1`）。子接口名为 `<port>.<vlan>`。
下发顺序：端口 -> 子接口增改 -> 子接口删；设备上多余端口只报告，不删除。

### 并发与背压

- 进程单实例（`data_dir` 独占）；
- 每设备一条 FIFO 队列（同设备串行、设备间并行）；
- 队列有界（`max_queue_depth`），满了返回 429，不阻塞 HTTP 请求；
- 写入侧用 `expected_version` / `If-Match` 做乐观并发控制，冲突返回 409。

### 崩溃恢复

启动时把 `pending/running` 操作标记 `interrupted`，对应设备置 `drifted`，
随后由启动后的首轮对账收敛。

### 漂移发现

漂移只在**启动后的首轮对账**时被发现和收敛：没有周期性对账，也没有手动对账入口。
运行期间设备侧的改动不会被自动发现，需重启控制器触发启动对账。

### 稳定性机制

| 机制 | 说明 |
|---|---|
| 原子提交 | 一个北向操作的全部参数在一次原子提交内生效；失败则设备保持原样，期望态回退到本操作之前的版本 |
| verified 分离 | `verified_version` 只前进、只在验证通过时推进 |
| 非阻塞队列 | 每设备队列有上限（`max_queue_depth`），满了直接 429 |
| 乐观并发 | `If-Match` / `expected_version` 做版本 CAS，冲突 409 |
| 启动自检 | `PRAGMA quick_check`、拒绝降级启动（schema 比程序新）、迁移失败直接退出 |
| diff 带旧值 | 更新项携带 `From`/`To`，可反向应用与审计 |
| 指标 | `/metrics`：操作计数、队列深度、待落地变更数、最近验证时间 |

### 设备侧事务能力映射

| 平台 | 设备侧能力 | 控制器用法 |
|---|---|---|
| NX-OS | `configure session` + commit/abort | 一次收敛的全部变更放进一个 session，失败整体 abort |
| NX-OS | `checkpoint` + `rollback running-config checkpoint` | 人工兜底（控制器不自动回滚） |
| 锐捷 | NETCONF candidate + commit + cancel-commit | 期望态写 candidate -> `<validate>` -> `<commit>`；失败用 `cancel-commit` |

## REST API 规格

统一前缀 `/api/v1`，JSON，无内置鉴权（内网/受信环境，靠网络层控制）。

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/switches` | 交换机列表（sync_state、version、verified_version、最近对账时间） |
| GET | `/switches/{name}/vrfs` | 查询 VRF（读权威模型） |
| PUT/DELETE | `/switches/{name}/vrfs/{vrf}` | 创建/更新/删除 VRF |
| GET | `/switches/{name}/vlans` | 查询 VLAN |
| PUT/DELETE | `/switches/{name}/vlans/{id}` | 创建/更新/删除 VLAN |
| GET | `/switches/{name}/vlan-interfaces` | 查询 SVI |
| PUT/DELETE | `/switches/{name}/vlan-interfaces/{vlan}` | 创建/更新/删除 SVI |
| PUT/DELETE | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | SVI 加入/离开 VRF |
| GET | `/switches/{name}/ports` | 查询受管物理口 |
| PUT | `/switches/{name}/ports/{port}` | 纳管物理口/设置模式（`%2F` 编码，无删除操作） |
| GET | `/switches/{name}/ports/{port}/sub-interfaces` | 查询子接口 |
| PUT/DELETE | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}` | 创建/更新/删除 VLAN 子接口 |
| PUT/DELETE | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}/vrf` | 子接口加入/离开 VRF |
| GET | `/switches/{name}/vrfs/{vrf}/bgp-neighbors` | 查询 BGP 邻居 |
| PUT/DELETE | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | 配置/删除 BGP 邻居（参数见下表） |
| GET | `/operations/{id}` | 操作详情（含 changes / delta / failure） |
| GET | `/operations?switch=&status=&kind=&limit=` | 操作列表 |
| GET | `/metrics` | Prometheus 文本格式指标 |

**异步语义**：变更类接口立即返回 `202 + Location: /api/v1/operations/{id}`
（幂等变更直接 `200 + no_change`）。202 表示“**期望态已提交**”，不表示“命令已下发”。

状态码：400 参数非法 / 404 未知交换机或对象 / 409 版本冲突或对象不在管理中 /
422 平台不支持 / 429 该设备队列已满 / 503 执行引擎不可用 / 500 内部错误。

## 数据模型（SQLite 要点）

- `devices`：name/host/port/dialect(如 `cisco_nxos`、`ruijie_rgos`)/credential_ref/capabilities(JSON)/last_seen_at/last_error
- `operations`：id(ULID)/device/kind(`apply|reconcile`)/status(`pending|running|succeeded|no_change|failed|interrupted`)/phase/changes(JSON)/delta(JSON)/failure_code/failure_message/时间戳/duration_ms
- `operation_steps`、`operation_commands`（逐条变更与回显/错误标记）
- `device_configs`（权威态）+ 版本历史、`schema_migrations`

## 配置文件（YAML 要点）

```yaml
server:
  listen: "10.0.0.5:8080"        # 绑管理网，不要 0.0.0.0
storage:
  data_dir: /var/lib/switchctl
execution:
  operation_timeout: 10m
  max_queue_depth: 128
marking:
  value: managed-by-switchctl    # 标记值；编码方式由平台后端决定
devices:
  - name: nx-core-1
    host: 10.0.0.11
    port: 443                    # NX-API HTTPS
    dialect: cisco_nxos
    bgp_asn: "65000"             # 设备未跑 BGP 时创建邻居需要
    credentials: { username_env: NX1_USER, password_env: NX1_PASSWORD }
  - name: rg-leaf-1
    host: 10.0.0.21
    port: 830                    # NETCONF over SSH
    dialect: ruijie_rgos
    credentials: { username_env: RG1_USER, password_env: RG1_PASSWORD }
```

凭据解析：`private_key_file`（+passphrase）优先于 `password_env`；支持 `*_env` / `*_file`
（0600 校验）。无需 enable 密码。

## 已知缺口

| 缺口 | 影响 |
|---|---|
| 南向后端未实现（NX-API / NETCONF） | 有设备清单即启动失败，无法下发 |
| 两家设备的模型清单未核实 | 四类业务对象能否完整映射未知 |
| NX-API DME 的 MO 路径与属性名未确认 | Nexus 侧映射无法落地 |
