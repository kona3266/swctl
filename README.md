# switchctl — 面向业务的交换机配置控制器

调用方用**业务语义**操作交换机（创建 VRF、删除 VLAN……），不需要接触任何 CLI。
控制器维护一份**权威配置模型**（内存为主 + DB 持久化，版本化），业务变更直接提交到模型，
再由后台把**期望态**收敛到设备上；周期性对账负责漂移修复。

```
期望态（每交换机一份全量模型：VRF/VLAN/SVI/BGP，版本化）
   内存（读快）── DB 持久化（重启恢复）── 版本历史（审计）

写接口（业务变更）：
   API → 校验 → 内存模型应用变更 → 落 DB（版本+1）→ 202 返回
        （期望态此刻已提交，"设备怎么变"是之后的步骤）
   后台队列（每交换机串行）：
        拉设备实际配置 → diff(目标, 实际) → 一次原子提交下发整批差异 → 再拉验证 → 持久化
        成功 → 标记 verified；失败 → 设备保持原样（原子性保证），期望态回退到上一版本，标记 drifted

漂移发现（仅启动时）：
   启动后首轮对账（建立 verified 基线）；运行期间不自动发现漂移
```

- **目标平台**：Cisco Nexus 9000（standalone NX-OS）、锐捷 RG-S6580（RGOS 12.5）
- **南向**：模型驱动（Nexus 走 **NX-API**，锐捷走 **NETCONF**）——**尚未实现，见下方状态**
- **业务对象**：VRF、VLAN、SVI（VLAN 三层接口）、VRF 内 BGP 邻居
- **存储**：SQLite（WAL）
- **并发**：每交换机一条 FIFO 队列串行执行，交换机间并行
- 完整设计文档：[docs/DESIGN.md](docs/DESIGN.md)；运维手册：[docs/RUNBOOK.md](docs/RUNBOOK.md)

## 当前实现状态（重要）

| 能力 | 状态 |
|---|---|
| 北向 REST API、权威模型、操作记录 | ✅ 已实现 |
| 设备级串行队列、崩溃恢复、verified 版本、指标 | ✅ 已实现 |
| 南向下发通道（NX-API / NETCONF） | ❌ **未实现** |
| SSH CLI 会话引擎与 IOS/RGOS 文本方言 | 🗑 已移除（不再维护） |

**因此当前处于 fail-fast 状态**：配置了设备但没有对应的 southbound 后端时，
控制器会在启动阶段因设备初始化失败而**直接退出**（不会以“只记账”模式继续运行），
直到模型驱动后端接入。

## 快速开始

```bash
make build
cp configs/switchctl.example.yaml configs/switchctl.yaml   # 按需修改
export SW1_USER=admin SW1_PASSWORD=... SW2_USER=admin SW2_PASSWORD=...
./bin/switchctl -config configs/switchctl.yaml

curl -s localhost:8080/api/v1/switches
curl -s -X PUT -H 'Content-Type: application/json' -d '{"rd":"65000:1"}' \
  http://localhost:8080/api/v1/switches/sw-core-1/vrfs/CUSTOMER1
```

设备侧前置条件（NETCONF 目标）：

```
netconf enable                    # 锐捷：默认已开；端口 830
netconf capability candidate      # 关键：开启候选配置才有 commit/discard
netconf max-sessions 4            # 默认 1，需给运维/监控留口
```

## REST API（`/api/v1`）

无鉴权设计：面向内网/受信环境，不内置认证与 TLS；请只绑定管理网地址，
由网络层（防火墙/反向代理/ACL）控制访问。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/switches` | 交换机列表（sync_state、版本、最近对账时间） |
| GET | `/switches/{name}/vrfs` | 查询 VRF（读权威模型） |
| PUT | `/switches/{name}/vrfs/{vrf}` | 创建/更新 VRF，body `{"rd":"65000:1"}` |
| DELETE | `/switches/{name}/vrfs/{vrf}` | 删除 VRF（仅限管理中的对象） |
| GET | `/switches/{name}/vlans` | 查询 VLAN（读权威模型） |
| PUT | `/switches/{name}/vlans/{id}` | 创建/更新 VLAN，body `{"name":"OFFICE"}` |
| DELETE | `/switches/{name}/vlans/{id}` | 删除 VLAN（仅限管理中的对象） |
| GET | `/switches/{name}/vlan-interfaces` | 查询 VLAN 三层接口（SVI） |
| PUT | `/switches/{name}/vlan-interfaces/{vlan}` | 创建/更新 SVI，body `{"ip":"10.0.0.1/24"}`（IP 必填） |
| DELETE | `/switches/{name}/vlan-interfaces/{vlan}` | 删除 SVI |
| PUT | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | **SVI 加入 VRF**，body `{"vrf":"CUSTOMER1"}` |
| DELETE | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | **SVI 离开 VRF**（幂等） |
| GET | `/switches/{name}/vrfs/{vrf}/bgp-neighbors` | 查询 VRF 内 BGP 邻居 |
| PUT | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | **配置 BGP 邻居**，body `{"remote_as":"65001"}` |
| DELETE | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | **删除 BGP 邻居** |
| GET | `/operations/{id}` | 操作详情（含变更摘要、抽象差异 delta、失败原因） |
| GET | `/operations?switch=&status=&kind=&limit=` | 操作列表 |
| GET | `/metrics` | Prometheus 文本格式指标 |

变更类接口**异步**：立即返回 202 + `Location: /api/v1/operations/{id}`（幂等变更直接
返回 200 + `no_change` 操作）。

**乐观并发**：可用 `If-Match: <version>` 或 `?expected_version=<version>` 声明本次编辑基于
哪个期望版本；版本已被别人推进时返回 **409**，避免静默覆盖。

状态码：400 参数非法（命名/RD 格式/保留 VLAN、If-Match 非数字等）；
404 交换机或对象不在管理范围；**409** 版本冲突 / 目标对象不在管理中（防误删未托管配置）；
422 平台不支持；**429** 该设备操作队列已满（请退避重试）；503 执行引擎不可用。

## 语义要点（重要）

### 业务对象与依赖守卫

| 对象 | 说明 | 依赖守卫 |
|---|---|---|
| VRF | NX-OS `vrf context` / 锐捷 `ip vrf`，RD 必填 | — |
| VLAN | `vlan N`（+ name） | VLAN 1 与 Cisco 保留 1002~1005 禁止操作 |
| SVI | `interface VlanN` + IP（必填，CIDR） | **只能建在管理中的 VLAN 上**；VLAN 有 SVI 时禁止删 VLAN |
| SVI↔VRF | 加入/移出 VRF | VRF 与 SVI 都须在管理中；绑定时禁止删 VRF |
| BGP 邻居 | VRF 内 address-family + neighbor | VRF 须在管理中；配置邻居时禁止删 VRF |

本地 BGP AS 号：设备已在跑 BGP 时自动解析；设备尚未运行 BGP 时须在设备清单声明
`bgp_asn`，否则创建邻居返回 400。

### 业务变更只提交"期望态"
- `PUT` 返回 202 时，权威模型已更新并持久化——此时 `GET` 就能看到新状态，**但设备还没变**。
  设备的跟进情况看 `/switches` 的 `sync_state` 或操作结果。
- **幂等**：重复提交相同内容 → `no_change`（不触碰设备）。`DELETE` 幂等同理。
- `RD` 必填；VLAN `name` 可选（不提供 = 不改名）。
- 保留资源硬约束：VLAN 1、Cisco 保留 VLAN 1002~1005 禁止操作。

### sync_state
| 值 | 含义 |
|---|---|
| `unknown` | 从未对账 |
| `synced` | 对账确认一致 |
| `drifted` | 设备与期望态不一致（操作失败/中断等），需触发对账收敛 |

### desired / verified

- `/switches` 同时返回 `version`（期望版本）与 `verified_version`（最后验证落地的版本）。
  **`version > verified_version` 即存在尚未落到设备的变更**——判断"我的操作完成没有"比轮询
  操作记录更稳。

- 下发失败时**设备保持原样**（后端在一次原子提交内应用整批差异），控制器把**期望态回退到本操作之前的版本**，操作标记 `drifted`。
- 同一交换机同一时刻只允许一个未完成的 apply（保证失败回退时不会覆盖更新的变更）；并发提交返回 409。

### 设备侧管理标记与"严格删除"
控制器在设备上给自己创建的对象打**管理标记**（`marking.value`，默认
`managed-by-switchctl`）。标记如何写进设备——description 字段、名称后缀还是别的形式——
由各平台后端决定，配置与业务代码不需要知道。

对账的删除规则：
| 设备上 | 在权威模型里 | 动作 |
|---|---|---|
| 带标记 | 否 | **删除**（清理我们自己的遗留对象） |
| 带标记 | 是 | 核对字段（含标记），缺什么补什么 |
| 无标记 | 否 | 只报告"未托管对象"，**绝不删** |

由此保证：**别人的配置永远不会被自动删除**。`DELETE` 接口也只允许删除管理中的对象
（未托管对象返回 409，请人工处理）。

### 对账会覆盖管理域内的手工改动
对账 diff 是"权威模型 vs 设备实际"：管理域（VRF/VLAN/SVI/BGP）内任何非控制器所为的
改动，在下个对账周期都会被纠正回权威态。未管理的配置不受任何影响。

## 配置

见 [configs/switchctl.example.yaml](configs/switchctl.example.yaml)（含全部字段注释）。
凭据一律通过环境变量（`*_env`，支持 `${VAR}`）或 0600 权限文件（`*_file`）引用。

## 部署

单静态二进制：`make build` 后放到目标机器，用现有方式拉起（前台 / nohup / 容器 / supervisor）。
控制器假定单实例运行（同一 `data_dir` 请勿同时启动多个进程）。

> 注意：本项目处于开发期。schema 迁移由二进制启动时自动执行；更早的 schema 不兼容，
> 请删除 `data_dir` 重建。
>
> 设备清单以 YAML 为唯一事实来源：从 YAML 移除设备后，API 与周期对账立即不再管理它，
> 但其历史操作仍保留在 DB（重新加入清单后会沿用原有权威模型）。

## 开发

```bash
make check   # gofmt + go vet + go test -race
```
