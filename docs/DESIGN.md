# switch-ctl — 交换机配置控制器（设计与实施计划）

## Context（背景与目标）

需要一个控制器统一管理交换机配置。目标不是大型批量运维平台，而是**小规模下的安全配置下发**：
外部系统通过 REST API 用**业务语义**（创建 VRF / 配置 BGP 邻居……）表达意图，控制器负责把
意图安全、可回滚地落到设备上，消除手工登录逐台敲命令的误操作风险。

| 维度 | 决策 |
|---|---|
| 运行形态 | 独立常驻服务（standalone daemon）+ REST API，不依赖 Kubernetes |
| 目标平台 | **Cisco Nexus 9000（standalone NX-OS）**、**锐捷 RG-S6580（RGOS 12.5）** |
| 接入方式 | **模型驱动**：Nexus 走 NX-API，锐捷走 NETCONF；**SSH CLI 已移除** |
| 规模 | 2 台交换机 —— 单进程、单二进制、SQLite，不引入 Redis/MQ/Postgres |
| 技术栈 | Go（环境 go1.26.4） |

## 平台能力事实（已核实）

平台在 2025 年发生过一次整体切换：从 "Cisco IOS/IOS-XE + 锐捷接入级 RGOS" 改为
"Nexus 9000 + RG-S6580"。这意味着**两家都能走模型驱动**，CLI 不再是唯一路径。

### Cisco Nexus 9000（standalone）

| 能力 | 事实 |
|---|---|
| NX-API | `feature nxapi`，HTTPS+JSON；`cli_conf`（CLI over HTTP）与 **DME（模型驱动 MO 树）** 两种模式 |
| NETCONF | 支持（需 feature），生态不如 NX-API 成熟 |
| 事务 | **`configure session`（暂存 + commit/abort）**、`checkpoint` + `rollback running-config checkpoint`、`configure replace` |
| 生态参照 | Terraform NX-OS provider、Ansible `nxos`（httpapi）、NAPALM `nxos` 均走 NX-API |

### 锐捷 RG-S6580（RGOS 12.5(1)B04xx）

| 能力 | 事实（来源：锐捷官方产品页 + RGOS 12.5 配置指南） |
|---|---|
| NETCONF | 支持（官方规格"管理方式"栏 + 配置指南章节；仅 S6520-64CQ、S6920-4C 不支持） |
| 传输 | SSH（netconf subsystem），**默认端口 830**；`netconf enable` 默认已开 |
| **会话数** | `netconf max-sessions` **默认 1** —— 控制器会独占，需上调 |
| capability | **默认全关**：`netconf capability { candidate \| rollback \| validate }` 需显式开启 |
| RPC | get / get-config / edit-config / copy-config / delete-config / validate / **commit** / **cancel-commit** / discard-changes / lock / unlock / get-schema |
| 标准 | RFC 6241/6242/6243/6536(NACM)/5717/6022/5277 |
| 校验 | `netconf calibration-mode { lightweight(默认) \| strict }` |
| YANG 模型 | **锐捷私有 `rg-*` 命名空间**（示例 `/rg-snmp:snmp`），未见 OpenConfig/IETF 配置模型 |
| 其他通道 | gNMI over gRPC dial-in（Get/**Set**/Subscribe，JSON，支持 OpenConfig + 私有模型）；SNMP；OpenFlow；ZTP；设备内 Python |
| 无 | RESTCONF / REST API（全文档 0 命中） |

结论：**锐捷开 `candidate` 后能拿到候选配置 + commit + confirmed-commit 语义**，
比原先"锐捷只有 checkpoint"的假设强一档；但模型是私有的，映射必须逐路径编写。

## 核心设计决策

### D1. 期望态 + diff 收敛（不做命令级"快路径"）

早期版本有两条下发路径：把业务变更渲染成命令后**直接推送**（快路径），以及周期性全量对账。
快路径的问题是：不拉全量、无验证、"命令被接受"≠"配置正确"，失败后靠对账补锅，
且 op 状态可能和设备事实背离（op=失败，但设备最终被对账改对了）。

**改为单一路径**：任何变更都走 `拉实际 → 解析 → diff(期望, 实际) → 下发差异 → 再拉验证`。
收益：漂移免疫（下发内容在执行时求解，而非提交时渲染）、op 状态可验证、失败可安全重试
（diff 天然幂等）、删除语义统一、只有一套执行代码。

### D2. 逐 op 串行收敛，**不合并**下发

多个 op 排队时不做"合并成一次 diff"的优化。原因：合并后一次下发失败**无法归因**到具体 op
（op1 改路由、op2 改 BGP 邻居，BGP 失败时无法判断谁该失败）。

代价是 op2 要等 op1 的完整收敛周期（秒到十几秒量级）。在 2 台设备、人工/CI 节奏下可接受。
将来若出现突发写入需求，再做"合并下发 + 对象级归因"，届时版本号语义不需要改。

### D3. 南向模型驱动，移除 SSH CLI

**已移除**：`internal/session`（SSH 会话引擎）、`internal/device/conn`（CLI 打开器）、
`internal/device/{ciscoios,ruijiergos}`（文本解析/命令渲染方言）、IOS 驱动实现。

移除代价（必须显式接受并弥补）：

1. **Day-0 引导仍需要 CLI/Console**：管理 IP、SSH、AAA、`netconf capability candidate`
   本身就是配置，得先进设备；
2. **Break-glass 只能是串口控制台**：一次下发若把管理面改坏（mgmt ACL / AAA / `no ip ssh`），
   NETCONF 与 SSH 同时失联——**控制台走另一条物理链路**（RJ45 Console + USB-串口线或带外
   console server），这也是唯一不可替代的救援路径。没有带外 console server 就意味着要人到现场；
3. **模型覆盖缺口无可回退**：没有 YANG 模型的对象既推不了也读不了，删除 CLI 后无处兜底，
   因此**必须先把模型清单导出核实**（见"里程碑"与"验证方式"）。

### D4. 版本号：desired / verified 双版本（已实现）

- `desired_version`：北向每次提交推进；
- `verified_version`：最后一次"收敛成功 + 验证通过"追平到的 desired 版本；
- **op 完成判定 = `verified_version ≥ op.desired_version`**（比轮询 op 记录更稳，版本号是完成判定的依据）；
- 漂移**不回退版本**：`desired > verified` 的 gap 本身就是"待收敛/漂移"信号；


### D7. 稳定性机制（已实现）

| 机制 | 说明 | 对标 |
|---|---|---|

| 原子提交 | **一个北向操作的全部参数在一次原子提交内生效**；失败则设备保持原样，控制器把期望态回退到本操作之前的版本 | NETCONF candidate+commit / NX-OS config session |
| verified 分离 | `verified_version` 只前进、只在验证通过时推进 | clixon candidate↔running |
| 非阻塞队列 | 每设备队列有上限（`max_queue_depth`），满了直接 429，绝不阻塞请求 | clixon lock/unlock 的显式拒绝 |
| 乐观并发 | `If-Match` / `expected_version` 做版本 CAS，冲突 409 | clixon candidate rebase |
| 启动自检 | `PRAGMA quick_check`、拒绝降级启动（schema 比程序新）、迁移失败直接退出 | clixon failsafe / repair |
| diff 带旧值 | 更新项携带 `From`/`To`，可反向应用与审计 | clixon `td_scvec`/`td_tcvec` |

| 指标 | `/metrics`：操作计数、队列深度、待落地变更数、最近验证时间 | clixon transaction log/show |

### D5. 设备侧管理标记 + 严格删除

控制器只管理自己创建的对象，靠设备侧标记识别。业务侧只提供一个标记值
（`marking.value`），**如何写进设备（description 字段 / 名称后缀 / 其他）由平台后端决定**，
通用代码里不出现任何编码细节。

删除规则：**带标记且不在期望态 → 删**；**带标记且在期望态 → 校正**；
**无标记 → 只报告"未托管对象"，绝不删**。`DELETE` 接口也只允许删除管理中的对象。

### D6. 利用设备侧事务能力

| 平台 | 可用的设备侧能力 | 控制器怎么用 |
|---|---|---|
| NX-OS | `configure session` + commit/abort | 一次收敛的全部变更放进一个 session，失败整体 abort |
| NX-OS | `checkpoint` + `rollback running-config checkpoint` | 人工兜底用（控制器不自动回滚） |
| 锐捷 | NETCONF candidate + commit + cancel-commit | 期望态写 candidate → `<validate>` → `<commit>`；确认失败用 `cancel-commit` |

这比在 classic IOS 上"逐行下发 + 事后校验 + configure replace 回滚"要干净得多——
**事务边界由设备保证**，控制器的职责退化为"算 diff + 校验 + 决定 commit 还是 abort"。

## 架构与目录结构

依赖方向单向：`httpapi → service → orchestrator → driver → (南向实现)`；
`store` / `state` / `model` / `cfgtext` 是叶子。

```
cmd/switchctl/main.go          # 装配：配置→日志/DB→Runner→HTTP→优雅退出
internal/
├── config/                    # YAML 加载/校验/默认值；凭据解析（env/file）
├── logx/                      # slog + 脱敏 Handler
├── httpapi/                   # 路由、DTO、handler、中间件、错误→状态码映射
├── service/                   # 用例编排：applyChange（校验+delta+期望态提交+入队）
├── orchestrator/              # 执行引擎：设备队列、收敛流程、步骤记录、崩溃恢复
├── state/                     # 权威态管理器：内存模型 + DB 持久化 + 同步状态
├── model/                     # 厂商无关模型：VRF/VLAN/SVI/BGP + 差异计算
├── cfgtext/                   # 配置文本归一化工具（解析后端复用）
├── device/
│   ├── driver/                # Device 接口（southbound seam）——**待实现后端**
│   └── dialect/               # 方言注册表（当前为空，待登记 NX-OS / 锐捷）
└── store/                     # SQLite DAO + 迁移
```

**southbound seam**（`internal/device/driver`）——以厂商无关模型为交换物：

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
| 连接与认证（NETCONF / NX-API / …） | 后端实现 |
| 渲染（命令 / XML / JSON）与错误判定 | 后端实现 |
| 配置归一化与摘要（`Snapshot.Digest`） | 后端实现 |
| 标记编码（写 `description` 还是名称后缀） | 后端实现 |
| 平台约束（保留 VLAN 区间、名称长度上限） | 方言注册表以 `model.Constraints` 声明 |
| 业务模型与差异计算 | `model`（不含任何厂商知识） |

## REST API 规格

统一前缀 `/api/v1`，JSON，无内置鉴权（内网/受信环境，靠网络层控制）。

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/switches` | 交换机列表（sync_state、版本、最近对账时间） |
| GET | `/switches/{name}/vrfs` | 查询 VRF（读权威模型） |
| PUT/DELETE | `/switches/{name}/vrfs/{vrf}` | 创建/更新/删除 VRF |
| GET | `/switches/{name}/vlans` | 查询 VLAN |
| PUT/DELETE | `/switches/{name}/vlans/{id}` | 创建/更新/删除 VLAN |
| GET | `/switches/{name}/vlan-interfaces` | 查询 SVI |
| PUT/DELETE | `/switches/{name}/vlan-interfaces/{vlan}` | 创建/更新/删除 SVI |
| PUT/DELETE | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | SVI 加入/离开 VRF |
| GET | `/switches/{name}/vrfs/{vrf}/bgp-neighbors` | 查询 BGP 邻居 |
| PUT/DELETE | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | 配置/删除 BGP 邻居 |
| GET | `/operations/{id}` | 操作详情（含 changes / delta / failure） |
| GET | `/operations?switch=&status=&kind=&limit=` | 操作列表 |

**异步语义**：变更类接口立即返回 `202 + Location: /api/v1/operations/{id}`
（幂等变更直接 `200 + no_change`）。注意 202 的含义是"**期望态已提交**"，不是"命令已下发"。

状态码：400 参数非法 / 404 未知交换机或对象 / **409 对象不在管理中（防误删）** /
422 方言不支持 / 503 执行引擎不可用 / 500 内部错误。

> 规划的增强：`desired_version` / `verified_version` 暴露在 `/switches` 上，
> 调用方比对版本号即可判断自己提交的操作是否已落地。

## 执行模型

### 收敛流程（apply 与 reconcile 完全相同）

```
connect → pull(拉设备配置 + 留存快照) → diff(目标模型 vs 实际)
        → push(在一次原子提交内应用整批差异) → verify(再拉一次比对)
        → 成功：持久化 + 标记 verified
        → 失败：设备未变（原子性保证），期望态回退到上一版本，标记 drifted
```

**控制器不做回滚**：NETCONF 的原子性来自 `candidate` + `<commit>`（锐捷需显式
`netconf capability candidate`），NX-OS 来自 `configure session` 的 commit/abort。
后端必须在 `Apply` 里把整批差异放进一次提交（`dialect.Impl.AtomicApply` 声明该能力）；
控制器的职责收缩为"算对差异 + 校验结果 + 决定是否持久化"。



**apply 不再推送提交时算好的 delta**：delta 隐含"设备等于提交时期望态"的假设，
一旦漂移（手工改动、上次收敛半途失败、刚回滚过）就会按错误基线下发。
现在 apply 与 reconcile 一样走 pull → diff，天然幂等；目标模型是本操作应达到的版本
（历史版本可读时用它，否则退回当前期望态）。

### 提交边界：一个北向操作 = 一次提交

- 业务变更操作记录 `base_version`，执行时目标模型 = `base_version + 1` 的历史版本，
  因此每次 apply 下发的差异**只包含本操作的参数**，不会把后续操作的意图搭车提交；
- 设备后端必须把整批差异放进一次原子提交（见上），失败即整体不生效；
- 唯一例外是需要多次提交的场景（如"先删 SVI 再删 VLAN"跨操作），由调用方拆成
  多个北向操作，各自原子、顺序由设备队列保证。

### 删除与"未托管"报告

对账把设备上**不带管理标记**的对象列入 `changes.unmanaged_*`，只报告不删除。
纳入管理的方式是用 `PUT` 创建同名对象（补写标记）。

### 崩溃恢复

启动时把 `pending/running` 操作标记 `interrupted`，对应设备置 `drifted`，
随后由启动后的首轮对账收敛（diff 幂等，重跑即修复）。

### 并发与背压

进程单实例（`data_dir` 独占）；每设备一条 FIFO 队列（同设备串行、设备间并行）；
队列有界（`max_queue_depth`），满了返回 429 而不是阻塞 HTTP 请求。
写入侧用 `expected_version` / `If-Match` 做乐观并发控制，冲突 409。

### 漂移发现（无周期性对账）

漂移只在**启动后的首轮对账**时被发现和收敛：没有周期性对账，也没有手动对账入口。
运行期间设备侧的改动不会被自动发现，需要重启控制器触发启动对账；这是明确接受的取舍。

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

凭据解析：`private_key_file`（+passphrase）> `password_env`；支持 `*_env`/`*_file`
（0600 校验）。**不再需要 enable 密码**（无 CLI shell 阶段）。

## 实施里程碑

**已完成**

| # | 内容 | 状态 |
|---|---|---|
| M0 | 骨架：配置、日志、SQLite 迁移、HTTP 服务、优雅退出 | ✅ |
| M1 | 权威态模型 + 版本化持久化 + 业务语义 API（VRF/VLAN/SVI/BGP）+ 依赖守卫 | ✅ |
| M2 | 操作模型：异步 202、步骤记录、幂等 `no_change`、409 防误删 | ✅ |
| M3 | 执行引擎骨架：设备 FIFO 队列、收敛流程步骤、周期对账、崩溃恢复 | ✅ |
| M4 | 移除 SSH CLI 与文本方言（目标平台切换为模型驱动） | ✅ |

**待实施**

| # | 内容 | 验收标准 |
|---|---|---|
| M5 | **模型清单核实**（阻塞项）：导出两家设备的 YANG/DME 模型，确认 VRF/VLAN/SVI/BGP 覆盖 | 四类对象都能给出确切的模型路径；缺口清单明确 |
| M6 | 锐捷 NETCONF 后端：`netconf capability candidate` 下 edit-config → validate → commit | 真机：创建/删除 VRF/VLAN/SVI/BGP 成功；错误 XML 被拒且设备状态不变 |
| M7 | Nexus NX-API 后端（DME 优先，`cli_conf` 备选）+ `configure session`/checkpoint | 真机：变更原子生效；验证失败能 abort/回滚 |
| M8 | ~~版本号（desired/verified）+ 北向暴露~~（已完成） | 调用方按 `verified_version` 判断完成 |
| M9 | 生产加固：break-glass runbook、管理面护栏（vty/AAA/mgmt ACL 拒写）、审计与告警 | 演练：管理面改坏后按 runbook 用控制台恢复 |


## 主要风险

1. **锐捷私有 YANG 模型覆盖不足**——最大风险。缓解：M5 先导出核实；缺口确认后再决定是缩小
   管理域还是补 gNMI 通道；
2. **NETCONF 默认关闭 candidate**——未开启就没有 commit/discard，等于退回"立即生效"，
   事务保证消失。缓解：设备准入检查项 + probe 记录能力；
3. **NETCONF 默认 1 个会话**——控制器独占会挡住运维。缓解：设备侧上调 `max-sessions`；
4. **删除 CLI 后无回退路径**——day-0 与 break-glass 依赖控制台。缓解：runbook + 带外 console；
5. **NX-API DME 的 MO 路径学习成本 + 版本差异**——缓解：先做四类对象的窄映射，避免全量建模。

## 验证方式

1. **真机核实（先于编码）**：
   - 锐捷：`show netconf yang tree` / `show netconf yang node-path` / `show netconf yang file`；
     `netconf capability candidate` 后 `<commit>`/`<discard-changes>` 行为；
     candidate 是否真隔离（edit-config 后 running 未变）、commit 失败是否整体回滚；
   - Nexus：`feature nxapi` 后 DME 的 `/api/mo/sys/...` 路径与属性名；
     `configure session` 的 commit/abort 语义与失败清理；
2. **单元测试**：`model` 差异计算 golden 用例；`store` 迁移幂等与唯一索引；
   `service` 依赖守卫与幂等分支；
3. **端到端**：httptest + 真实 service/orchestrator + 假后端（内存实现 `driver.Device`）+ 临时 SQLite，
   覆盖 apply/reconcile/失败/中断恢复；
4. **门禁**：`make check`（gofmt + vet + `go test -race`）。
