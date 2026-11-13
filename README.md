# switchctl - a business-level switch configuration controller

Callers operate switches with **business-level semantics** (create a VRF, delete a VLAN, ...)
without touching any CLI. The controller keeps an **authoritative configuration model**
(in-memory first, persisted to a DB, versioned); changes are submitted to that model,
and a background worker converges the **desired state** onto the devices.
Drift repair happens during the startup reconciliation.

```
Desired state (one full model per switch: VRF/VLAN/SVI/port/sub-interface/BGP, versioned)
   in-memory (fast reads) -- DB persistence (restart recovery) -- version history (audit)

Write path (business change):
   API -> validate -> apply to in-memory model -> persist (version+1) -> 202
        (the desired state is committed at this point; "how the device changes" comes later)
   Background queue (serial per switch):
        pull device config -> diff(target, actual) -> push the whole batch in ONE atomic commit
        -> pull again to verify -> persist
        success -> mark verified; failure -> device unchanged (atomicity), desired state rolls
        back to the previous version, marked drifted

Drift discovery (startup only):
   the first reconciliation after startup establishes the verified baseline;
   drift is not discovered automatically while running
```

- **Target platforms**: Cisco Nexus 9000 (standalone NX-OS), Ruijie RG-S6580 (RGOS 12.5)
- **Southbound**: model-driven (Nexus via **NX-API**, Ruijie via **NETCONF**) - **not implemented, see status below**
- **Objects**: VRF, VLAN, SVI (VLAN L3 interface), physical port mode, VLAN sub-interface,
  BGP neighbors inside a VRF
- **Storage**: SQLite (WAL)
- **Concurrency**: one FIFO queue per switch (serial within a switch, parallel across switches)
- Design documentation: [docs/DESIGN.md](docs/DESIGN.md); runbook: [docs/RUNBOOK.md](docs/RUNBOOK.md)

## Current status (important)

| Capability | Status |
|---|---|
| Northbound REST API, authoritative model, operation records | Implemented |
| Per-device serial queue, crash recovery, verified versions, metrics | Implemented |
| Southbound push (NX-API / NETCONF) | **Not implemented** |
| SSH CLI session engine and IOS/RGOS text dialects | Removed (no longer maintained) |

**The controller therefore fails fast**: if devices are configured but no southbound backend
exists for them, startup fails during device initialization and the process **exits**
(it does not keep running in a "bookkeeping only" mode) until the model-driven backends land.

## Quick start

```bash
make build
cp configs/switchctl.example.yaml configs/switchctl.yaml   # adjust as needed
export NX1_USER=admin NX1_PASSWORD=... RG1_USER=admin RG1_PASSWORD=...
./bin/switchctl -config configs/switchctl.yaml

curl -s localhost:8080/api/v1/switches
curl -s -X PUT -H 'Content-Type: application/json' -d '{"rd":"65000:1"}' \
  http://localhost:8080/api/v1/switches/nx-core-1/vrfs/CUSTOMER1
```

Device-side prerequisites (NETCONF targets):

```
netconf enable                    # Ruijie: enabled by default; port 830
netconf capability candidate      # required: candidate config is what gives commit/discard
netconf max-sessions 4            # default is 1; leave room for operators/monitoring
```

## REST API (`/api/v1`)

No authentication by design: intended for internal/trusted networks, with no built-in auth or TLS.
Bind only to a management address and control access at the network layer
(firewall / reverse proxy / ACL).

| Method | Path | Description |
|---|---|---|
| GET | `/switches` | Switch list (sync_state, version, last reconciliation time) |
| GET | `/switches/{name}/vrfs` | List VRFs (reads the authoritative model) |
| PUT | `/switches/{name}/vrfs/{vrf}` | Create/update a VRF, body `{"rd":"65000:1"}` |
| DELETE | `/switches/{name}/vrfs/{vrf}` | Delete a VRF (managed objects only) |
| GET | `/switches/{name}/vlans` | List VLANs (reads the authoritative model) |
| PUT | `/switches/{name}/vlans/{id}` | Create/update a VLAN, body `{"name":"OFFICE"}` |
| DELETE | `/switches/{name}/vlans/{id}` | Delete a VLAN (managed objects only) |
| GET | `/switches/{name}/vlan-interfaces` | List VLAN L3 interfaces (SVIs) |
| PUT | `/switches/{name}/vlan-interfaces/{vlan}` | Create/update an SVI, body `{"ip":"10.0.0.1/24"}` (IP required) |
| DELETE | `/switches/{name}/vlan-interfaces/{vlan}` | Delete an SVI |
| PUT | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | **Bind the SVI to a VRF**, body `{"vrf":"CUSTOMER1"}` |
| DELETE | `/switches/{name}/vlan-interfaces/{vlan}/vrf` | **Unbind the SVI from its VRF** (idempotent) |
| GET | `/switches/{name}/ports` | List managed physical ports (L2/L3 mode) |
| PUT | `/switches/{name}/ports/{port}` | Manage a port / set its mode, body `{"mode":"l3"}` or `{"mode":"l2"}` (see below) |
| GET | `/switches/{name}/ports/{port}/sub-interfaces` | List the port's VLAN sub-interfaces |
| PUT | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}` | **Create/update a sub-interface**, body `{"ip":"10.0.0.1/24","vrf":"CUSTOMER1"}` |
| DELETE | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}` | **Delete a sub-interface** |
| PUT | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}/vrf` | **Bind the sub-interface to a VRF**, body `{"vrf":"CUSTOMER1"}` |
| DELETE | `/switches/{name}/ports/{port}/sub-interfaces/{vlan}/vrf` | **Unbind the sub-interface** (idempotent) |
| GET | `/switches/{name}/vrfs/{vrf}/bgp-neighbors` | List BGP neighbors in the VRF |
| PUT | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | **Configure a BGP neighbor**, body see below |
| DELETE | `/switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` | **Delete a BGP neighbor** |
| GET | `/operations/{id}` | Operation details (change summary, abstract delta, failure reason) |
| GET | `/operations?switch=&status=&kind=&limit=` | List operations |
| GET | `/metrics` | Prometheus text-format metrics |

Mutating endpoints are **asynchronous**: they return `202 + Location: /api/v1/operations/{id}`
(idempotent changes return `200` with a `no_change` operation directly).

**Optimistic concurrency**: use `If-Match: <version>` or `?expected_version=<version>` to declare
which desired version your edit is based on. If the version has already moved on, the request
fails with **409** instead of silently overwriting.

Status codes: 400 invalid parameters (names / RD format / reserved VLANs / non-numeric If-Match, ...);
404 unknown switch or object; **409** version conflict / target object not managed
(guards against deleting unmanaged config); 422 platform unsupported; **429** the device's
operation queue is full (retry with backoff); 503 execution engine unavailable.

## Semantics (important)

### Objects and dependency guards

| Object | Description | Dependency guards |
|---|---|---|
| VRF | NX-OS `vrf context` / Ruijie `ip vrf`, RD required | - |
| VLAN | `vlan N` (+ name) | VLAN 1 and Cisco-reserved 1002-1005 are rejected |
| SVI | `interface VlanN` + IP (required, CIDR) | **can only be created on a managed VLAN**; deleting a VLAN with an SVI is rejected |
| SVI<->VRF | Bind/unbind | both the VRF and the SVI must be managed; deleting a bound VRF is rejected |
| Physical port | L2/L3 mode (`switchport` / `no switchport`) | **mode change is rejected while sub-interfaces exist** |
| VLAN sub-interface | `interface <Port>.<tag>` + `encapsulation dot1q <tag>` + IP | parent port must be managed **and in L3 mode**; IP required; deleting a bound VRF is rejected |
| BGP neighbor | address-family + neighbor inside a VRF | the VRF must be managed; deleting a VRF with neighbors is rejected |

Local BGP AS: parsed from the device when BGP is already running; otherwise it must be declared
as `bgp_asn` in the device inventory, or creating a neighbor returns 400.

### BGP neighbor parameters

Body of `PUT /switches/{name}/vrfs/{vrf}/bgp-neighbors/{ip}` (everything except `remote_as` is optional):

| Field | Type | Description |
|---|---|---|
| `remote_as` | string | Peer AS number (1-4294967295); required on create |
| `source_ip` | string | Local source address used to reach the peer (`update-source`), unicast IPv4; `""` clears |
| `password` | string | BGP TCP-MD5 key, 1-80 printable ASCII characters (no spaces); `""` clears |
| `bfd` | bool | Enable BFD for this neighbor |
| `bfd_min_rx_ms` / `bfd_min_tx_ms` | int | BFD receive/transmit interval: 0 = platform default, otherwise 50-30000; requires `bfd: true` |
| `bfd_multiplier` | int | BFD detection multiplier: 0 = platform default, otherwise 1-50; requires `bfd: true` |
| `max_prefix` | int | Maximum prefixes accepted from the peer; 0 = unlimited (otherwise 1-4294967295) |
| `max_prefix_warning_only` | bool | Warn only, never tear the session down; requires `max_prefix > 0` |
| `route_reflector_client` | bool | Mark the neighbor as a client of this route reflector; **iBGP only** (`remote_as` equals the local AS), otherwise 400 |

The neighbor address itself is the `{ip}` path segment (IPv4, also the object identity); it is not a body field.

`PUT` is a **partial update**: fields omitted from the body keep their current value; only fields
present are applied (including explicit clearing). Changing `remote_as` on an existing neighbor
therefore does not require re-sending the password.

Two special rules for the password:

- **Write-only**: `GET .../bgp-neighbors` only returns `password_set: true/false` and never echoes
  the secret; operation `changes`/`delta` also show `***`.
- **Excluded from convergence verification**: devices do not echo the TCP-MD5 key, so post-push
  verification (`ComputeVerifiable`) ignores the field; consequently a neighbor with a password is
  re-pushed on every convergence (the operation is idempotent). The password is stored with the
  authoritative model (SQLite under `data_dir`), so treat the database file as a credential.

### Physical ports and VLAN sub-interfaces

Sub-interfaces live on a **Layer-3** physical port (`Ethernet1/1.100` + `encapsulation dot1q 100`).

```bash
# 1) manage the physical port and set it to Layer 3 (mode defaults to l3)
curl -s -X PUT -H 'Content-Type: application/json' -d '{"mode":"l3"}' \
  http://localhost:8080/api/v1/switches/nx-core-1/ports/Ethernet1%2F1

# 2) create the sub-interface (IP required; vrf optional)
curl -s -X PUT -H 'Content-Type: application/json' \
  -d '{"ip":"10.0.0.1/24","vrf":"CUSTOMER1"}' \
  http://localhost:8080/api/v1/switches/nx-core-1/ports/Ethernet1%2F1/sub-interfaces/100
```

- **Port name in the path**: `/` must be encoded as `%2F` (`Ethernet1%2F1`), otherwise it is treated
  as a path separator.
- `mode` in `PUT /ports/{port}`: `l3` (`no switchport`) or `l2` (`switchport`).
  **When omitted, a new port defaults to `l3`; an existing port is left unchanged.**
- **While the port has sub-interfaces, switching `l2`<->`l3` is rejected** (400, asking you to delete
  the sub-interfaces first).
- Sub-interface `vlan` is the 802.1Q tag, range **1-4094** (unlike the SVI range of 2-4094);
  dot1q only, no QinQ.
- VRF binding has two equivalent forms: pass `vrf` in the create/update body, or use the dedicated
  `PUT .../sub-interfaces/{vlan}/vrf` endpoint (same as SVIs, idempotent). In the body, an omitted
  `vrf` means "no change" and `""` moves the interface back to the global table; a non-empty VRF
  must be managed. Unbind with `DELETE .../vrf` (idempotent).
- Port objects have **no delete operation**: restore Layer 2 with `PUT /ports/{port}` +
  `{"mode":"l2"}`; removing a port from management is a manual operation.
- Push order is guaranteed by the backend: ports -> sub-interface add/update -> sub-interface delete.

### A business change only submits "desired state"

- When `PUT` returns 202 the authoritative model is already updated and persisted - a subsequent
  `GET` shows the new state, **but the device has not changed yet**. Track device progress via
  `sync_state` or the operation record.
- **Idempotent**: submitting the same content again yields `no_change` (no device access).
  `DELETE` behaves the same way.
- `RD` is required; VLAN `name` is optional (omitted = do not rename).
- Hard reservation constraints: VLAN 1 and Cisco-reserved VLANs 1002-1005 are rejected.

### sync_state

| Value | Meaning |
|---|---|
| `unknown` | never reconciled |
| `synced` | reconciliation confirmed consistency |
| `drifted` | device and desired state disagree (failed/interrupted operation, ...); needs reconciliation to converge |

### desired / verified

- `/switches` returns both `version` (desired version) and `verified_version` (the last version
  verified as landed). **`version > verified_version` means changes have not reached the device**,
  which is a more reliable "is my operation done?" signal than polling operation records.

- On failure the **device is left untouched** (the backend applies the whole diff in one atomic
  commit), and the controller **rolls the desired state back to the version before the operation**,
  marking it `drifted`.
- A switch allows only one unfinished apply at a time (so a rollback cannot overwrite a newer
  change); concurrent submissions return 409.

### Device-side managed marker and "strict delete"

The controller marks the objects it creates on the device with a **managed marker**
(`marking.value`, default `managed-by-switchctl`). How the marker is encoded - description field,
name suffix or something else - is up to each platform backend; configuration and business code
never need to know.

Reconciliation delete rules:

| On the device | In the authoritative model | Action |
|---|---|---|
| marked | no | **delete** (clean up our own leftovers) |
| marked | yes | reconcile fields (including the marker), fill in whatever is missing |
| unmarked | no | only report as "unmanaged"; **never delete** |

This guarantees that **nobody else's configuration is ever deleted automatically**.
`DELETE` endpoints likewise only delete managed objects (unmanaged objects return 409; handle them manually).

### Reconciliation overwrites manual changes inside the managed domain

The reconciliation diff is "authoritative model vs device reality": any change inside the managed
domain (VRF/VLAN/SVI/port/sub-interface/BGP) that the controller did not make is corrected back to
the authoritative state at the next convergence (startup reconciliation, or any later apply's
full-model diff). Unmanaged configuration is never touched.

## Configuration

See [configs/switchctl.example.yaml](configs/switchctl.example.yaml) (every field is documented).
Credentials are always referenced through environment variables (`*_env`, `${VAR}` supported) or
0600 files (`*_file`).

## Deployment

Single static binary: run `make build`, copy it to the target host, and start it however you prefer
(foreground / nohup / container / supervisor). The controller assumes a single instance
(never start two processes against the same `data_dir`).

> Note: this project is under development. Schema migrations run automatically at startup, and
> older schemas are incompatible - delete `data_dir` and recreate it.
>
> The device inventory in YAML is the single source of truth: after removing a device from the YAML,
> the API and reconciliation stop managing it immediately, but its operation history stays in the DB
> (re-adding it reuses the existing authoritative model).

## Development

```bash
make check   # gofmt check + go vet + staticcheck + go test -race
```
