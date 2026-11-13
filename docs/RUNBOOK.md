# switchctl 运维手册（Runbook）

> 架构：权威配置模型（内存 + DB）-> 期望态收敛（拉实际 -> diff -> 下发 -> 验证）；
> 漂移只在启动对账时发现（没有周期性对账，也没有手动对账入口）。
> 语义细节见 [README](../README.md) 与 [设计文档](DESIGN.md)。
>
> **当前状态**：南向下发通道（NX-API / NETCONF）尚未实现；配置了设备但没有对应后端时，
> 控制器启动会在设备初始化阶段失败并**直接退出**（fail-fast），不会以只读模式继续运行。

## 1. 部署

单静态二进制（纯 Go，无 CGO），不绑定具体进程管理器：

```bash
make build
mkdir -p /var/lib/switchctl && chmod 700 /var/lib/switchctl
cp configs/switchctl.yaml /etc/switchctl/switchctl.yaml && chmod 600 /etc/switchctl/switchctl.yaml
```

设备凭据通过环境变量注入（chmod 600 的 env 文件或容器 secret）：

```
NX1_USER=admin
NX1_PASSWORD=<密码>
RG1_USER=admin
RG1_PASSWORD=<密码>
```

```bash
set -a; . /etc/switchctl/switchctl.env; set +a
./bin/switchctl -config /etc/switchctl/switchctl.yaml
```

注意：控制器假定单实例运行--同一 `data_dir` 请勿同时启动多个进程；优雅退出（SIGTERM）
会停止接单并等待在途操作（`server.shutdown_grace` 为上限）。

## 2. 设备侧前置条件（**上线前必须完成**）

### 锐捷 RG-S6580（NETCONF）

```
enable
configure terminal
netconf enable                     # 默认已开
netconf max-sessions 4             # 默认 1！不改会挡住运维手工连接
netconf capability candidate       # 关键：不开就只有"立即生效"，没有 commit/discard
netconf capability validate
netconf calibration-mode strict    # 建议：非法 XML 提前拦截
netconf timeout 300                # 大配置下发时调大（默认 120s）
```

可选：`netconf commit auto-save`（commit 后自动保存 startup）；NACM 建控制器专用
user-group + rule-list（最小权限：只开放需要的模块路径与 RPC）。

模型清单导出（决定"能不能下发"）：

```
show netconf yang tree
show netconf yang node-path
show netconf yang file
```

### Cisco Nexus 9000（standalone）

```
feature nxapi                      # 或走 NETCONF：feature netconf
show nxapi                         # 确认 NX-API 状态（HTTP/HTTPS 端口）
```

事务能力核实：`configure session <name>` -> 改动 -> `commit` / `abort`；
`checkpoint <name>` + `rollback running-config checkpoint <name>`。

## 3. 首次接入交换机

**设备清单以 YAML 为唯一事实来源。** 从 YAML 删除设备并重启后，该设备不再出现在
`/switches`、也不能再通过 API 操作或参与对账；历史操作仍保留。
重新加回清单会沿用原来的权威模型。

**新交换机在控制器里的权威模型是空的（不导入设备现有配置）**，直接通过业务接口创建对象即可：

1. 网络可达性：锐捷 `nc -vz <IP> 830`；Nexus `curl -k https://<IP>/`（NX-API）。
2. 启动服务，`GET /api/v1/switches` 应看到交换机（`sync_state: unknown`）。
3. 启动后会自动为首批设备触发一次对账（建立 verified 基线）--
   设备不可达会在操作记录/日志中报错；可达则返回 unmanaged 报告（设备上已有的
   VRF/VLAN 全部列为"未托管"，只报告不删）。
4. 小改动试水：创建一个测试 VLAN 再删除（`PUT/DELETE /switches/<name>/vlans/3999`），
   两个操作落到 `succeeded` 即通路完整。

## 4. 日常操作

```bash
# 创建/更新 VRF（202 = 期望态已提交，设备异步跟进）
curl -s -X PUT -H 'Content-Type: application/json' -d '{"rd":"65000:1"}' \
  http://<host>:8080/api/v1/switches/<name>/vrfs/CUSTOMER1

# 删除（仅限管理中的对象；未托管对象删除会被拒 409，防误删）
curl -s -X DELETE http://<host>:8080/api/v1/switches/<name>/vrfs/CUSTOMER1

# VLAN
curl -s -X PUT -H 'Content-Type: application/json' -d '{"name":"OFFICE"}' \
  http://<host>:8080/api/v1/switches/<name>/vlans/100

# VLAN 三层接口（SVI，IP 必填）
curl -s -X PUT -H 'Content-Type: application/json' -d '{"ip":"10.0.0.1/24"}' \
  http://<host>:8080/api/v1/switches/<name>/vlan-interfaces/100

# SVI 加入 / 离开 VRF
curl -s -X PUT -H 'Content-Type: application/json' -d '{"vrf":"CUSTOMER1"}' \
  http://<host>:8080/api/v1/switches/<name>/vlan-interfaces/100/vrf
curl -s -X DELETE http://<host>:8080/api/v1/switches/<name>/vlan-interfaces/100/vrf

# VRF 内 BGP 邻居
curl -s -X PUT -H 'Content-Type: application/json' -d '{"remote_as":"65001"}' \
  http://<host>:8080/api/v1/switches/<name>/vrfs/CUSTOMER1/bgp-neighbors/10.0.0.2
curl -s -X DELETE http://<host>:8080/api/v1/switches/<name>/vrfs/CUSTOMER1/bgp-neighbors/10.0.0.2

# 物理口：纳管并置为三层（mode 缺省即 l3）
curl -s -X PUT -H 'Content-Type: application/json' -d '{"mode":"l3"}' \
  http://<host>:8080/api/v1/switches/<name>/ports/Ethernet1%2F1

# 物理口上的 VLAN 子接口（IP 必填；vrf 选填）
curl -s -X PUT -H 'Content-Type: application/json' \
  -d '{"ip":"10.0.0.1/24","vrf":"CUSTOMER1"}' \
  http://<host>:8080/api/v1/switches/<name>/ports/Ethernet1%2F1/sub-interfaces/100
# 子接口绑定 / 解绑 VRF（与 SVI 一致）
curl -s -X PUT -H 'Content-Type: application/json' -d '{"vrf":"CUSTOMER1"}' \
  http://<host>:8080/api/v1/switches/<name>/ports/Ethernet1%2F1/sub-interfaces/100/vrf
curl -s -X DELETE \
  http://<host>:8080/api/v1/switches/<name>/ports/Ethernet1%2F1/sub-interfaces/100/vrf

curl -s -X DELETE \
  http://<host>:8080/api/v1/switches/<name>/ports/Ethernet1%2F1/sub-interfaces/100

# 跟踪操作（返回体里有操作 id）
curl http://<host>:8080/api/v1/operations/<operation_id>
curl 'http://<host>:8080/api/v1/operations?switch=<name>&limit=20'
```

- 端口名里的 `/` 必须编码为 `%2F`（如 `Ethernet1%2F1`），否则会被当成路径分隔符。
- 端口存在子接口时不能切换二层/三层；先删子接口。恢复二层用 `PUT .../ports/<port>` + `{"mode":"l2"}`。
- `PUT`/`DELETE` 立即返回 202：**期望态已入库**，设备由后台收敛。
- 幂等重复提交 -> 200 + `no_change`。
- 设备跟没跟上：看 `/switches` 的 `sync_state`（`synced` / `drifted`）。

## 5. 故障处置

### sync_state = drifted
设备与期望态不一致（下发失败、操作被中断、或对账发现差异）；apply 失败时期望态会回退到本操作之前的版本。
- **不会自动重试**（没有周期对账，也没有手动对账入口）：运行期间的漂移不会被自动发现，需重启控制器触发启动对账。
- 看操作详情里的失败原因；`version > verified_version` 说明仍有未落地变更。
- 持续 drifted 常见原因：设备不可达（看 `last_error`）、XML/命令被设备拒绝
  （如删除仍被接口引用的 VRF，失败信息会透传设备报错）、
  **锐捷未开 `netconf capability candidate`**、**NETCONF 会话数被占满**、
  **对象没有对应 YANG 模型**。


### 操作 interrupted / 控制器意外重启
- 启动时自动把未完成操作标记 `interrupted`，并把对应交换机置 `drifted`；
  随后的启动对账会把设备状态收敛回期望态。
- 人工确认：`GET /api/v1/operations/<id>` + `GET /api/v1/switches`。

### 设备上有"未托管对象"报告
对账会把设备上**不带管理标记**的 VRF/VLAN/SVI/BGP 邻居列在操作详情的
`changes.unmanaged_*` 里（只报告，绝不删除）。处置选择：
- 保留它 -> 什么都不做（控制器永远不碰）。
- 纳入管理 -> 用 `PUT` 创建同名对象（会补写管理标记，自此归控制器管）。
- 删除它 -> 人工登录设备删除（控制器不做，防误删）。

### 设备侧标记被人工改动
- 有人把受管对象的标记（description 或平台约定的名称后缀）删了 -> 对账会**补写标记**（对象仍在期望态里）。
  若要彻底放弃管理：先从 API 删除对象。
- 有人给手工对象加了我们的标记字符串 -> 对账会把它当受管对象处理（在期望态里就保留，
  不在就删除）。避免在设备上使用与 `marking.*` 相同的字样。

### 管理面被改坏 -- **控制台救援（break-glass）**
控制器已不含 SSH CLI 路径，NETCONF 与 SSH 都跑在管理面上：一旦下发把管理面改坏
（mgmt ACL、AAA、`no ip ssh`、VTY 变更），远程通道会**同时失联**，此时唯一恢复手段是
**设备的物理串口控制台**：

1. 通过带外 console server（推荐）或现场 USB-串口线接入 RJ45 Console 口；
2. 以本地账号登录，恢复管理面：撤销 mgmt ACL、恢复 AAA/vty 配置、确认 `netconf enable`
   与 `ip ssh`；
3. 控制器侧：重启控制器触发启动对账，核对 `sync_state`。

**预防优于救援**：把 `line vty`、`aaa`、管理接口 shutdown、mgmt ACL 变更列入下发前护栏
（当前未实现，属加固里程碑）。

### 交换机不可达
- `GET /api/v1/switches` 看 `last_error` / `last_seen_at`。
- 锐捷 NETCONF：确认 `nc -vz <IP> 830` 通、`show netconf session` 没有占满会话；
- Nexus：确认 `feature nxapi` 仍在、HTTPS 可达。

## 6. 数据

- 全部状态：`data_dir/switchctl.db`（期望态 + 版本历史 + 操作）。
- **控制器自身备份**：定期复制该 db 即可（SQLite 用 WAL，建议先
  `sqlite3 db "PRAGMA wal_checkpoint(TRUNCATE)"` 或停服复制）。
- **恢复**：把 db 放回原位启动即可。

## 7. 常见告警含义

| 现象 | 含义 |
|---|---|
| `unsupported_dialect` | 该设备的平台后端尚未实现（当前全量如此，等 M6/M7） |
| `queue_full`（429） | 该设备待执行队列已满，退避后重试 |
| `version_conflict`（409） | 提交基于过期版本，重新 `GET` 后再改 |


## 8. 数据库异常

- 启动时执行 `PRAGMA quick_check`，库损坏会直接拒绝启动（需修复或重建 data_dir）。
- schema 版本高于本程序支持的最高版本时**拒绝启动**（防降级写坏数据）。
- 迁移失败 -> **直接拒绝启动**（打印错误后退出），修复数据库后重启。
| `该平台的后端未实现` | 该平台没有注册 southbound 后端（当前全部如此，等 M6/M7） |
| `unmanaged_*` 列表 | 设备上存在不归控制器管的对象（只报告不删） |
| `not_managed`（409） | DELETE 的目标不在期望态里，控制器拒绝代删（防误删他人配置） |
| 400 「VRF 仍被三层接口引用 / 仍配置有 BGP 邻居」 | 删除顺序不对：先删/解绑引用对象再删 VRF |
| 400 「无法确定本地 BGP AS 号」 | 设备未运行 BGP 且清单未声明 `bgp_asn` |
| NETCONF `<rpc-error>` 提示 capability 不支持 | 设备未 `netconf capability candidate`（或版本不支持） |
| 连接被拒 / 会话建立失败（锐捷） | `netconf max-sessions` 已满，或管理面 ACL 拦截 |
