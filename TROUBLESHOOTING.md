# 三节点 Kubernetes/SRE 实验环境：问题与修复记录

最后更新：2026-09-14  
当前状态：Checkpoint 1–5 已通过；三节点和监控基线健康；k8s-ops-platform Agent/Controller 已部署在 `k8s-ops` 并完成一次真实 DeploymentReplicasUnavailable 诊断验证。

本文档持续记录实验环境中真实出现的问题、诊断依据、处理方式和最终验证结果。没有实际发生的问题不写成事故；由外部条件引起且未由本项目直接修改的事项会明确标注。

## 当前健康基线

- Kubernetes：v1.35.8，三节点全部 Ready
- containerd：2.2.1
- Calico：v3.32.2，VXLAN、BGP disabled
- Calico 状态：Available=True、Progressing=False、Degraded=False
- CoreDNS：2/2 Ready
- Prometheus Targets：23/23 UP（Checkpoint 5 前为 21/21，接入平台后新增 diagnosis-agent 与 action-controller 两个 target）
- monitoring namespace：全部 Pod Ready，当前 Pod 重启数为 0
- systemd failed units：三台均为 0
- 临时告警测试规则：已删除，当前数量为 0

## 问题总览

| ID | 阶段 | 问题 | 根因 | 处理结果 |
|---|---|---|---|---|
| NET-01 | 建群前网络验证 | Kubernetes/Calico 所需端口最初未完全互通 | 腾讯云私网安全策略缺少必要方向规则 | 用户补充最小范围私网规则后复测通过 |
| NET-02 | UDP 4789 验证 | 第一版 socat UDP 检测本身报错 | `UDP-RECVFROM` 地址族用法与 socat 1.8 不兼容 | 改用 `UDP4-RECVFROM`，六向带唯一 payload 复测通过 |
| IMG-01 | Checkpoint 1 | Docker Hub BusyBox 拉取超时 | 外部镜像仓库可达性不稳定 | 改用可达的阿里云镜像并固定 tag，测试成功 |
| CNI-01 | Checkpoint 1 | worker-2 calico-node/Felix NotReady，Operator Degraded | 节点到实际 Typha 端点 TCP 5473 被阻断 | 用户补充腾讯云安全组私网 5473；所有必要方向复测通过 |
| OPS-01 | 节点恢复 | 三台节点曾关机/开机，Events 中出现 Rebooted 和启动期探针失败 | 用户执行了节点电源操作，组件启动存在短暂收敛期 | 开机后重新验证节点、CNI、DNS、Service 和 systemd，全部恢复健康 |
| OBS-01 | Checkpoint 2 | Prometheus Operator Pod Pending | 固定调度到 master，但缺少 control-plane NoSchedule taint toleration | 增加精确 toleration；Operator 正常调度并 Ready |
| OBS-02 | Checkpoint 2 | Operator 因缺少 admission TLS Secret 挂载失败 | 关闭 admission webhook 后，Operator TLS 仍保持启用并引用 Secret | 在轻量内部部署中显式设置 `prometheusOperator.tls.enabled=false` |
| OBS-03 | Checkpoint 2 | Prometheus 部分 kubelet/node-exporter Targets DOWN | Prometheus 位于 worker-1 时，现有私网端口方向不足以覆盖所有 host-IP scrape 路径 | Prometheus 固定到 master；node-exporter 改为 Pod 网络；最终 20/20 UP |
| OBS-04 | Checkpoint 2 | Grafana sidecar OOMKilled/BackOff | 默认 27 个 dashboard 的处理量超过最初 64Mi sidecar limit | 关闭默认重型 dashboards，保留一个轻量 dashboard；sidecar limit 调至 128Mi |
| OBS-05 | Checkpoint 2 | 首次 Grafana API/Provisioning 验证为空或 500 | rollout 尚未完成时查询过早，sidecar 仍在同步和 reload | 等待所有容器 Ready 后重新验证 API、datasource 和 dashboard，全部成功 |
| OBS-06 | Checkpoint 2 | 删除测试 PrometheusRule 后，Alertmanager 180 秒内仍显示活动告警 | 删除规则不会立即发送一次显式 false 状态；Alertmanager 会保留到告警的 `endsAt` | 最终测试先把表达式改为 false，确认 resolved 后再删除规则 |

## 详细记录

### NET-01：建群前必要端口未完全互通

**现象**

建群前按真实监听器测试时，TCP 6443、TCP 10250 和 Calico VXLAN UDP 4789 的必要方向并非一开始全部成功。三台主机能 SSH/ping，并不能证明 Kubernetes 控制面、kubelet 和 Overlay 网络需要的端口已经开放。

**诊断**

- 使用绑定私网 IP 的临时监听器，避免把“端口上没有服务”误判成网络策略阻断。
- 逐方向验证：worker → master TCP 6443、master → worker TCP 10250、三节点 UDP 4789 六向。
- 同时检查路由、主机防火墙和实际监听情况；没有修改公网网络或 SSH。

**处理**

云安全组由用户在腾讯云侧补充私网最小权限规则。本项目没有自行修改云安全组，也没有向 `0.0.0.0/0` 暴露管理端口。

**验证**

- worker-1/worker-2 → master:6443 成功
- master → 两台 worker:10250 成功
- UDP 4789 六向成功
- 临时监听器均已清理

证据：`work/cluster-bootstrap-blocked.md`

### NET-02：第一版 UDP 4789 检测方法无效

**现象**

最初的 UDP listener 报错，不能据此判断云网络是否放行。

**根因**

socat 1.8 对通用 `UDP-RECVFROM` 的地址族处理不符合该命令预期；这是测试工具用法问题，不是网络失败证据。

**处理与验证**

改为显式 IPv4 的 `UDP4-RECVFROM`，先完成本机自测，再为每个方向发送唯一 payload。最终六个方向全部收到预期数据。

经验：UDP 检查必须先证明 listener 自身可用，再解释远端超时。

### IMG-01：Docker Hub 镜像拉取超时

**现象**

Checkpoint 1 网络测试使用的 BusyBox 从 Docker Hub 拉取超时。

**根因**

外部镜像仓库连接不稳定，不是 Pod 网络或 containerd 本身故障。

**处理**

改用当时真实可达的 `registry.aliyuncs.com/google_containers/busybox:1.27.2`，并固定镜像 tag。

**验证**

两个 Worker 上的测试 Pod 均 Running，跨节点 Pod 通信、DNS 和 ClusterIP Service 验证成功。

证据：`work/baseline.md`

### CNI-01：Typha TCP 5473 阻断导致 Felix NotReady

**现象**

- `calico-node` 在 worker-2 不 Ready
- Felix readiness 返回 HTTP 503
- Tigera Operator 显示 Degraded
- 日志显示 worker-2 连接 `10.0.0.6:5473` 和 `10.0.0.8:5473` 超时

**诊断**

直接测试实际 Typha endpoint，多个必要方向 TCP 5473 timeout；同时确认 Typha 进程确实监听该端口。由此把问题定位到主机外部私网策略，而不是 Pod 路由、Felix 配置或 Typha 未监听。

**处理**

停止后续实验并报告阻塞。用户随后在腾讯云安全组增加 Calico Typha TCP 5473 私网规则；未开放公网规则。

**验证**

- 三节点到两个实际 Typha endpoint（10.0.0.6、10.0.0.8）的必要 TCP 5473 连接成功
- Typha 2/2 Ready
- calico-node 3/3 Ready
- Felix readiness 全部成功
- Calico Available=True、Progressing=False、Degraded=False
- 三节点 Ready，跨节点 Pod、DNS、ClusterIP Service 正常

证据：`work/typha-5473-blocker.md`、`work/baseline.md`

### OPS-01：节点电源恢复后的短暂告警

**现象**

用户重新开机后，历史 Events 中存在 Node `Rebooted`，Calico、Typha 和 CoreDNS 有启动期 readiness/startup probe 失败记录，既有 Pod 重启计数也有所增加。

**处理**

没有通过重启或删除组件掩盖记录。等待系统正常收敛后，重新验证节点、Typha/Felix、CoreDNS、跨节点通信、DNS、Service、containerd/kubelet 和 systemd。

**验证**

当前所有节点和组件健康。旧 Warning Events 作为真实审计线索保留，不等同于当前故障。

### OBS-01：Operator 因调度约束 Pending

**现象**

首次 Helm 安装后，Prometheus Operator Pod Pending。Scheduler 报告 master 有未容忍 taint，两台 Worker 又不匹配 nodeSelector。

**根因**

配置要求 Operator 固定在 `sre-master`，但没有同时容忍 `node-role.kubernetes.io/control-plane:NoSchedule`。

**修复**

给 Operator 增加仅针对 control-plane taint 的 `Exists/NoSchedule` toleration，没有移除 master taint。

**验证**

Operator 1/1 Ready，最终监控资源由 Operator 正常协调。

证据：远端 `/opt/sre-lab/experiments/01-observability/helm-pending-install.txt`

### OBS-02：Operator TLS Secret 缺失

**现象**

Operator 新 Pod 报 `FailedMount`，缺少 `monitoring-kube-prometheus-admission` Secret。

**根因**

`admissionWebhooks.enabled=false` 只关闭 webhook 资源，没有自动关闭 Operator 自身 TLS Secret 挂载。

**修复**

本环境所有相关 Service 均为集群内部访问，因此显式设置：

```yaml
prometheusOperator:
  admissionWebhooks:
    enabled: false
  tls:
    enabled: false
```

**验证**

Operator Pod 1/1 Ready、0 重启，缺失 Secret 不再阻塞启动。

证据：远端 `/opt/sre-lab/experiments/01-observability/operator-tls-secret-issue.txt`

### OBS-03：部分 Prometheus Targets DOWN

**现象**

Pod 都可以是 Running，但 Prometheus API 显示从 worker-1 到部分远端 kubelet/node-exporter host 地址的采集失败。

**根因**

现有云私网规则是按 Kubernetes 必要方向设计的，并不天然覆盖“任意 Worker 作为 Prometheus 对所有节点 host port 发起采集”的额外路径。继续开放更多公网/主机端口不符合最小权限原则。

**修复**

- Prometheus 固定到 master，复用已经验证的 master → worker kubelet 10250 路径。
- node-exporter 设置 `hostNetwork: false`，通过 Calico Pod IP 抓取，避免新增主机 9100 网络规则。
- 没有修改腾讯云安全组或暴露公网 Service。

**验证**

Prometheus API 最终显示 20/20 active targets UP，lastError 为空。

证据：`checkpoint2-docs/observability-baseline.md`、远端 `prometheus-targets.txt`

### OBS-04：Grafana sidecar OOMKilled

**现象**

Grafana dashboard/datasource sidecar 出现 OOMKilled 和 BackOff，导致 Grafana Pod 不能稳定 Ready。

**根因**

默认约 27 个 dashboard 的加载/写入峰值超过最初为 sidecar 设置的 64Mi limit。小集群内存约束不能仅靠把所有默认内容打开后观察 Pod Running。

**修复**

- 设置 `defaultDashboardsEnabled: false`
- 仅提供一个轻量 `SRE Node Overview` dashboard
- 两个 sidecar request 调整为 48Mi、limit 调整为 128Mi
- Grafana 主容器保持 request 96Mi、limit 256Mi

**验证**

- 当前 Grafana Pod 3/3 Ready、所有当前容器 restartCount=0
- Grafana API database=ok
- Prometheus datasource 返回 20 条真实 `up` series
- dashboard UID `sre-node-overview` 存在
- Prometheus 仍能查询到此前真实 OOMKilled 历史指标，证明可观测链路有效

证据：远端 `grafana-sidecar-issue.txt`、`grafana-validation.md`、`oom-metrics-raw.txt`

### OBS-05：Grafana 验证时机过早

**现象**

部署过程中第一次读取 provisioning/API 时返回空结果或 HTTP 500。

**根因**

Helm rollout 尚未完全结束，sidecar 仍在写入 datasource/dashboard 并调用 Grafana reload。

**处理**

没有把短暂结果当作最终失败，也没有重新生成敏感凭据。等待三容器全部 Ready，并通过 sidecar 日志确认文件写入及 reload HTTP 200 后再验证。

**验证**

最终 API health、datasource proxy query 和 dashboard search 全部成功。管理员密码没有写入报告。

### OBS-06：测试告警删除后未立即 resolved

**现象**

第一轮临时 `ObservabilityPipelineTest` 直接删除规则后，Alertmanager 在 180 秒观察窗口内仍保留 active 状态。

**根因**

直接删除规则不会保证 Prometheus 先发送一次表达式为 false 的更新；Alertmanager 会根据最后一次告警携带的 `endsAt` 保留状态。

**修复后的验证流程**

1. 创建 `vector(1)`、`for: 20s` 的隔离测试规则。
2. 观察 pending（创建后 12 秒）。
3. 观察 firing 并确认 Alertmanager 收到（创建后 33 秒）。
4. 把表达式更新为 `vector(0) > 0`。
5. 观察 resolved（禁用后 60 秒）。
6. 删除临时 PrometheusRule。

最终临时规则对象计数为 0，Alertmanager active alerts 为空。

证据：远端 `/opt/sre-lab/experiments/01-observability/alert-pipeline-test.md`

## 已知限制与暂不处理事项

这些不是当前故障，但会影响后续设计：

- `tigerastatus tiers` 等待可选 Tigera API server；当前未安装该组件。Calico dataplane 自身为 Healthy，不能把两者混为一谈。
- Prometheus/Grafana 当前未配置持久存储；Pod 重建可能丢失未通过 ConfigMap/Helm 管理的本地数据。
- Alertmanager 为单副本，尚未配置外部通知 receiver；Checkpoint 2 只验证到 Alertmanager 的状态链路。
- Loki 未部署：监控栈实际工作集约 596.57MiB，master 同时承载控制面和 Prometheus，优先保留实验余量。
- `k8s-ops-platform` Agent 当前未部署，Agent metrics 延后到应用/CI-CD 阶段。
- 当前是单 control-plane 实验集群，不具备生产级控制面高可用。
- 历史 Events 中保留已恢复问题的 Warning 记录；判断当前健康必须结合时间、Pod 当前状态和组件 API。

## 后续维护规则

后续每遇到真实问题，在本文件追加一节，至少包含：

1. 时间和所属 Checkpoint
2. 用户可见现象与原始错误
3. 影响范围
4. 排查过程和被排除的原因
5. 根因
6. 修复动作
7. 恢复验证
8. 证据文件路径
9. 是否遗留风险或需要回滚

禁止用删除集群、清理 Events 或重装节点来掩盖问题；先保存现场，再修复，再验证。

## 证据索引

- Checkpoint 1 本地材料：`work/cluster-bootstrap-blocked.md`、`work/typha-5473-blocker.md`、`work/baseline.md`
- Checkpoint 2 本地汇总：`checkpoint2-docs/observability-baseline.md`
- Checkpoint 1 服务器证据：`/opt/sre-lab/experiments/00-cluster-baseline/`
- Checkpoint 2 服务器证据：`/opt/sre-lab/experiments/01-observability/`

## Checkpoint 3：Node NotReady Detection & Recovery

时间：2026-09-13  
实验目标：仅停止 `sre-worker-1` 的 kubelet，观察真实 Node NotReady 检测、告警链路、Pod 驱逐、Deployment 行为、业务连续性和恢复过程。  
最终结果：`PASS_WITH_CRITICAL_FINDING`。检测、告警触发、Pod 行为、业务连续性、恢复和最终集群健康均满足；但 NodeNotReady 在 kubelet 恢复前提前 resolved，不能视为正确的恢复闭环。

### 关键时间线

- `T0 = 2026-09-13T23:19:58.708818329+08:00`：`sudo systemctl stop kubelet`，验证 kubelet 为 `inactive`。
- `T_node_notready = 2026-09-13T23:20:43+08:00`：Node Ready 从 `True` 变为 `Unknown/NodeStatusUnknown`。
- `T_workload_change = 2026-09-13T23:20:43+08:00`：Node unreachable taint 添加；Pod `Ready=False`；Deployment `Available=False`；EndpointSlice worker-1 端点 `ready=false`。
- `T_alert_pending = 2026-09-13T23:21:12.555594268+08:00`：Prometheus NodeNotReady rule `activeAt`。
- `T_alert_firing = 2026-09-13T23:22:12.555594268+08:00`：`activeAt + for=60s` 后 firing。
- `T_alertmanager = 2026-09-13T23:22:12.555+08:00`：Alertmanager `startsAt`；首次 API 观测时间为 `23:22:15.224162324+08:00`。
- `T_eviction_mark = 2026-09-13T23:25:43+08:00`：TaintManagerEviction；替代 Pod 同时创建。
- `T_worker1_pod_terminating = 2026-09-13T23:26:13+08:00`：旧业务 Pod `deletionTimestamp`。
- `T_alert_resolved = 2026-09-13T23:26:42.556840676+08:00`：告警提前 inactive，早于恢复时间。
- `T_recover_start = 2026-09-13T23:27:32.096300579+08:00`：手动启动 kubelet。
- `T_kubelet_active = 2026-09-13T23:27:32.149434970+08:00`。
- `T_node_ready = 2026-09-13T23:27:32+08:00`：Node Ready 恢复；首次轮询观测为 `23:27:41.090186079+08:00`。
- `T_pods_healthy = 2026-09-13T23:28:06+08:00`：业务替代 Pod Ready，副本容量恢复。
- `NodeDetectionLatency = 44.291s`
- `AlertPendingLatency = 73.847s`
- `MTTD = 133.847s`
- `NodeRecoveryTime <= 1s`（Node condition 只有秒级精度）
- `AlertResolveTime = -49.540s`（负值，说明 resolved 发生早于 kubelet 恢复）
- `TotalIncidentDuration = 487.291s`
- `ServiceDowntime = 0s`

### CP3-01：业务基线尝试扩到 3 副本时无法调度

**现象**

已有 `sre-baseline/baseline-http` 为 2 副本，分别位于 worker-1 和 worker-2。按实验要求尝试扩为 3 副本时，新 Pod 一直 Pending，Deployment rollout 超时，状态停在 `2/3`。

**影响范围**

仅影响实验前业务基线准备，没有进入故障实验；原有两个 Running 副本没有被删除，Service 仍有可用后端。

**排查与已排除原因**

- master 存在 `node-role.kubernetes.io/control-plane:NoSchedule` taint，普通业务不能调度到 master。
- 现有 Pod 使用 required pod anti-affinity，按 `kubernetes.io/hostname` 强制每个 worker 最多一个同标签 Pod。
- 两个 worker 已各有一个旧副本；第三个副本既不能去 master，也不能落到已有同标签 Pod 的 worker。
- 当时只允许修改轻量测试 Deployment，不允许删除 master taint，也不允许把实验变成调度策略实验。

**根因**

在 master NoSchedule taint、两个可调度 worker 和 required anti-affinity 同时存在时，3 副本没有合法的调度位置。旧 ReplicaSet 仍带 required anti-affinity，进一步阻止新副本在 worker 上创建。

**处理**

- 保存了原始 Deployment YAML 和失败现场。
- 没有修改 master taint，也没有删除或逼近 Calico、网络、节点或其他系统组件。
- 恢复为实验前原配置：2 副本、required anti-affinity、每个 worker 一个业务 Pod。
- 将 3 副本要求记录为未完全满足项，而不是通过改变调度语义强行制造结果。

**验证**

最终 `baseline-http` 为 2/2 Ready，worker-1 和 worker-2 各一个业务 Pod；Service EndpointSlice 包含两个 Ready 端点。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/business-baseline-before.txt`
- `/opt/sre-lab/experiments/02-node-notready/baseline-http-deployment-before.yaml`

**遗留问题**

未解决：实验没有使用 3 副本。当前 2 副本足以验证 Service 连续性，但不能覆盖“多副本分布在两个以上可调度节点”的场景。

### CP3-02：sre-master 无法通过私网 IP SSH 控制 worker-1

**现象**

从 `sre-master` 执行 `ssh ubuntu@10.0.0.8` 返回 `Permission denied (publickey,password)`，不能依赖 master 自动控制 worker-1 的 kubelet。

**影响范围**

只影响自动恢复 watchdog 的部署方式；本机到三台主机的 SSH alias 正常，业务和 Kubernetes 控制面不受影响。

**排查与已排除原因**

- 三台主机从本机均可通过 `sre-master`、`sre-worker-1`、`sre-worker-2` alias SSH 登录。
- `sre-master` 的 `/etc/hosts` 或 SSH 配置不能解析 `sre-worker-1` alias。
- master 到 worker-1 私网 IP 没有可用的公钥认证。
- 没有修改 SSH、网络或云安全组来绕开该限制。

**根因**

当前自动化使用本机 SSH 配置和应用侧分发，不是通过 master 对 worker 建立管理 SSH。master 到 worker 的私网 SSH 本来就不是本项目已验证的控制路径。

**处理**

- 主控继续从本机通过 `sre-worker-1` alias 执行故障注入和恢复。
- 在 worker-1 本机创建一次性 15 分钟 watchdog，避免控制会话中断后 kubelet 长期停止。
- watchdog 唯一动作是 `sudo systemctl start kubelet`；没有 reboot、containerd、网络或其他恢复动作。
- 正常恢复后立即取消 watchdog，并清理进程、PID 文件、脚本和空目录。

**验证**

- master 到 worker-1 私网 SSH 仍按实际结果记录为不可用；没有把它伪装成已修复。
- worker-1 kubelet 最终为 `active`。
- watchdog 日志保留 armed/cancelled 两条记录。
- 最终没有 watchdog 进程、timer 或 cron 残留。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/watchdog.log`
- `/opt/sre-lab/experiments/02-node-notready/experiment-meta.txt`
- `/opt/sre-lab/experiments/02-node-notready/recovery.txt`

**遗留问题**

未解决：master 到 worker 仍不能直接 SSH 控制。若后续需要节点故障自动化，应单独设计安全的管理通道；本次没有为此修改网络或 SSH。

### CP3-03：watchdog 第一次用 setsid 启动时 PID 验证失败

**现象**

第一次使用 `setsid` 启动 watchdog 时，记录的 PID 很快不存在，`ps -p` 返回失败。由于脚本启用了 `set -e`，验证命令提前退出；此时 kubelet 还没有被停止。

**影响范围**

仅影响安全恢复机制的首次启动验证。没有执行 `systemctl stop kubelet`，没有造成节点故障，也没有进入实验 T0。

**排查与已排除原因**

- `setsid` 在进程组领导者场景下发生了 fork，记录的 PID 不一定是最终运行脚本的 PID。
- 没有把 PID 不存在误判为 kubelet 故障，也没有根据错误 PID 执行清理。
- kubelet 状态当时仍为 `active`。

**根因**

watchdog 进程 fork 后的 PID 管理与外层 Shell 的 `set -e` 组合不可靠。

**处理**

- 改用 `nohup bash /run/checkpoint3/kubelet-watchdog.sh` 启动，PID 直接对应长期运行的脚本。
- 验证 `PPID=1`、PID 存活和日志已写入后再继续。
- 取消时使用进程组 ID 终止，避免只杀父进程而留下 `sleep 900`。

**验证**

第二次 watchdog PID 为 46402，存活于 PID 1 下；日志记录 `armed`。正常恢复后进程组 46391 被终止，脚本和 PID 文件已删除。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/watchdog.log`
- `/opt/sre-lab/experiments/02-node-notready/recovery.txt`

### CP3-04：NodeNotReady 在节点恢复前提前 resolved（未解决，关键问题）

**现象**

NodeNotReady 在 `23:22:12` firing，但在 `23:26:42.556` 变为 inactive；手动恢复 kubelet 的时间是 `23:27:32.096`。告警比恢复早约 49.54 秒 resolved，导致 `AlertResolveTime = -49.540s`。

**影响范围**

影响 NodeNotReady 告警的可信度和恢复验证方法。告警虽然曾经正确 firing，但没有在真实节点恢复后完成一次正确的 resolved 闭环。

**排查与已排除原因**

- Node 在 `23:26:42` 仍然是 `Unknown/NodeStatusUnknown`，没有恢复。
- kubelet 仍在 `inactive`。
- `kube-state-metrics` 在 `23:25:43` 被 TaintManagerEviction，替代 Pod 因固定在 worker-1 而 Pending。
- Prometheus 失去 `kube_node_status_condition` 指标序列后，表达式不再返回 `0`，规则被判定为 inactive。
- 之后 kube-state-metrics 在 `23:27:43` Ready，但 Node 已经恢复 Ready，所以告警没有重新 firing。

**根因**

NodeNotReady 依赖 kube-state-metrics，而 kube-state-metrics 使用 `nodeSelector: kubernetes.io/hostname: sre-worker-1`，与故障节点绑定。节点故障后监控数据源和目标同时失效，Prometheus 无法区分“节点恢复”与“指标消失”。

**修复状态**

未修复。实验严格遵守单一故障边界，没有修改 Prometheus 告警规则、kube-state-metrics 调度或 Helm release。

**验证**

- NodeNotReady 最后 firing 观测：`23:26:41.324241393+08:00`。
- 首次 inactive 观测：`23:26:46.914054727+08:00`。
- Prometheus 对应 evaluation：`15:26:42.556840676Z`。
- 恢复后规则保持 inactive/health=ok，但这不是正确的恢复 resolved。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/prometheus-alert-state.txt`
- `/opt/sre-lab/experiments/02-node-notready/alertmanager-state.txt`
- `/opt/sre-lab/experiments/02-node-notready/timeline.md`
- `/opt/sre-lab/experiments/02-node-notready/analysis.md`

**建议后续修复方向（未实施）**

- 将 kube-state-metrics 迁出故障域，或取消硬 `nodeSelector`，使用 anti-affinity 分散到其他可调度节点。
- 评估 kube-state-metrics 双副本，并处理 Prometheus 去重。
- 为 NodeNotReady 增加数据缺失条件，例如对 `absent(kube_node_status_condition...)` 或 kube-state-metrics `up == 0` 单独告警。
- 将“告警 resolved 时间必须晚于节点恢复时间”作为后续验收门禁，而不是只检查 state 从 firing 变为 inactive。

### CP3-05：kube-state-metrics 成为监控单点（未解决）

**现象**

worker-1 故障后，同节点的 `monitoring-kube-state-metrics-587c85c649-5jds2` 被标记删除并 Terminating；替代 Pod `monitoring-kube-state-metrics-587c85c649-268mq` 因硬 nodeSelector 无法在其他节点调度，保持 Pending。

**影响范围**

- NodeNotReady 的核心指标源中断。
- 故障期间 Prometheus targets 从 20/20 降到 `16 UP / 3 DOWN`，三个 DOWN 都是 worker-1 kubelet 端点。
- 告警提前 resolved，见 CP3-04。

**根因**

Observability baseline 为保证轻量部署，把 kube-state-metrics 固定在 worker-1，没有为单节点故障设计迁移或冗余。

**处理状态**

未修复。未修改 monitoring Helm 配置，仅依靠 worker-1 恢复后让替代 Pod 自动回到 Ready。

**验证**

- 替代 Pod Ready：`2026-09-13T15:27:43Z`。
- Prometheus targets 在 `23:28:02` 恢复到 20/20 UP。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/pods-transitions.txt`
- `/opt/sre-lab/experiments/02-node-notready/deployment-transitions.txt`
- `/opt/sre-lab/experiments/02-node-notready/events.txt`
- `/opt/sre-lab/experiments/02-node-notready/resources-after.txt`

### CP3-06：故障期间 Prometheus targets 降级

**现象**

worker-1 kubelet 停止后，Prometheus API 显示 `16 UP / 3 DOWN`，三个 DOWN target 均为 `10.0.0.8:10250` 的 kubelet metrics 路径。

**影响范围**

这是节点故障的预期可观测性结果，不是额外故障。它直接降低 worker-1 的 kubelet 指标覆盖，并放大了 CP3-05 的指标源风险。

**根因**

kubelet 已停止，Prometheus 无法连接 worker-1 的 10250 端口。

**处理**

没有修改 Prometheus 抓取配置或 ServiceMonitor；等待 kubelet 恢复后由 Operator 配置自动恢复 target。

**验证**

`2026-09-13T23:28:02+08:00` 恢复到 20/20 UP。

### CP3-07：裸 Pod net-test-w1 被驱逐后未重建（未处理，默认行为）

**现象**

`sre-baseline/net-test-w1` 是直接创建的裸 Pod，没有 Deployment/ReplicaSet 控制器。TaintManager 在 `23:25:43` 将其标记删除后，没有替代 Pod。

**影响范围**

Checkpoint 1 遗留的某个网络测试 Pod 不再存在；不影响业务 Service、监控或集群健康。

**根因**

Kubernetes 只负责驱逐 Pod；没有 controller 的裸 Pod 不会被自动重建。

**处理状态**

未重建。避免在已完成的 NodeNotReady 实验中重新引入无关的测试工作负载；该状态作为真实默认行为记录。

**验证**

最终没有 `net-test-w1`，也没有新的异常 Pod；`baseline-http` 业务副本已恢复正常。

**证据**

- `/opt/sre-lab/experiments/02-node-notready/events.txt`
- `/opt/sre-lab/experiments/02-node-notready/pods-transitions.txt`

### CP3-08：安全探测产生的 known_hosts 临时条目

**现象**

验证 master 到 worker-1 私网 SSH 时，使用了 `StrictHostKeyChecking=accept-new`，在 master 的 `known_hosts` 中临时写入了 `10.0.0.8`。

**影响范围**

仅影响 master 的 SSH known_hosts 状态；探测本身未建立可用会话，未修改 SSH 服务配置、密钥或网络。

**根因**

探测命令允许接受新的 host key，属于测试命令设计问题。

**处理**

使用 `ssh-keygen -R 10.0.0.8` 移除该临时条目，并验证 `ssh-keygen -F 10.0.0.8` 不再匹配。

**验证**

master `known_hosts` 临时条目已 removed；没有遗留该测试产生的 SSH 状态变更。

### Checkpoint 3 最终恢复验证

- kubelet：`active`
- Node：三节点全部 `Ready`
- Node taints：worker-1 taints=0
- Calico：`Available=True`、`Progressing=False`、`Degraded=False`
- CoreDNS：2/2 Ready
- monitoring：所有当前 Pod Ready
- Prometheus：20/20 targets UP
- 四条核心规则：全部 `inactive`、`health=ok`
- Alertmanager：ready，活动告警 0
- Grafana：`database=ok`、version 13.2.1
- Helm：`monitoring` revision 3、deployed
- 临时 PrometheusRule：0
- systemd failed units：三台均为 0
- 业务探针：677 次请求，677 成功，0 失败，ServiceDowntime=0
- watchdog：无进程、无 PID 文件、无脚本、无 timer、无 cron 残留
- 没有新的长期异常 Pod

### Checkpoint 3 证据索引

- 服务器证据目录：`/opt/sre-lab/experiments/02-node-notready/`
- 总览：`README.md`
- 时间线：`timeline.md`
- 实验前快照：`baseline-before.txt`、`preflight-topology.txt`、`business-baseline-before.txt`
- 节点与 Lease：`node-transitions.txt`、`lease-transitions.txt`
- 工作负载与端点：`pods-transitions.txt`、`deployment-transitions.txt`、`endpointslice-transitions.txt`
- 事件：`events.txt`
- 告警：`prometheus-alert-state.txt`、`alertmanager-state.txt`
- 业务连续性：`service-probe.log`、`service-probe-summary.txt`
- 恢复：`recovery.txt`、`resources-after.txt`
- 分析与面试：`analysis.md`、`interview-notes.md`
- 验收结果：`checkpoint3-validation.yaml`
- 安全机制：`watchdog.log`、`experiment-meta.txt`

## Checkpoint 3.5：Observability SPOF Fix & NodeNotReady Revalidation

时间：2026-09-13  
目标：修复 kube-state-metrics 与 sre-worker-1 的单故障域绑定，并重新验证 NodeNotReady 只在节点真正恢复后才 resolved。  
最终结果：`PASS_WITH_DUPLICATE_ALERT_FINDING`。SPOF 修复和 resolved 顺序门禁通过；双 KSM 副本引入了重复告警实例，作为未解决项记录。

### 关键时间线

- `T0 = 2026-09-13T23:43:26.634920323+08:00`
- `T_node_notready = 2026-09-13T23:44:13+08:00`，首次观测 `23:44:15.459339279`
- `T_alert_pending = 2026-09-13T23:44:27.555594268+08:00`
- `T_alert_firing = 2026-09-13T23:45:27.555594268+08:00`，首次观测 `23:45:31.571626658`
- `T_alertmanager = 2026-09-13T23:45:27.555+08:00`（`startsAt`；首次观测 `23:45:31.571626658`）
- `T_ksm_worker1_notready = 2026-09-13T23:44:13+08:00`
- `T_ksm_failover_observed = 2026-09-13T23:49:18.458197606+08:00`（active KSM targets 2→1，worker-2 副本仍 up；替代 Pod 在 worker-2 创建）
- `T_recover_start = 2026-09-13T23:51:58.215916644+08:00`
- `T_node_ready = 2026-09-13T23:51:58+08:00`
- `T_alert_resolved = 2026-09-13T23:52:27.556690708+08:00`
- `T_alertmanager_cleared_first_observed = 2026-09-13T23:52:58.752808662+08:00`
- `NodeDetectionLatency = 46.365s`
- `AlertPendingLatency = 60.921s`
- `MTTD = 120.921s`
- `NodeNotReady continuous firing = 420.001s`
- `AlertResolveDelayAfterNodeReady = 29.557s`
- `Final Prometheus targets = 21/21 UP`
- `Minimum KSM active targets during worker-1 failure = 1; never 0`

### CP3.5-01：KSM 从硬固定 worker-1 改为双副本软分散

**现象**

Checkpoint 3 的 `kube-state-metrics` 只有 1 个副本，并通过 `nodeSelector: kubernetes.io/hostname: sre-worker-1` 固定在 worker-1。worker-1 故障时 KSM 与故障对象同故障域，替代 Pod 无法调度，Prometheus 最终丢失 `kube_node_status_condition`，NodeNotReady 在恢复前提前 resolved。

**影响范围**

影响所有依赖 kube-state-metrics 的 node/deployment/pod 级告警，包括 NodeNotReady。故障节点还在 NotReady 时，告警可能因为指标源消失而被清除。

**根因**

KSM 单副本加硬 nodeSelector，使监控数据源与故障节点绑定。Prometheus 无法区分“节点已恢复”和“指标已消失”。

**处理**

通过现有 Helm release 修改，没有手工 patch Deployment：

- `kube-state-metrics.replicas: 2`
- 移除硬 `nodeSelector`
- 增加 `topologySpreadConstraints`，`topologyKey: kubernetes.io/hostname`，`whenUnsatisfiable: ScheduleAnyway`
- 增加 soft preferred `podAntiAffinity`
- 通过 `additionalPrometheusRulesMap` 增加 `KubeStateMetricsDown` 和 `PrometheusTargetDown`
- Helm revision 4 完成 KSM 双副本和规则变更；revision 6 完成最终跨 worker 分布

规则表达式：

- `KubeStateMetricsDown`: `absent(up{job="kube-state-metrics"}) or sum(up{job="kube-state-metrics"}) == 0`，`for: 2m`
- `PrometheusTargetDown`: `up == 0`，`for: 2m`

**验证**

- 修复前：KSM 1/1，固定 worker-1；Prometheus 20/20。
- 修复后：KSM 2/2，分别位于 worker-1 和 worker-2；Prometheus 21/21 UP。
- worker-1 故障期间 KSM active target 最低为 1，从未为 0。
- `kube_node_status_condition{node="sre-worker-1",condition="Ready",status="true"}` 在故障全程存在，value=0。
- NodeNotReady 在 `23:44:27.555` pending、`23:45:27.555` firing，并持续 firing 420.001 秒。
- Node Ready 之后，NodeNotReady 在 `23:52:27.557` resolved，晚于 Node Ready 29.557 秒。
- 最终 targets 21/21 UP，Alertmanager 无残留活动告警。

**证据**

- `/opt/sre-lab/experiments/03-observability-ha/ksm-before.yaml`
- `/opt/sre-lab/experiments/03-observability-ha/ksm-after.yaml`
- `/opt/sre-lab/experiments/03-observability-ha/pod-distribution.txt`
- `/opt/sre-lab/experiments/03-observability-ha/prometheus-targets.txt`
- `/opt/sre-lab/experiments/03-observability-ha/alert-rules.yaml`

### CP3.5-02：软 topology spread 没有自动重新分布副本（已解决）

**现象**

第一次修复 rollout 后 KSM 两个副本都在 worker-2 或都在 worker-1，未满足“两个副本尽量分布到两个不同 Worker”。这是故障恢复和滚动更新后常见的既有 Pod 不自动搬迁问题。

**影响范围**

影响 KSM 的节点级冗余：两个副本同节点时，该节点再次故障会同时失去全部 KSM 数据源。此时虽然没有立即故障，但修复目标未达成。

**排查与已排除原因**

- `whenUnsatisfiable: ScheduleAnyway` 是软约束，只影响新 Pod 调度评分，不会搬迁已经运行的 Pod。
- worker-1 故障后，替代 Pod 自动落到 worker-2，是预期的可用性行为，但恢复后没有自动回到两个 worker。
- 通过 Helm revision 5 再次滚动更新后，两个 Pod 仍可能被调度到同一 worker。

**根因**

软 topology spread 不保证已有 Pod 重新平衡；它只在新调度时提供偏好。

**处理**

- 增加 soft preferred podAntiAffinity，weight=100，topologyKey=`kubernetes.io/hostname`。
- 保持 `ScheduleAnyway`，避免在正常双 worker 条件下强制 Pending。
- 通过 Helm revision 6 触发一次受控滚动更新。
- 没有手工 patch Deployment，没有删除 master taint，没有使用 cluster-admin 或绕过调度器。

**验证**

最终 KSM 2/2 Ready：

- `monitoring-kube-state-metrics-7f5df76f7f-6znrw` → sre-worker-1
- `monitoring-kube-state-metrics-7f5df76f7f-qxbl2` → sre-worker-2

**遗留风险**

soft 约束仍然可能在某些资源或调度评分条件下把副本放在同一 worker；本阶段通过实际 rollout 验证了当前分布，但没有引入强制 anti-affinity 或 descheduler。

### CP3.5-03：KSM 双副本导致告警实例重复（未解决，关键后续项）

**现象**

KSM 两个副本各自抓取同一 node condition，产生带不同 `instance`/`pod` 标签的同一 `kube_node_status_condition` series。现有 `NodeNotReady` 没有按 node 聚合，因此 Alertmanager 同时收到 2 个 NodeNotReady 实例。`DeploymentReplicasUnavailable` 等规则也出现重复实例放大。

**影响范围**

- 告警噪声增加。
- 告警分组、通知和事件关联可能重复。
- 不能通过删除一个 KSM 副本来解决，否则会重新引入 CP3.5 要修复的 SPOF。

**根因**

KSM 双副本解决了数据可用性，但现有 node/deployment 级告警表达式没有对 `instance`/`pod` 维度做聚合或去重。

**处理状态**

未修复。保持实验范围内的 Prometheus 规则不变，仅记录为 Checkpoint 4 之前必须处理的后续项。

**验证**

- 故障时 NodeNotReady 在 Alertmanager 中观测到 2 个 active 实例。
- 恢复后最终 active alerts = 0；重复实例随 resolved 清理。
- 最终规则 health 均为 ok。

**建议后续修复方向**

- 将 NodeNotReady 等规则改为按 node 聚合，例如评估 `max by (node) (...)` 或等价表达式后只产生一个告警。
- 对 Deployment/Pod 级别规则按 namespace/deployment/object 聚合。
- 评估 KSM sharding 或给 Prometheus 配置去重策略；不要在未替代方案前降低副本数。

**证据**

- `/opt/sre-lab/experiments/03-observability-ha/prometheus-alert-state.txt`
- `/opt/sre-lab/experiments/03-observability-ha/alertmanager-state.txt`
- `/opt/sre-lab/experiments/03-observability-ha/analysis.md`
- `/opt/sre-lab/experiments/03-observability-ha/checkpoint3.5-validation.yaml`

### CP3.5-04：最终滚动更新期间出现短暂 Error 旧 Pod（已自动恢复）

**现象**

Helm revision 6 滚动更新结束后，短暂观察到旧 ReplicaSet 的一个 Pod 处于 `Error`。等待 rollout 收敛后，该 Pod 被删除，KSM Deployment 稳定为 2/2。

**影响范围**

仅出现在受控滚动更新窗口，KSM Service 始终有至少一个可用 target；最终没有 Pending、Error 或 CrashLoopBackOff Pod。

**根因**

滚动更新期间旧 ReplicaSet 缩容和 Pod 终止的正常过渡；需要通过最终状态确认是否长期残留。

**处理**

- 没有手工删除 Pod。
- 等待 Deployment/ReplicaSet 收敛，并重复检查 Pod、targets、rules、Alertmanager。
- 最终确认旧 ReplicaSet 已缩容到 0，只有 2 个 Running/Ready Pod。

**验证**

- KSM Deployment 2/2 Ready。
- Prometheus KSM targets 2/2 UP。
- 无长期 Pending/CrashLoopBackOff。
- 三节点 failed units 均为 0。

### CP3.5 最终验收结果

- KSM 2 副本跨节点分布：通过
- worker-1 故障时至少 1 个 KSM 存活：通过（最低 1 个 active target，从未为 0）
- `kube_node_status_condition` 故障期间持续存在：通过
- NodeNotReady pending/firing：通过
- 节点未恢复时持续 firing：通过（420.001 秒）
- kubelet 恢复后 Node Ready：通过
- Alert resolved 晚于 Node Ready：通过（晚 29.557 秒）
- 最终 Prometheus targets 全部恢复：通过（21/21 UP）
- Alertmanager 无残留活动告警：通过（0）
- 三节点最终全部 Ready：通过
- 无长期 Pending/CrashLoopBackOff：通过
- watchdog 已清理：通过（无进程、timer、cron、脚本或 PID 文件）
- 新增未解决项：KSM 双副本导致重复告警实例

### Checkpoint 3.5 证据索引

- 服务器证据目录：`/opt/sre-lab/experiments/03-observability-ha/`
- 修复前/后：`ksm-before.yaml`、`ksm-after.yaml`、`values-before.yaml`、`values-after-final.yaml`
- 调度分布：`pod-distribution.txt`、`pod-distribution-before.txt`
- 目标与规则：`prometheus-targets.txt`、`alert-rules.yaml`
- 时间线：`timeline.md`
- 告警过程：`prometheus-alert-state.txt`、`alertmanager-state.txt`
- 指标可用性：`metric-availability.txt`
- 恢复：`recovery.txt`、`pre-recovery.txt`
- 分析：`analysis.md`、`interview-notes.md`
- 验收：`checkpoint3.5-validation.yaml`
- 安全机制：`watchdog.log`

## Checkpoint 4：Node Maintenance with cordon / drain / PDB

时间：2026-09-14  
实验目标：仅对 `sre-worker-1` 做计划内维护——cordon → drain → PDB → Pod 迁移 → Service 连续性 → uncordon → 恢复验证。全程不停止 kubelet/containerd，不关机，不重启，不进入故障注入域（NodeNotReady / CrashLoopBackOff / OOMKilled / Service 故障 / CI-CD 均未涉及）。  
最终结果：`PASS`。21 项验收 gate 全部通过；业务 Service 0 中断；正常 PDB 与过严 PDB 的行为均符合设计。同时记录到 7 项真实问题/风险（CP4-01 ~ CP4-07）和 1 项环境工具链问题（CP4-08）。

说明：本节为追加内容，未修改上方任何既有记录。

### 关键时间线

- `T_cordon_start = 2026-09-14T10:20:12.454926393+08:00`
- `T_cordon_effective = 10:20:12.697637706`（CordonLatency = 0.243 s）
- `T_drain_start = 10:21:54.678754031`
- `T_first_eviction = 10:21:55.484786574`（`8qrhf`，首次采样到 `deletionTimestamp` 的时刻）
- `T_first_replacement_ready = 10:21:58`（`2nx8k` on sre-worker-2）
- `T_drain_complete = 10:22:31.547537556`（DrainDuration #1 = 36.869 s）
- `T_uncordon_after_drain = 10:24:06.732751334`
- `T_workload_redistributed = 10:24:18.497222922`
- `T_blocking_drain_start = 10:24:21.745645762`
- `T_pdb_block = 10:24:22.017521756`（PDBBlockLatency = 0.272 s）
- `T_pdb_normal_restored = 10:25:38.301670264`
- `T_final_drain_start = 10:25:41.537246101`
- `T_final_drain_complete = 10:26:23.170795425`（DrainDuration #2 = 41.634 s）
- `T_uncordon = 10:26:23.401189259`
- `T_fully_healthy = 10:26:48.557051581`
- `PodMigrationTime = 2.515 s`
- `MinimumAvailableReplicas = 2`（观测值只有 2 和 3，未跌破 PDB 底线）
- `ServiceDowntime = 0.000 s`
- `HTTPProbeSuccessRate = 100.0000%`（529/529 次 1 Hz 请求）
- `NodeUnschedulableWindow = 370.946 s`（cordon → uncordon）
- `MaintenanceWindow = 396.102 s`（cordon → 恢复正常）

### CP4-01：cordon 触发 Calico typha 自动缩容，drain 期间出现 0 个就绪 typha（未处理，已记录为维护前检查项）

**现象**

`kubectl cordon sre-worker-1` 完成 6.5 秒后，tigera-operator 就主动缩减了 typha：

```
{"level":"info","ts":"2026-09-14T02:20:19Z","logger":"typha_autoscaler","msg":"Updating typha replicas from 2 to 1"}
```

同一时刻 sre-master 上的 `calico-typha-848cf6c69d-4pd9w` 被删除，ReplicaSet 从 2 缩到 1。此时唯一存活的 typha Pod 恰好位于待维护的 sre-worker-1 上；随后 drain 驱逐了它，在新 Pod 就绪前出现约 15 秒 **0 个就绪 typha 副本** 的窗口（新 Pod `vvgq6` 在 sre-worker-2 拉取镜像耗时 14.775 s，10:22:09 才 Running）。

副本数随 cordon/uncordon 来回变化，operator 日志共 4 条：

```
02:20:19  2 -> 1   （第一次 cordon）
02:24:09  1 -> 2   （第一次 uncordon）
02:24:29  2 -> 1   （第二次 cordon，负向测试）
02:26:29  1 -> 2   （最终 uncordon）
```

**影响范围**

- Calico 数据面未受影响：整个维护期间 `Available=True、Progressing=False、Degraded=False`，Pod 网络和 Service 均正常。
- 但 CNI 控制面（typha 是 Felix 的 fan-out 缓存）的 HA 余量在维护开始前就已消失。若此时 worker-2 也发生故障，typha 会完全不可用。
- 这是"PDB 管不到、但会影响维护安全性"的典型组件。

**根因**

tigera-operator 的 typha autoscaler 按**可调度节点数**计算副本数。cordon 让一个节点在调度意义上"消失"，autoscaler 立即把副本数下调；缩小后保留的那个 Pod 又正好在待 drain 节点上，导致控制面出现单副本甚至短暂零副本。

**处理**

- 本阶段安全边界明确禁止修改 Calico / tigera-operator，因此**不做任何配置修改**。
- 仅在 uncordon 后验证 operator 能自动把副本恢复到 2/2。
- 依靠 calico-node（DaemonSet，不依赖 typha 持续在线）保证数据面不中断。
- 将"维护前确认 typha 副本与落点"写入本检查项，作为后续维护的准入条件。

**验证**

- operator 日志 4 条 `typha_autoscaler` 记录与 cordon/uncordon 时间一一对应。
- 最终 `calico-typha` Deployment `2/2`，Pod：`vvgq6 -> sre-worker-2`、`zcgh2 -> sre-worker-1`。
- 最终 `tigerastatus calico`：`Available=True Progressing=False Degraded=False`。

**遗留风险与建议**

- 应避免在承载 typha 副本的节点上做维护，或维护前先确认 typha 缩容后仍有多副本。
- 可考虑给 typha 增加 PodDisruptionBudget、提前扩容副本，或通过 operator 的 typha 覆盖配置固定副本数（本阶段未执行）。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/finding-calico-typha-autoscaler.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/drain-success.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/resources-after.txt`

### CP4-02：drain 把 KSM 副本挤到同一节点，破坏 CP3.5 的跨 worker 分布（已解决）

**现象**

第一次 drain 驱逐了位于 sre-worker-1 的 `monitoring-kube-state-metrics-7f5df76f7f-6znrw`，其替代 Pod 只能落到 sre-worker-2（当时唯一可调度节点），KSM 变成 2 个副本**都在 sre-worker-2**。收尾 drain 后仍是同一分布。

随后执行 `rollout restart` 时，两个新 Pod 又都调度到了 sre-worker-1（空节点评分最高），分布依旧不均衡。

**影响范围**

- 暂时回归 CP3.5 之前的同故障域问题：若此时 sre-worker-2 故障，两个 KSM 副本会同时消失，`kube_node_status_condition` 等序列会丢失，正是 CP3.5 要修复的监控单点。
- 未造成告警误判：维护期间无节点故障，且 CP3.5 的 NodeNotReady 修复前提是"至少一个 KSM 存活"，本次没有触发该场景。

**根因**

软反亲和（`preferredDuringSchedulingIgnoredDuringExecution`）只在**新调度**时给偏好，不会把已经存在的 Pod 拉回；drain 后 sre-worker-1 不可调度，替代副本唯一选择就是 sre-worker-2。

**处理**

- `kubectl -n monitoring rollout restart deploy/monitoring-kube-state-metrics` 触发一次受控滚动更新。
- 发现两个新 Pod 仍在同一节点后，删除 sre-worker-1 上的一个副本，让替代 Pod 按软反亲和落到 sre-worker-2。
- 没有修改 KSM 的调度约束，没有删除 master taint，没有降低副本数。

**验证**

- 最终 KSM `2/2 Ready`，一 worker 一个：
  - `monitoring-kube-state-metrics-74498898c-hrzzr -> sre-worker-1`
  - `monitoring-kube-state-metrics-74498898c-dbnlg -> sre-worker-2`
- `health-check.sh` 输出 `kube_state_metrics 2/2 / nodes: sre-worker-2 sre-worker-1`。

**遗留风险与建议**

soft 约束不保证均衡，每次节点维护后都要复查 KSM 分布。若要更稳定，建议引入 `topologySpreadConstraint`（`maxSkew` + `ScheduleAnyway`）或 descheduler（本阶段未执行）。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/drain-success.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/recovery.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/resources-after.txt`

### CP4-03：维护期间唯一真正 firing 的告警来自邻近的 baseline-http，而不是被测业务（已自动恢复）

**现象**

`DeploymentReplicasUnavailable`（表达式 `kube_deployment_status_replicas_available < kube_deployment_spec_replicas`，`for: 2m`）在整个维护窗口中**只对 `sre-baseline/baseline-http` 达到 firing**：

```
sre-baseline/baseline-http  alertstate=firing  first=2026-09-14T02:24:20Z  last=02:24:40Z
Alertmanager startsAt = 2026-09-14T02:24:12.555Z
```

被测业务 `sre-maintenance/maintenance-http` 只到 pending（可用副本降到 2 的窗口不足 2 分钟，从未满足 `for`）。同一时间 `calico-system/calico-typha` 和 `monitoring/monitoring-kube-state-metrics` 也只到 pending。

**影响范围**

- 维护窗口内产生 1 条真实告警（真正被观测到的唯一 firing），但不涉及被测业务的可用性。
- 说明节点维护会波及共享同一批节点的邻近工作负载，而 PDB 只保护被 PDB 覆盖的对象。

**根因**

`baseline-http` 使用 **required** `podAntiAffinity`（2 副本、2 个可调度 worker）。sre-worker-1 被 cordon 后，被驱逐副本的替代 Pod 只能去 sre-worker-2，而该节点已有一个同标签 Pod，于是卡在 Pending 直到 uncordon，持续超过 2 分钟，触发告警。这与 CP3-01 的 required anti-affinity 问题同源。

**处理**

- 不修改 `baseline-http`（不在本阶段范围内，且不属于被测对象），不改动告警规则来"迎合"实验。
- uncordon 后该 Pod 自动调度回 sre-worker-1，Deployment 自行恢复到 2/2，无需人工干预。
- 如实记录告警对象、时间与根因。

**验证**

- `alerts.txt` 中的 `ALERTS` 历史：唯一 firing 序列为 `sre-baseline/baseline-http`。
- Alertmanager 最终 `0` 条 active alert；所有规则最终 inactive。
- `baseline-http` 最终 `2/2`：`djdf6 -> sre-worker-2`、`xpv6w -> sre-worker-1`。

**遗留风险与建议**

建议把这类基线业务改为 preferred anti-affinity 或 `topologySpreadConstraint`，否则每次单节点维护都会产生 Pending 与告警噪声（本阶段未修改）。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/alerts.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/prometheus-alert-state.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/alertmanager-state.txt`

### CP4-04：双 KSM 副本导致 PDB 指标与告警序列重复（未修复，与 CP3.5-03 同源）

**现象**

两个 KSM 副本并存时，每个 PodDisruptionBudget 的状态序列都出现两份：

```
pdb_allowed=[{"pdb":"maintenance-http-pdb","v":"1"},{"pdb":"maintenance-http-pdb","v":"1"}]
pdb_healthy=[{"pdb":"maintenance-http-pdb","v":"3"},{"pdb":"maintenance-http-pdb","v":"3"}]
```

`ALERTS` 序列同样成对出现：`DeploymentReplicasUnavailable` 的每条记录在历史查询中都以两个相同 series 返回。

**影响范围**

- 告警与看板重复计数；如果对 `kube_poddisruptionbudget_status_pod_disruptions_allowed` 直接求和会翻倍，可能得出错误结论。
- 这是 CP3.5-03 记录的问题在本阶段的再次体现，说明 KSM 双副本带来的重复序列不只影响节点级告警。

**根因**

两个 KSM 副本各自抓取同一对象状态，产生标签只差 `instance`/`pod` 的重复 series；现有规则和查询没有按对象维度聚合。

**处理**

本阶段不修改 Prometheus 规则（保持与 CP3.5 基线一致），仅记录并复用 CP3.5-03 的修复方向。

**验证**

- `/opt/sre-lab/experiments/04-node-maintenance/metric-availability.txt` 中 PDB 指标成对出现。
- `/opt/sre-lab/experiments/04-node-maintenance/alerts.txt` 中 ALERTS 历史成对出现。

**遗留风险与建议**

按对象聚合后再使用，例如 PDB 指标用 `max by (namespace, poddisruptionbudget) (...)`，Deployment/Pod 级告警按 `namespace/deployment` 聚合。不要在未替代方案前降低 KSM 副本数。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/metric-availability.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/alerts.txt`

### CP4-05：drain 时间远大于 Pod 迁移时间（已定位；根因是 30 s grace period 与应用忽略 SIGTERM，未改配置）

**现象**

替代副本 2.515 秒就 Ready，但两次 drain 分别耗时 36.869 s 和 41.634 s。被驱逐的 Pod 对象在 `deletionTimestamp` 出现后整整 30 秒才消失：`8qrhf` 在 `10:21:55.485` 被驱逐，`deletionTimestamp = 02:22:25Z`（= 驱逐时间 + 30 s），Pod 在 `10:22:25` 才转为 Failed 并消失。

**影响范围**

- drain 必须串行等待每个 Pod 走完终止宽限期，维护窗口被显著拉长。
- drain 与"业务迁移"是两个不同指标，容易被误读为迁移很慢。

**根因**

业务 Pod 使用默认 `terminationGracePeriodSeconds = 30`，而 echoserver 镜像不响应 SIGTERM，容器会跑满整个宽限期。API server 把 `metadata.deletionTimestamp` 设为"对象将被删除的时刻"（驱逐时间 + 30 s），drain 必须等对象消失才继续。

**处理**

- 不修改 Deployment 配置（保持与 CP3 业务基线一致，便于对比）。
- 在 `pod-migration.txt` 与 `analysis.md` 中明确区分 `PodMigrationTime` 与 `DrainDuration`，并记录"压缩维护窗口的关键是应用优雅退出 + 合理的 grace period"。

**验证**

- `pod-migration.txt` 的 per-Pod 表格同时给出"驱逐观测时刻"和"kubelet 记录的 deletionTimestamp"。
- 原始 2 Hz 采样 `pod-migration-raw.txt` 可复算全部时间点。

**遗留风险与建议**

若要缩短维护窗口，需要业务支持优雅退出（SIGTERM/preStop）并下调 grace period；本阶段未改动被测业务，避免与既有基线产生不可比差异。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/pod-migration.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/pod-migration-raw.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/metrics.txt`

### CP4-06：软反亲和使 uncordon 后 3 个副本全部回到空节点（已记录；行为合法）

**现象**

uncordon 后执行 `rollout restart`，3 个业务副本**全部**落在 sre-worker-1（空节点，调度评分最高），而不是实验前的 2/1 分布。

**影响范围**

单节点承载全部副本，若此时 sre-worker-1 再次异常会同时失去 3 个副本。不过这是软约束下的正常结果，不是错误。

**根因**

`preferredDuringSchedulingIgnoredDuringExecution` 只提供偏好；空节点的反亲和加分最高，因此新 Pod 全部倾向它。

**处理**

- 不做人工 patch，只删除 sre-worker-1 上的一个副本，让替代 Pod 按偏好落到 sre-worker-2。
- 最终恢复为"每个 worker 至少一个副本"。

**验证**

- 最终分布 `2 -> sre-worker-1`、`1 -> sre-worker-2`。
- EndpointSlice 含 3 个 Ready 端点：`192.168.33.153`、`192.168.33.155`、`192.168.55.151`。

**遗留风险与建议**

软反亲和不能保证均衡；如需稳定分布，应使用 `topologySpreadConstraint`（`maxSkew`）或 descheduler（本阶段未执行）。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/recovery.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/resources-after.txt`

### CP4-07：PDB 阻塞负向测试若使用全量 drain 会连带驱逐其他 namespace 的 Pod（已用 `--pod-selector` 限定范围）

**现象**

`kubectl drain` 是节点级操作，没有 namespace 过滤。第一次正常 drain 已经把 monitoring、sre-baseline、calico-system 的 Pod 从 sre-worker-1 迁走过一次；如果在负向测试中再次执行全量 drain，会先把这些**未被 PDB 覆盖**的 Pod 再驱逐一遍，制造第二次无谓抖动。

**影响范围**

- 负向测试的结论会被"其他 Pod 被迁移"的噪声掩盖。
- `baseline-http` 会再次进入 Pending（required anti-affinity），重复产生 CP4-03 的现象。
- 真实生产中如果 PDB 阻塞全量 drain，节点会停在"部分排空"状态，这是需要写进 runbook 的风险。

**根因**

drain 会对节点上所有可迁移 Pod 发起 eviction；PDB 只约束被它覆盖的 Pod，未覆盖的 Pod 会被正常驱逐。

**处理**

- 负向测试改用 `kubectl drain sre-worker-1 --pod-selector=app=maintenance-http --ignore-daemonsets --delete-emptydir-data`，把 drain 范围限定到被测业务。
- 该参数只**缩小 drain 考虑范围**，不削弱 PDB 强制力；全程未使用 `--force`、`--disable-eviction`，未强删 Pod，未临时删除 PDB。
- 第一次正常 drain 仍使用全量范围，以便验证 DaemonSet 行为。

**验证**

- `drain-pdb-blocked.txt`：3 个 Pod 的 eviction 全部被拒，45 秒未完成（exit 124），阻塞前后 Pod 名单完全一致。
- 直接调用 Eviction API 返回 `TooManyRequests: Cannot evict pod as it would violate the pod's disruption budget`。
- 该窗口内业务探针 0 失败（75 个采样）。

**遗留风险与建议**

把"PDB 阻塞的 drain 可能让节点处于部分排空状态"写入维护 runbook；执行前先确认节点上还有哪些未被 PDB 覆盖的 Pod。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/drain-pdb-blocked.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/pdb-blocking.yaml`

### CP4-08：实验集群定位与本地工具链限制（已解决）

**现象**

- 本地工作目录 `~/Desktop/k8s-ops-platform` 当前 kubeconfig 指向 kind 集群 `kind-k8s-ops`（节点 `k8s-ops-control-plane/worker/worker2`，CNI 为 kindnet，无 monitoring namespace）。直接用本地 kubectl 会把实验做到错误的集群上。
- 命令沙箱（bwrap）在本机无法初始化，非提权命令直接失败；文件编辑工具也无法写入 `/tmp`，只能写入工作区。

**影响范围**

仅影响执行环境，不影响实验结论；但若误判集群，会得出完全错误的基线。

**根因**

- 实验集群是腾讯云三节点集群，通过 SSH 访问，不在本地 kubeconfig 中：`~/.ssh/config` 中定义 `sre-master`（124.222.30.15）、`sre-worker-1`（124.223.70.213）、`sre-worker-2`（111.229.38.106）。
- 本地沙箱能力不足，需要提权执行；编辑工具对 `/tmp` 的读写受限。

**处理**

- 通过 `~/.ssh/config` 确认目标主机，所有集群操作统一在 `sre-master` 上执行（`ssh sre-master ...`），节点级检查通过 SSH 直达三台主机。
- 所有实验数据写入 `/opt/sre-lab/experiments/04-node-maintenance/`，本机仅作临时暂存，任务结束后已清理，工作区无残留。

**验证**

- `ssh sre-master 'kubectl get nodes'` 返回 `sre-master / sre-worker-1 / sre-worker-2` 三节点 Ready，Calico 与 monitoring 存在，与实验基线一致。
- 工作区已确认无 `.cp4-stage` 等临时目录残留。

**遗留风险与建议**

在执行任何集群实验前，先确认目标 context/主机，避免把 kind 开发集群当成 SRE 实验集群。

**证据**

- `/opt/sre-lab/experiments/04-node-maintenance/baseline-before.txt`
- `/opt/sre-lab/experiments/04-node-maintenance/resources-after.txt`

### Checkpoint 4 最终验收结果

- cordon 成功：通过（`Ready,SchedulingDisabled`，taint 添加，CordonLatency 0.243 s）
- cordon 后已有 Pod 不被立即驱逐：通过（20 s 后 Pod 仍 Running，RESTARTS 不变，Deployment 3/3）
- 新 Pod 不调度到被 cordon 节点：通过（测试 Pod 落到 sre-worker-2，验证后删除）
- 正常 PDB 下 drain 成功：通过（exit 0）
- 业务 Pod 成功迁移：通过（3/3 副本迁出 sre-worker-1）
- DaemonSet 行为符合预期：通过（4 个 DaemonSet Pod 保留在 worker-1，AGE 未变）
- Service 连续性有真实探针数据：通过（529/529，成功率 100%，Downtime 0 s）
- 严格 PDB 成功阻止不安全 drain：通过（eviction 全被拒，exit 124，Eviction API 返回 TooManyRequests）
- 未使用 `--disable-eviction` 绕过 PDB：通过（`--force` / `--disable-eviction` 全程未使用）
- 恢复正常 PDB 后完成维护：通过（第二次 drain exit 0）
- uncordon 成功：通过
- worker-1 恢复 SchedulingEnabled：通过（`spec.unschedulable=false`，taints 为空）
- 三节点最终 Ready：通过（3/3）
- Calico Healthy：通过（`Available=True Progressing=False Degraded=False`）
- CoreDNS Healthy：通过（2/2）
- monitoring Healthy：通过（9/9 Pod Ready）
- Prometheus targets 最终全部 UP：通过（21/21）
- Alertmanager 无遗留活动告警：通过（0 active）
- systemd failed units = 0：通过（三台均为 0）
- 无长期 Pending / Terminating / CrashLoopBackOff：通过（0）
- 新增未解决项：Calico typha 因 cordon 自动缩容（CP4-01）；双 KSM 重复序列（CP4-04）

### Checkpoint 4 证据索引

- 服务器证据目录：`/opt/sre-lab/experiments/04-node-maintenance/`
- 实验前基线：`baseline-before.txt`、`workload-before.yaml`
- PDB 配置与观测：`pdb-normal.yaml`、`pdb-blocking.yaml`
- cordon：`cordon.txt`
- drain：`drain-success.txt`、`drain-pdb-blocked.txt`
- Pod 迁移：`pod-migration.txt`、`pod-migration-raw.txt`
- 业务连续性：`service-probe.log`、`service-probe-summary.txt`
- 事件与告警：`events.txt`、`alerts.txt`、`prometheus-alert-state.txt`、`alertmanager-state.txt`
- 恢复与最终状态：`uncordon.txt`、`recovery.txt`、`resources-after.txt`
- 指标与时间线：`metrics.txt`、`timeline-marks.txt`
- Calico typha 发现：`finding-calico-typha-autoscaler.txt`
- 分析：`analysis.md`、`interview-notes.md`
- 验收：`checkpoint4-validation.yaml`

## Checkpoint 5：k8s-ops-platform Agent 集群接入与真实诊断验证

本阶段把项目**按当前真实代码**用 Helm 部署到三节点集群的独立命名空间 `k8s-ops`，并用一个低风险、可恢复的真实异常验证「Agent 能否自己发现并输出诊断」。自动执行全程关闭（`--auto-pod-restart=false`、`--auto-deployment-restart=false`），只验证「发现 + 诊断」。

### 关键时间线

全部时间为 UTC，完整版见 `/opt/sre-lab/experiments/05-agent-integration/timeline.md`。

| 时间 | 事件 |
|---|---|
| 02:45:24 | 部署前基线：Prometheus 21/21 UP、0 活动告警、三节点 Ready、systemd failed 0 → 允许部署 |
| 02:46:52 | Helm 首次安装（未修改代码，镜像 `checkpoint5-014df3e24d48`），Agent/Controller 1/1 Ready |
| 02:47:41 | ServiceMonitor 创建；同时暴露 CP5-02（`/healthz` 404）与 CP5-05（未被 Prometheus 纳入） |
| 02:48:54 | 触发再同步，Prometheus 出现 `k8s-ops` job → 23/23 UP |
| 02:50–02:54 | 实验负载经历 CP5-08（runAsNonRoot 冲突 → 滚动死锁）；修正清单后 2/2 Ready |
| 02:55:25.009 | **T0（基线实验）**：注入 readiness 探针端口错误 |
| 02:55:28 → 02:57:51 | Deployment 降级 → Prometheus 指标约 13 s 变化 → 告警约 2 min 27 s 后 firing |
| 02:55–03:00 | **未修改代码的 Agent 对该故障零输出** → 定位为 CP5-04 |
| 03:00:08.832 | T_recover；03:01:19.509 T_healthy（RecoveryTime 70.7 s） |
| 03:02–03:05 | 实施 CP5-01/02/03/04 修复，重建镜像 → `checkpoint5-59a643c1ed82` |
| 03:05:53.437 | **T0（最终实验）**：同一故障重跑 |
| 03:05:53.567 | Agent 输出 `DeploymentReplicasUnavailable` 结构化诊断 |
| 03:08:53.470 → 03:10:04.128 | 恢复（RecoveryTime 70.7 s），`k8s_ops_active_diagnosis` 归零 |
| 03:10:13.138549 | **T0（延迟复测）**；03:10:13.271 状态跳变 → 03:10:13.267 Agent 输出（时钟修正后 ≈0.13 s） |
| 03:11:35 | 终态健康检查全部通过（23/23 UP、0 告警、0 异常 Pod） |

### 问题总览

| ID | 问题 | 根因 | 处理结果 |
|---|---|---|---|
| CP5-01 | 项目 ServiceMonitor 无法被集群 Prometheus 选中 | kube-prometheus-stack 的 `serviceMonitorSelector` 为 `release=monitoring`，项目清单既无该标签、也不在 Helm chart 内 | 在 chart 增加默认关闭的可选 ServiceMonitor 模板，用 `--set` 开启 |
| CP5-02 | Controller `/healthz` 返回 404 | 只设置了 `HealthProbeBindAddress`，未注册任何健康检查 | 注册 `AddHealthzCheck`/`AddReadyzCheck`，现为 200 |
| CP5-03 | 没有任何接口能证明「状态已进入 Agent」 | 项目无状态导出接口；且 `k8s_ops_*` 均为带标签的 Vec，无异常时零序列 | 新增 `GET /state` 与 `k8s_ops_informer_cache_objects` 指标 |
| CP5-04 | Agent 无法识别 DeploymentReplicasUnavailable | 只有 node/pod detector；pod 规则不含「Running 但 Ready=False」；deployments informer 被创建却无 handler 消费 | 新增 `internal/diagnosis/deployment` 检测器并接入 informer |
| CP5-05 | ServiceMonitor 创建后 Prometheus 未立即纳入 | 创建时机与控制器的 reconcile 竞争，未产生后续事件 | 对自有 ServiceMonitor 触发一次再同步；未改动监控栈 |
| CP5-06 | 无法使用 git short SHA 作为镜像 tag | 本地项目目录不是 git 仓库（无 `.git`） | 改用源码内容哈希 `checkpoint5-<hash>`，并记录 OCI digest |
| CP5-07 | 部署到 `k8s-ops` 后平台失去「自我排除」保护 | `cmd/agent/main.go` 中 `ExcludedNamespaces` 硬编码 `k8s-ops-platform`、`kube-system` | **未修复**（非本阶段阻塞项），已记录，见下方建议 |
| CP5-08 | 实验负载无法启动 / 滚动更新死锁 | 实验清单设了 `runAsNonRoot` 而 `echoserver:1.10` 以 root 运行；required anti-affinity 叠加 `maxUnavailable=0` 使替换 Pod 无处可调度 | 修正实验清单（去 `runAsNonRoot`，改 `maxSurge=0/maxUnavailable=1`） |
| CP5-09 | 用 `/api/v2/alerts` 查活动告警返回 404 | 该 Prometheus 构建（v3.14.0-distroless）未提供 v2 alerts 端点；`/api/v1/*` 正常 | 改用 `/api/v1/alerts`，并在采样脚本中固定该接口 |
| CP5-10 | 同一条告警在 Prometheus 中出现两个序列 | KSM 双副本各自抓取同一对象，label 集不同（与 CP3.5-03、CP4-04 同源） | 复现既有问题，本阶段不修改监控栈；统计告警数时按 alertname 去重 |

### CP5-01：项目 ServiceMonitor 与集群 Prometheus 选择器不匹配（已解决）

**现象**

把 `config/prometheus/monitors.yaml` 形式的最小 ServiceMonitor 应用到 `k8s-ops` 后，Prometheus targets 数量没有变化；`/api/v1/status/config` 中不存在 `k8s-ops` job。

**根因**

两个叠加问题：

1. 集群 Prometheus（kube-prometheus-stack 89.2.4）的 `spec.serviceMonitorSelector` 是 `matchLabels: {release: monitoring}`，项目清单没有这个标签。
2. 项目原本只在 `config/prometheus/monitors.yaml` 提供该清单，**不在 Helm chart 内**，也不被 `make deploy-yaml` 应用。直接 `kubectl apply` 会脱离 Helm 管理，形成配置漂移。

**处理**

按项目「优先用现有 chart、不手工 patch 造成漂移」的原则，在 `charts/k8s-ops-platform/templates/servicemonitor.yaml` 增加**默认关闭**（`observability.serviceMonitor.enabled: false`）的可选模板，标签可通过 values 覆盖：

```sh
--set observability.serviceMonitor.enabled=true \
--set observability.serviceMonitor.labels.release=monitoring
```

默认关闭保证没有 Prometheus Operator CRD 的集群不会因此安装失败。

**验证**

- ServiceMonitor 由 Helm 管理（`meta.helm.sh/release-name=k8s-ops-platform`）。
- Prometheus targets 由 21/21 变为 **23/23 UP**，`job=diagnosis-agent` 与 `job=action-controller` 均 `up=1`、`lastError=""`。
- 证据：`/opt/sre-lab/experiments/05-agent-integration/prometheus-target.txt`、`deployment-manifests/chart-servicemonitor.diff`

### CP5-02：Controller `/healthz` 返回 404（已解决）

**现象**

Controller 容器 Ready，但直接访问 `:8081/healthz` 返回 **404**。Deployment 的 readiness/liveness 使用 `tcpSocket`，因此端口可连就判定通过。

**根因**

`ctrl.NewManager(... HealthProbeBindAddress: ":8081")` 只是启动了探针 HTTP 服务；没有调用 `AddHealthzCheck`/`AddReadyzCheck` 时，该服务不注册任何路径。结果是「探针服务在监听」被误当成「组件健康」。

**处理**

在 `cmd/action-controller/main.go` 注册 `healthz.Ping`：

```go
mgr.AddHealthzCheck("healthz", healthz.Ping)
mgr.AddReadyzCheck("readyz", healthz.Ping)
```

**验证**

`/healthz` = 200、`/readyz` = 200、`/metrics` = 200。证据：`controller-startup.txt`。

**遗留建议（未在本阶段修改）**：chart 中的探针仍是 `tcpSocket`。端口可连不等于服务健康，后续应收紧为 `httpGet /healthz`。

### CP5-03：无法从 Agent 自身接口证明「状态已进入 Agent」（已解决）

**现象**

- Agent 的 `/metrics` 只有 `promhttp_*` 与 Go/进程指标，**没有任何 `k8s_ops_*` 序列**。
- 除 kubectl 外，没有任何方式能证明 Agent 真的拿到了 Node/Pod/Deployment/Event。

**根因**

`k8s_ops_diagnosis_total`、`k8s_ops_active_diagnosis` 都是带标签的 Vec，只有在某个标签组合被观测后才会产生序列。集群完全健康时 Agent 零输出，于是「Agent 正常但无异常」和「Agent 已经坏了」在监控上**不可区分**。

**处理**

1. 新增指标 `k8s_ops_informer_cache_objects{resource_kind}`（只有 4 个标签值，不引入基数问题），由 Agent 后台协程周期发布。
2. 新增 `GET /state`，直接读 SharedInformer 缓存，返回每类资源的对象数、`hasSynced` 以及资源名（每类最多 25 个，超出标记 `truncated`）。

**验证**

- `/state` 返回 `Node=3` 且名为 `sre-master`、`sre-worker-1`、`sre-worker-2`；`Pod=38`、`Deployment=12`、`Event=310`，全部 `hasSynced=true`。
- Prometheus 中可查询到真实值：`k8s_ops_informer_cache_objects{resource_kind="Node"}=3`。
- 证据：`agent-metrics.txt`、`prometheus-target.txt`

### CP5-04：Agent 无法识别 DeploymentReplicasUnavailable（已解决，本阶段核心发现）

**现象**

把一个 2 副本 Deployment 的新版本 readiness 探针端口改成无监听的 8081 后：

- Pod 变为 `Running` 但 `Ready=False`，`restartCount` 仍为 0；
- Deployment `availableReplicas=1 < spec.replicas=2`；
- kubelet 写入 `Warning Unhealthy: Readiness probe failed: ... 8081: connect: connection refused`；
- Prometheus 指标约 13 s 变化、告警 `DeploymentReplicasUnavailable` 约 2 min 27 s 后 firing；
- **Agent 对该故障零输出**（同一窗口内只输出了与本次故障无关的 Pod `FailedScheduling`，即滚动更新瞬间的 anti-affinity 噪声）。

**根因**

- `internal/diagnosis/` 只有 node 与 pod 两个 detector；pod 规则覆盖 `FailedScheduling / CrashLoopBackOff / ImagePullBackOff / OOMKilled`，**不包含 Running 但 `Ready=False`**。探针失败不会重启容器，因此完全不落在既有规则里。
- Agent 虽然构造了 deployments informer，但**没有注册任何 handler**，`status.availableReplicas` 从未被读取。

**处理**

按现有 `Detector.Diagnose → DiagnosisResult → Sink` 结构新增 `internal/diagnosis/deployment`，并在 `cmd/agent/main.go` 将 deployments informer 的 Add/Update/Delete 接入。检测器要点：

- 用 `status.observedGeneration >= metadata.generation` 防抖，避免每次 spec 变更都被判成故障；
- `availableReplicas >= spec.replicas` 时静默；
- `availableReplicas == 0 && desired > 0` 时升为 `critical`，否则 `warning`；
- **不输出 SuggestedAction**：readiness 配错不是靠重启能修的，错误的推荐比没有推荐更糟；这也保证即使有人打开自动执行开关也不会创建 Action。

**验证**

用**同一套操作**重跑故障后：

```
reason=DeploymentReplicasUnavailable severity=warning
namespace=sre-agent-test name=agent-probe-demo
summary="Deployment has 1/2 available replicas"
evidence: spec.replicas=2, status.availableReplicas=1, status.readyReplicas=1,
          status.unavailableReplicas=1, status.updatedReplicas=1,
          DeploymentCondition[Progressing]=True (reason=ReplicaSetUpdated)
```

AgentDetectionLatency ≈ 0.13 s（Deployment 状态在 master 时钟 03:10:13.271 跳变，Agent 在自身时钟 03:10:13.267 输出；worker-1 相对 master 实测慢 0.133 s，修正后约 03:10:13.400）。`k8s_ops_diagnosis_total{reason="DeploymentReplicasUnavailable"}` 可达 15；恢复后 `k8s_ops_active_diagnosis` 回到 0。

**边界（必须与上述结论一起讲）**：达到的是「Deployment 级异常识别 + 结构化证据」，**不是完整根因判定**。Agent 没有指出「是新版本探针端口配错」，因为根因文本在 kubelet Event 里，而当前 Detector 之间没有因果关联。单元测试覆盖：异常/严重度/健康静默/status 过期静默/spec.replicas 未设置。

证据：`diagnosis-during.txt`、`raw/diagnosis-during-baseline-image.txt`、`raw/latency-run/`、`internal/diagnosis/deployment/detector_test.go`

### CP5-05：ServiceMonitor 创建后 Prometheus 未立即纳入（已解决）

**现象**

Helm 升级创建 ServiceMonitor 后等待 50 s，Prometheus 仍为 21/21 targets，配置中没有 `k8s-ops` job；控制器日志显示该时刻只发生过一次 `sync prometheus`（时间与 ServiceMonitor 创建时刻重合）。

**根因**

创建时机与控制器 reconcile 竞争：控制器在 ServiceMonitor 落库前后完成了一次同步，之后没有产生新的触发事件。

**处理与验证**

对**自有**的 ServiceMonitor 打一个 annotation 触发重新同步后，Prometheus 配置立即出现 `k8s-ops` job，targets 变为 23/23。全程未修改 Prometheus、Alertmanager 或 monitoring 命名空间内的任何资源。

证据：`prometheus-target.txt`、`raw/targets-after-servicemonitor.json`

### CP5-06：本地项目不是 git 仓库（已记录）

**现象**

`git status` 失败：项目根目录没有 `.git`，因此无法提供 branch、HEAD 和 short SHA。

**影响**

本阶段要求的 `checkpoint5-<git-short-sha>` 镜像 tag 无法生成。

**处理**

改用源码内容哈希作为 tag（对 `*.go`、`Dockerfile*`、`go.mod`、`go.sum` 排序后取 sha256 前 12 位），并记录完整 OCI digest：

| 阶段 | tag | agent digest | controller digest |
|---|---|---|---|
| 未修改代码 | `checkpoint5-014df3e24d48` | `sha256:5637fb6f485e0ca451c24e78ede69985eb87dfc753a9fdfe240b3070f6cf8ddf` | `sha256:bdb42690a6a2fac04350520ca1c7356a184c5326f7dbacf2a8cdf0a6729985e9` |
| 终态 | `checkpoint5-59a643c1ed82` | `sha256:e15ff2a6154d7bcf7de5f102a9f07ee399a46db89b4c31959f19c5c337e40613` | `sha256:f6da477da4a9605a952da7f6c048c46b702cf75f0a25899025fdbce4da9ede7d` |

注：内容哈希 tag 只适用于本地实验；CI/CD 仍必须使用不可变的提交 SHA。**建议后续为该项目初始化 git 仓库**，否则「本地项目是唯一事实来源」无法通过版本控制保证。

### CP5-07：ExcludedNamespaces 硬编码导致部署到其他命名空间后失去自我排除（未修复，已知项）

**现象**

按本阶段要求部署到 `k8s-ops` 后，平台自身命名空间不在 Agent 的排除列表中。

**根因**

`cmd/agent/main.go` 中 `policy.Config.ExcludedNamespaces` 被写死为 `[]string{"k8s-ops-platform", "kube-system"}`，不接受配置；Helm 的 `namespace` 是可变值，两者会脱钩。

**影响与现状**

本阶段自动执行关闭（`--auto-pod-restart=false`、`--auto-deployment-restart=false`），Policy 只输出推荐、不创建 Action，因此**实际无影响**。但一旦开启自动执行，Agent 理论上可以对自己的 Pod 创建 Action，形成自我处置循环。

**建议修复（未在本次执行，避免超出「只修阻塞项」的范围）**

1. 把排除列表改为 flag（如 `--excluded-namespaces`），并在 chart 的 agent args 中以 `{{ .Values.namespace }}` 注入；
2. 或让 Agent 读取自身 namespace（Downward API）并始终加入排除列表。

### CP5-08：实验负载无法启动并出现滚动更新死锁（已解决，实验清单问题）

**现象**

1. 两个 Pod 长期 `CreateContainerConfigError`：`Error: container has runAsNonRoot and image will run as root`。
2. 修正后滚动更新卡住：新 Pod `Pending`，事件为 `0/3 nodes are available: ... 2 node(s) didn't match pod anti-affinity rules`。

**根因**

1. 实验清单照搬了平台工作负载的 `runAsNonRoot: true`，但 `registry.aliyuncs.com/google_containers/echoserver:1.10` 以 root 运行。
2. 清单同时使用 required `podAntiAffinity`（跨 hostname）与默认 `maxUnavailable: 0`（2 副本的 25% 向下取整为 0）。替换 Pod 需要在“没有同类 Pod 的节点”上调度，而旧 Pod 尚未被删除，形成死锁。

**处理**

实验清单去掉 `runAsNonRoot`（`echoserver` 为 root 镜像，且该隔离命名空间不承载业务），并显式设置 `maxSurge: 0 / maxUnavailable: 1`，使滚动更新可以推进并稳定复现 `availableReplicas < desiredReplicas`。

**验证**

负载恢复 2/2 Ready；注入探针错误后稳定进入 `ready=1 available=1 unavailable=1`；恢复配置后回到 2/2。

证据：`deployment-manifests/sre-agent-test.yaml`、`diagnosis-during.txt`

### CP5-09：Prometheus `/api/v2/alerts` 返回 404（已解决）

**现象**

预检脚本用 `curl http://127.0.0.1:19090/api/v2/alerts` 读取活动告警，返回 `404 page not found`；而 `/api/v1/targets`、`/api/v1/query`、`/api/v1/status/buildinfo` 全部正常。

**根因**

该集群的 Prometheus 为 `v3.14.0-distroless`（revision d7598b7141418fa35be2b5ec5d0fefb634199610）。这个构建不提供 `/api/v2/alerts` 端点，v1 接口才是可用的。不是网络、鉴权或 port-forward 问题——同一端口的 v1 路径可正常返回。

**处理**

改用 `/api/v1/alerts`（返回结构为 `data.alerts[]`），并把采样脚本里的接口固定下来，避免后续重复踩坑。

**验证**

同一时刻 `/api/v2/alerts` = 404、`/api/v1/alerts` 返回 `active_alert_count`，两者结论一致；Checkpoint 5 全程的告警判定都基于 v1 接口。

### CP5-10：同一条告警出现两个序列（复现既有问题，未修复）

**现象**

故障制造后，`DeploymentReplicasUnavailable` 在活动告警列表中同时以 `pending` / `firing` 出现两次，label 只有 `pod` 与 `instance` 不同（`monitoring-kube-state-metrics-74498898c-hrzzr` 与 `-dbnlg`）。

**根因**

与 CP3.5-03、CP4-04 同源：kube-state-metrics 为双副本，且两副本各自抓取同一批对象，因此同一条规则对同一对象产生两个序列。这是本实验环境在 Checkpoint 3.5 引入双 KSM 后的已知副作用，**不是 Checkpoint 5 引入的问题，也不是平台组件导致的**。

**处理**

本阶段不修改监控栈（明确约束），因此不修复。为避免这个重复影响结论，本次所有延迟与告警状态判定都**按 alertname 去重**，并在 `prometheus-target.txt`、`diagnosis-*.txt` 中保留原始双序列输出以便核对。

**影响评估**

只影响「同一条告警的可见次数」，不影响告警是否触发、触发时间与恢复时间；对本次 Delivery 结论无实质影响，但长期会让告警计数虚高。

**建议修复（未执行）**

恢复 KSM 单副本，或为 KSM 增加分片/去重（例如 `--metric-labels-allowlist` 配合单副本，或改用 `honor_labels` 处理），收敛后同步删除本项与 CP3.5-03、CP4-04 记录。

### Checkpoint 5 最终验收结果

- Agent 成功部署：通过（1/1 Ready，restartCount=0，`k8s-ops`）
- Controller 成功部署：通过（1/1 Ready，restartCount=0，Action CRD controller 已启动）
- 无 RBAC Forbidden：通过（运行日志无 403；`cluster-admin=no`、`secrets get=no`）
- Informer/List-Watch 正常：通过（cache sync 成功，`/state` 四类资源全部 `hasSynced=true`）
- Agent 能看到真实 Node：通过（`sre-master`、`sre-worker-1`、`sre-worker-2`）
- Agent 能看到真实 Pod：通过（38 个，含 monitoring / sre-baseline / k8s-ops / sre-agent-test）
- Agent 能看到真实 Deployment：通过（12 个，含 `sre-baseline/baseline-http`）
- Agent 能读取 Events：通过（310 条）
- Agent `/metrics` 正常：通过（HTTP 200，52 条序列）
- Prometheus 能 scrape Agent：通过（`up=1`、`lastError=""`）
- 低风险 Deployment 异常成功产生：通过（`sre-agent-test/agent-probe-demo`）
- Agent 实际观察到异常：通过（修复后，≈0.13 s）
- Agent 输出结构化异常结果：通过（`DeploymentReplicasUnavailable` + 6 条 evidence）
- 恢复后 Agent 状态收敛：通过（`k8s_ops_active_diagnosis=0`）
- 测试 Deployment 恢复 2/2 Ready：通过
- 三节点最终 Ready：通过（3/3）
- Calico Healthy：通过（`calico`/`ippools` Available=True、Degraded=False；`tiers` 的 `Degraded=True (Waiting for Tigera API server to be ready)` 为实验前既有状态，本次未修改 Calico）
- CoreDNS Healthy：通过（2/2）
- monitoring Healthy：通过（9/9 Pod Ready）
- Prometheus targets 最终全部 UP：通过（23/23，较基线 21/21 新增两个平台 target）
- 无长期异常 Pod：通过（Pending 0、非 Running/Completed 0、Terminating 0）
- Alertmanager 无遗留活动告警：通过（0 active）
- systemd failed units = 0：通过（master 0）
- 未验证项（明确不做）：Action 自动执行（本阶段显式关闭）

### Checkpoint 5 证据索引

- 服务器证据目录：`/opt/sre-lab/experiments/05-agent-integration/`
- 部署前基线：`preflight.txt`
- 权限：`rbac.txt`
- 部署清单与变更：`deployment-manifests/`（含 `helm-install.txt`、`helm-manifest-applied.yaml`、`chart-servicemonitor.diff`、`project-changes/`、`sre-agent-test.yaml`）
- 启动验证：`agent-startup.txt`、`controller-startup.txt`
- 指标接入：`agent-metrics.txt`、`prometheus-target.txt`
- 诊断实验（终态镜像）：`diagnosis-before.txt`、`diagnosis-during.txt`、`diagnosis-after.txt`
- 诊断实验（未修改代码的基线）：`raw/diagnosis-*-baseline-image.txt`
- 延迟复测：`raw/latency-run/`（Deployment 状态跳变 + Agent 日志 + T0）
- 事件流：`events.txt`
- 时间线与分析：`timeline.md`、`analysis.md`、`interview-notes.md`
- 终态健康：`final-health.txt`
- 验收：`checkpoint5-validation.yaml`
- 本文件快照（云端只读副本）：`TROUBLESHOOTING-snapshot.md`（本地 `TROUBLESHOOTING.md` 为唯一可编辑源）

## Checkpoint 6：CI/CD Failure Detection & Helm Rollback

### 现象与影响

V2 正常发布为 Helm revision 5，两个 Deployment rollout 成功。V3 revision 6 仅把 Agent readiness path 改为 `/checkpoint6-bad-readiness`；新 Agent Pod 为 `Running`、restartCount=0，但 kubelet 持续记录 `Readiness probe failed: HTTP probe failed with statuscode: 404`，Pod `Ready=False`。30 秒 rollout 观察超时，10 秒 smoke 观察失败于 “Agent Deployment is not Ready”。旧 V2 Agent 继续为 Service 提供 endpoint，所以 V3 窗口业务探针 251/251 成功。

### 根因与诊断

Helm values 中的错误 path 与进程真实提供的 `/healthz` 不一致。Readiness 失败不会杀死容器（liveness 仍访问正确路径），只会阻止该 Pod 成为可服务 endpoint，并让 RollingUpdate 保留旧副本。权威失败时间来自 Agent Pod 的首条 Unhealthy Event，而不是 Pod 初始的 Ready=False condition。

实测：ReadinessFailureLatency `2.351479336s`；RolloutFailureLatency `31.875021933s`；SmokeFailureLatency `42.020416028s`；PrometheusDetectionLatency `9.365916780s`。Prometheus 看到 `kube_pod_status_ready{condition="true"}=0`；既有告警没有进入 pending/firing，Alertmanager 保持 0 active。

Agent 没有输出 DeploymentReplicasUnavailable，AgentDetectionLatency 为 N/A。默认 RollingUpdate 让旧副本保持 `availableReplicas=1`，与 `spec.replicas=1` 相等，因此 Deployment detector 的规则条件从未满足；新 Agent 本身仍完成 informer sync。恢复后 `k8s_ops_active_diagnosis{reason="DeploymentReplicasUnavailable"}=0`。

### 修复、回滚与恢复验证

现场保存后调用现有 `scripts/rollback.sh k8s-ops-platform k8s-ops 5`，没有 uninstall、delete Deployment 或 reinstall。Helm revision 7 记录 `Rollback to 5`，7.559499574s 内两个 Deployment rollout 成功。最终 smoke 验证 Agent/Controller Ready、CRD discovery、RBAC、真实 Action、Agent health/metrics/state 和 Controller healthz/readyz；Prometheus 23/23 UP，Alertmanager 0 active，3 Nodes Ready，三台 failed units 均为 0。

`TotalRecoveryTime=253.682473856s` 以坏发布开始到最终 smoke PASS 计算，包含保存现场和修正 smoke harness 假阴性的时间。全实验 HTTP 探针成功率 99.908257%（1089/1090）；唯一一次失败发生在 V2 rollout，单请求超时 2.002049s。精确 ServiceDowntime 在 1 秒采样下不可得，成功样本给出的中断窗口上界为 4.023139s；V3 窗口 ServiceDowntime 为 0 个失败样本。

### 实验中发现的测试基础设施问题

1. 本机没有 Helm CLI：第一次组合 CI 的 `helm lint` 真实失败。最终在 sre-master 用 Helm 4.3.0 对从本地复制的完全相同 chart 执行 lint；本机 `make ci` 与远端 lint 总耗时 6.360710998s。Remote GitHub Actions 未验证。
2. smoke workload 使用 `registry.k8s.io/pause:3.10`，节点无缓存且访问 registry 超时，造成 ErrImagePull 假失败。改为节点已有且与集群 sandbox 版本一致的 `pause:3.10.1`。
3. 回滚复用启动超过 5 分钟的健康 V2 Pod，smoke 的 `logs --since=5m` 查不到启动标记而误报。改为检查当前 Deployment Pod 的完整启动日志。
4. cleanup 混写资源类型导致临时 Action/Deployment 未删除，且错误被 `|| true` 隐藏。改用 `action/cicd-smoke deployment/cicd-smoke` 显式资源引用，并验证最终无残留。
5. 初版证据脚本按共享 tag 子串误选 Controller Pod；权威时间改用 Agent label 与首条 Unhealthy Event，错误记录保留并追加 correction，未篡改现场。

### 已知限制与证据路径

本项目仍是单 control-plane、Alertmanager 单副本、无 Loki；KSM 双副本仍会产生重复 series；Agent 不是完整 RCA；自动 Action 未开启；GitHub/GHCR 未验证，当前闭环不等同于生产发布平台。证据目录：`/opt/sre-lab/experiments/06-cicd/`，其中 `v3-bad-release.txt`、`rollout-failure.txt`、`smoke-failure.txt`、`agent-observation.txt`、`prometheus-observation.txt`、`rollback.txt`、`service-probe.log` 和 `final-health.txt` 为核心原始证据。
