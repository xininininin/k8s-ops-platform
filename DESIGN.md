# K8s Ops Platform V1 设计与验证记录

> 本文档是项目唯一的设计、实验、进度与变更记录。测试结论只记录实际执行结果。

## 1. 项目背景与问题

Kubernetes 故障发现、处置和恢复确认经常分散在告警、人工命令和脚本中。项目将只读诊断与有副作用的执行隔离，并以可恢复的声明式状态机闭环操作。

## 2. 项目目标

构建 `API Server → Diagnosis Agent → DiagnosisResult → Policy → Action CRD → Action Controller → Execute → Verify` 的 V1，支持 Node/Pod 诊断、三类 Action、可观测性和真实 Kind E2E。

## 3. 总体架构

Agent 通过 SharedInformer 本地缓存诊断并输出日志/指标；Policy 只为允许的风险级别创建 Action。Controller 根据持久化 Status 执行和验证，重启不依赖进程内任务状态。

## 4. 组件职责

- Diagnosis Agent：只读 Watch、诊断、推荐、按策略创建 Action。
- Action Controller：执行、验证、重试、超时和审计。
- Prometheus/Grafana/OTel Collector：指标抓取、展示与结构化日志采集。

## 5. Diagnosis Agent 设计

使用一个 `SharedInformerFactory` 建立 Node、Pod、Deployment、Event 的 List-Watch 与本地缓存。Pod/Node add、update 触发诊断；新 Event 会按 involvedObject 从 Pod lister 缓存重新诊断。10 分钟 resync 只是安全重放，不是高频 API 轮询。

Deployment add、update 同样触发诊断：`internal/diagnosis/deployment` 比较 `status.availableReplicas` 与 `spec.replicas`，并用 `status.observedGeneration >= metadata.generation` 防抖，输出 `DeploymentReplicasUnavailable`。该检测器刻意不输出 SuggestedAction，因为 readiness 类故障不是靠重启能修的。

Agent 另外暴露 `GET /state`，直接读 Informer 缓存返回每类资源的对象数、`hasSynced` 和资源名（每类最多 25 个）。这是「状态确实已进入 Agent」的可验证证据，不需要 kubectl 旁路。

## 6. DiagnosisResult 模型

`DiagnosisResult` 是内部 Go 对象，Evidence 由 source、field、value、message、timestamp 组成。结果写入 JSON 结构化日志，并通过 Sink 接口扇出到后续指标和 Policy，不写 CRD。

## 7. Action CRD 设计

`ops.example.io/v1alpha1`、Namespaced `Action`。Spec 保存动作、目标、原因、超时和来源；Status 保存阶段、时间、generation、重试与 Conditions。仅需执行闭环的操作持久化。

## 8. Action Controller 状态机

Reconcile 驱动 `Pending → Running → Verifying → Succeeded`，不可恢复错误或重试/超时耗尽进入 Failed。终态直接返回。每次副作用后必须进入 Verify；Status 是唯一任务进度来源。

## 9. PodRestart

执行前把原 Pod UID 和 controller owner 写入 Status；删除仅在当前 Pod UID 与检查点一致时发生。验证要求出现同一 owner 控制、UID 不同且 Ready 的新 Pod。

## 10. DeploymentRestart

以 Action UID 作为确定性 pod-template annotation patch 值，重复 Execute 不产生新 rollout。验证同时要求 observedGeneration、updatedReplicas 和 availableReplicas 达到期望。

## 11. NodeMaintenance

流程检查点为 `PreChecked → Cordoned → Drained → HookCompleted → HealthChecked → Uncordoned`。drain 使用 policy/v1 Eviction（PDB 拒绝会保留等待），跳过 DaemonSet、mirror 和终态 Pod，拒绝驱逐无 controller 的 Pod。默认 Hook 是显式 no-op 且接口要求实现保持幂等。

## 12. Policy 与风险分级

PodRestart 为低风险，可通过 `--auto-pod-restart` 开启自动创建（默认关闭，避免持续性 CrashLoop 造成重启风暴）；DeploymentRestart 为中风险，默认仅输出推荐，可通过 `--auto-deployment-restart` 开启；NodeMaintenance 为高风险，Policy 无论配置都不自动创建。自动 Action 使用目标哈希生成确定名称，重复 Create 的 AlreadyExists 视为已去重；`kube-system` 和平台自身命名空间不进入自动修复。

## 13. RBAC 设计

Agent 与 Controller 使用不同 ServiceAccount/ClusterRole。Agent 对 Node/Pod/Deployment/Event 只有 get/list/watch，对 Action 只有 create；没有 Pod delete、Deployment/Node patch 或 eviction。Controller 可 watch Action/update status，并仅取得三种执行器所需的 delete Pod、patch Deployment/Node、create pods/eviction。隔离原因是诊断输入属于广泛只读面，而副作用权限必须只进入持久化、可审计、可恢复的 Reconcile 闭环；未授予 cluster-admin，也不读取 Secret。

## 14. Prometheus 指标设计

提供 `k8s_ops_diagnosis_total`、`k8s_ops_active_diagnosis`、`k8s_ops_action_total`、`k8s_ops_action_failed_total`、`k8s_ops_action_duration_seconds`。标签限制为资源类型、reason、severity、action type 和 result，不包含名称/UID。Agent 自维护资源到异常集合以正确增减 active gauge；两个组件均在 8080 暴露 `/metrics`。

Agent 额外提供 `k8s_ops_informer_cache_objects{resource_kind}`，反映本地缓存中的对象数。之所以需要它：其余五项都是带标签的 CounterVec/GaugeVec，在某个标签组合被观测前不产生序列，于是「Agent 正常且无异常」与「Agent 已经失效」在监控上不可区分。该指标只有 4 个标签值，不引入基数问题。

Helm chart 提供**默认关闭**的可选 ServiceMonitor（`observability.serviceMonitor.enabled`），因为 `monitoring.coreos.com` CRD 并非所有集群都有；标签通过 `observability.serviceMonitor.labels` 覆盖，以适配不同 Prometheus Operator 的 `serviceMonitorSelector`。

## 15. OpenTelemetry 日志链路

Agent/Controller 使用 stdout JSON 结构化日志且字段白名单不含 Secret/Token。Collector Contrib DaemonSet 以 filelog 只读采集这两个 workload 的容器日志，经 memory_limiter/batch 后输出 debug exporter；后续可只替换 exporter 接入日志后端。

## 16. Grafana Dashboard

Grafana provisioning ConfigMap 包含当前异常数、按 Reason 分类、Action 总数、失败数和 p95 执行耗时五个面板。

## 17. 幂等与 Controller 重启恢复

Controller 不保存进程内执行进度。Pod UID/owner、NodeMaintenance 分步检查点及 phase 都写入 Action Status；Deployment patch 值由 Action UID 决定。因而 Running/Verifying 状态重放时会基于实际资源继续。Phase 9 已真实在 PodRestart 的 Verifying 阶段将 Controller 缩容至 0，再恢复为 1；重启后最终 Succeeded。

## 18. E2E 测试场景与实际结果

执行环境：Docker 29.6.2、Kind 0.32.0、Kubernetes v1.36.1，三节点（1 control-plane、2 worker）。最终集群保留运行，便于复查；可执行 `make kind-down` 删除。

最终命令 `./scripts/e2e.sh k8s-ops` 返回 exit 0，输出 `ALL 4 E2E CASES PASSED`：

1. CrashLoop：创建故障 Deployment；Agent JSON 日志出现 CrashLoopBackOff；端口转发后的 `/metrics` 出现对应 `k8s_ops_diagnosis_total`，通过。
2. PodRestart：Action Watch 实际观察到 Pending、Running、Verifying、Succeeded；替换 Pod UID 与原 UID 不同且新 Pod Ready，通过。
3. NodeMaintenance：Kind worker 完成 cordon、Eviction drain、no-op Hook、Ready health check、uncordon；最终 `executionState=Uncordoned`、Node schedulable，通过。
4. 重启恢复：PodRestart 进入 Verifying 后将 action-controller Deployment 缩容到 0，确认 Pod 删除，再扩容到 1；新 Controller 从 Status/集群状态继续并最终 Succeeded，通过。

真实失败与修复记录：

- 首次运行：controller-runtime 与入口重复注册 `kubeconfig` flag，Controller panic；改为项目专用 `--kubeconfig-path`。
- 再部署同名 `:dev` 镜像时旧 Pod 未更新；`deploy-kind.sh` 增加显式 rollout restart。
- E2E 严格模式下 `grep -q` 令管道上游 SIGPIPE 返回 141；改为完整读取匹配。
- 一次 Case 2 脚本从 PodList 第一项取到 Terminating 的旧 Pod，误报 UID 未变化；改为断言列表中存在不同 UID，并增加四阶段 Watch 验证。

可观测性实际验证：OTel Collector 初次因 memory_limiter 缺少 `check_interval` 启动失败；补为 1s 后 DaemonSet 2/2 Ready，debug exporter 真实输出 Agent 的 ImagePullBackOff DiagnosisResult 与 Controller 的 action_audit。Controller 自定义指标初次因 controller-runtime 使用独立 Registry 而未出现在端点；显式注册后，真实查询得到 DeploymentRestart succeeded 的 action total/时长 histogram，以及故意失败 Action 对应的 `k8s_ops_action_failed_total=1`。

## 19. 当前已完成能力

- Phase 1：项目目录、Go module、Action API 类型、CRD schema、基础 Makefile 和目录边界。
- Phase 2：SharedInformer Agent、四类资源缓存、结构化日志 Sink 与 DiagnosisResult。
- Phase 3：NotReady、MemoryPressure、DiskPressure、FailedScheduling、CrashLoopBackOff、ImagePullBackOff、OOMKilled 组合规则与单测。
- Phase 4：Action Reconciler、超时/重试、状态条件与终态幂等。
- Phase 5：PodRestart、DeploymentRestart 的 Execute/Verify 和验证单测。
- Phase 6：NodeMaintenance 分阶段检查点、Eviction/PDB、可插拔 Hook、健康检查与 uncordon。
- Phase 7：风险 Policy、Action 去重、两套最小权限 RBAC 和结构化操作审计。
- Phase 8：五项 Prometheus 指标、两个 metrics 端点、ServiceMonitor/抓取配置、OTel Collector 与 Grafana Dashboard。
- Phase 9：三节点 Kind 自动化、四个 E2E 场景、Controller 重启恢复和 OTel 实际链路。
- Phase 10：全量 gofmt/test/vet/build、双镜像构建、清单修复与最终设计记录。
- Checkpoint 5（真实三节点集群）：项目按当前代码用 Helm 部署到 `k8s-ops`，Agent/Controller 1/1 Ready；Prometheus 接入后 23/23 targets UP；用 readiness 探针配置错误稳定复现 `DeploymentReplicasUnavailable`，修复后 Agent 约 0.13s 输出结构化诊断，Prometheus 告警约 2m27s。基线（未修改代码）Agent 对该故障零输出，这一负面结果同样记录在案。

## 20. 已知限制

- Grafana Dashboard ConfigMap 已由 API Server 接受，但本 Kind 集群未安装 Grafana，UI 展示未验证。
- Prometheus 抓取配置、ServiceMonitor 和两个真实 `/metrics` 端点已提供；本 Kind 集群未安装 Prometheus Operator/Prometheus Server，因此持续抓取与告警未验证。
- NodeMaintenance E2E 验证了 Kind worker 的完整路径，但未制造 PDB 拒绝；实现使用原生 Eviction API，真实 PDB 阻塞行为尚未单独验证。
- MaintenanceHook V1 为 no-op；真实内核、kubelet 或主机维护必须实现新的幂等 Hook。
- MemoryPressure、OOMKilled 等难以安全注入的故障由单元测试覆盖，未在 Kind 中真实施压复现。
- Agent/Controller 当前各部署单副本，未启用 leader election；Action 的持久化和幂等不依赖单进程内存，但多副本生产部署仍需增加选主配置。
- 诊断规则为无状态纯函数，没有 `for`/抑制/去重语义。Deployment 检测器只靠 `observedGeneration` 防抖；Pod 侧「Running 但 Ready=False」尚无规则，因为缺少时间窗时会在每次正常滚动更新误报（Checkpoint 5 刻意未加）。
- Detector 之间没有因果关联：Deployment 副本不足与导致它的 Pod 探针失败，在输出中是两条独立事实，Agent 不会指出「是新版本探针配置错误」。
- `ExcludedNamespaces` 在 `cmd/agent/main.go` 中硬编码为 `k8s-ops-platform`、`kube-system`，与 Helm 的 `namespace` 取值会脱钩。部署到 `k8s-ops` 时平台自身命名空间不再被排除；当前自动执行默认关闭所以无实际影响，开启自动执行前必须先改为可配置。
- Controller 已注册 `/healthz`、`/readyz`（此前返回 404），但 chart 中的探针仍为 `tcpSocket`；端口可连不等于服务健康，应收紧为 `httpGet`。
- DiagnosisResult 只写结构化日志与指标，不落 CRD，因此没有历史诊断查询接口；Event 默认仅保留 1 小时，长时间故障的早期事件会过期。

## 21. 后续可扩展方向

- 外部日志后端、审批集成、更多诊断插件和 MaintenanceHook 实现。
- Prometheus/Grafana 完整栈验收、PDB 拒绝/恢复 E2E、controller-runtime leader election 与 admission 校验。

## 22. 变更记录

- 2026-08-31：Phase 1 初始化；创建 API 模型、CRD、Makefile、README 与本设计文档。
- 2026-09-01：Phase 2-3 完成 Agent、诊断规则和单元测试。
- 2026-09-01：Phase 4-6 完成 Action 状态机及三类执行器。
- 2026-09-01：Phase 7-8 完成权限隔离、策略、审计和可观测性。
- 2026-09-01：Phase 9 完成真实 Kind 四场景 E2E、Controller 重启恢复和 OTel debug exporter 验证。
- 2026-09-01：Phase 10 完成全量质量门禁、双镜像构建、指标 Registry 修复和 V1 收口。
- 2026-09-14：Checkpoint 5 在真实三节点集群完成部署与诊断验证。新增 `internal/diagnosis/deployment` 检测器（DeploymentReplicasUnavailable）并接入 deployments informer；新增 `k8s_ops_informer_cache_objects` 指标与 `GET /state` 状态导出；修复 Controller `/healthz` 未注册检查导致的 404；chart 新增默认关闭的可选 ServiceMonitor。实测：Agent 检测延迟约 0.13s、Prometheus 指标约 15.6s、Prometheus 告警约 2m27s、恢复 40.5–70.7s。未修改代码的基线镜像对该故障零输出，作为能力边界保留。

## 23. CI/CD 与发布安全

CI/CD 以 Makefile 作为本地与 GitHub Actions 的共同契约。YAML 只负责编排事件、权限、凭据和步骤顺序，格式化检查、测试、构建、部署、Smoke Test、E2E 与回滚都通过 `make` 入口执行，因此开发者可以在提交前复现同一组命令，脚本修复也不会在多份 YAML 中漂移。项目没有既有 lint 工具，所以本阶段不为展示技术栈额外引入 lint 框架；CI 使用 `gofmt`、`go vet`、单测和编译作为质量门禁。

CI 只验证源代码，不需要集群凭据。Release 重新执行基础门禁，构建 Agent/Controller 两个非 root distroless 镜像并推送 GHCR。CD 消费 Release 成功后的 `sha-<12位提交SHA>` 镜像，在 Kind 中完成 Helm 部署、两个 Deployment rollout、Smoke Test 和完整 E2E。CI 与 Release 分开可以清晰区分“代码可合并”和“制品已产生”，CD 只验证已经发布的制品，避免测试镜像与最终镜像不一致。

SHA tag 是发布与回滚的主键：它不可变、能直接映射源码提交，也能写入变更记录。`latest` 只为体验入口，Git tag 是面向人的版本别名，两者都不能替代 SHA。GHCR 使用最小化的 `GITHUB_TOKEN` packages 权限；真实集群的 `KUBECONFIG` 只存 GitHub `production` Environment Secret。Harbor 通过 `IMAGE_PREFIX` 保留兼容边界，未来接入时使用 `HARBOR_USERNAME`/`HARBOR_PASSWORD` Secrets，但当前未进行 Harbor 联调。

PR 不具备 production 部署路径。生产任务仅允许手工触发、引用已经发布的 tag，并绑定需要管理员在 GitHub 仓库设置中开启 required reviewers 的 `production` Environment。审批属于 GitHub 外部保护规则，不能靠 workflow 文件自行伪造。目标集群还应使用专用、最小权限 kubeconfig，并自行配置私有仓库 imagePullSecret。

`kubectl apply` 或 Helm 返回成功只代表资源被 API Server 接受。发布验证还要求 Agent/Controller Ready、CRD discovery、ServiceAccount/RBAC、一个真实 DeploymentRestart Action 闭环、健康/metrics 端点以及 Kind 的四场景 E2E。失败时 `scripts/rollback.sh` 使用部署前记录的 Helm revision 回滚并再次等待两个 Deployment；如果是没有历史版本的首次安装，只能卸载失败 release，不能伪称恢复到旧版本。自动回滚默认用于 Kind 演示环境；生产回滚虽然提供受保护任务内的失败处理，但仍受集群状态、外部依赖和数据兼容性边界约束。

Kind CD 是短生命周期、可重复创建的发布验收，验证 Kubernetes API 行为和控制器闭环，但不代表云厂商网络、存储、准入策略、负载和升级窗口已经验证。真实集群部署是显式可选能力，默认关闭自动触发。Helm 将 CRD 放在 `crds/`，避免 upgrade/rollback 意外接管 CRD 删除与替换；schema 演进需要独立流程。

当前实现状态：GitHub Actions、双镜像 tag、Helm、Smoke Test、E2E 复用与 rollback 已实现。Go 门禁、Docker、Helm lint/template 及 Kind 流程的本次验证结果记录在下方 Phase 验证流水；GHCR 推送、GitHub Environment approval、Harbor 与真实云集群只有配置实现，必须在对应外部环境实际运行后才能标记为已验证。

### Phase 验证流水

| Phase | 命令 | 真实结果 |
|---|---|---|
| 1 | `go mod tidy && gofmt -w api/v1alpha1/*.go && go test ./...` | 通过（exit 0；API 包编译成功，无测试文件） |
| 2-3 | `go mod tidy && go test ./internal/diagnosis/... ./cmd/agent` | 通过（exit 0；node/pod 测试通过；Agent 编译通过） |
| 4-6 | `go test ./controllers ./internal/action ./cmd/action-controller` | 通过（exit 0；状态机与 Verify 测试通过；Controller 编译通过） |
| 7 | `go test ./internal/policy ./internal/audit ./cmd/agent ./cmd/action-controller ./controllers` | 通过（exit 0；Policy 测试与两个入口编译通过） |
| 8 | `go mod tidy && go test ./internal/metrics ./cmd/agent ./controllers ./cmd/action-controller` | 通过（exit 0） |
| 9 | `./scripts/kind-up.sh k8s-ops` | 通过；三节点均 Ready |
| 9 | `./scripts/e2e.sh k8s-ops` | 通过（exit 0；4/4 Case 通过） |
| 9 | `kubectl apply -f config/otel/collector.yaml` + Collector debug 日志检查 | 修复一次配置错误后通过；DaemonSet 2/2 Ready，Agent/Controller 日志均被采集 |
| 10 | `gofmt -w cmd internal controllers api && go mod tidy && go test ./...` | 通过（exit 0） |
| 10 | `go vet ./...` | 通过（exit 0，无输出） |
| 10 | `go build ./cmd/agent ./cmd/action-controller` | 通过（exit 0） |
| 10 | `make docker-build` | 通过（exit 0；agent/controller 两个镜像构建成功） |
| 10 | Controller `/metrics` 真实查询 | 通过；action total、failed total、duration histogram 均有实际样本 |
| 11 | `make fmt && make vet && make test && make build` | 通过（exit 0；Go 1.26.4 按 go.mod 1.24 兼容模式执行） |
| 11 | `make docker-build TAG=cicd-test` | 通过（exit 0；Agent/Controller 两个 distroless 非 root 镜像均完成构建） |
| 11 | Helm 4.3.0 `lint` / `template`、PyYAML parse、actionlint 1.7.12 | 通过（chart lint 0 failure；渲染 10 个 YAML document；3 个 workflow 均通过 actionlint） |
| 11 | 单节点 Kind + Helm deploy + rollout + `make smoke-test` | 通过（exit 0；两个 Deployment Ready、CRD discovery、SA/RBAC、Agent informer、真实 DeploymentRestart Action、healthz/metrics 均通过）。首次运行发现 API discovery 返回完整资源名并修正断言后重跑通过。 |
| 11 | Helm upgrade revision 2 + `make rollback PREVIOUS_REVISION=1` | 通过（exit 0；生成 revision 3 `Rollback to 1`，Agent/Controller rollout 再次通过） |
| 11 | `make kind-up KIND_CLUSTER=k8s-ops-cicd` + `make e2e` | 三节点动态验收受环境阻塞：control-plane 可启动，但两个 worker join 持续挂起且 kube-proxy/CNI 不健康；原 `k8s-ops` 集群又因旧控制面证书 NotBefore 晚于当前 UTC 而无法 reconcile。单节点不具备 NodeMaintenance E2E 所需 worker，因此本次未执行 E2E，也未标记为通过。 |

## 24. Checkpoint 6：发布失败检测与 Helm 回滚

发布模型固定为 V1/V2/V3：V1 是 Checkpoint 5 健康基线（Helm revision 4）；V2 增加只读构建身份响应头并把 Agent readiness path 参数化，使用 SHA 派生的不可变镜像 tag 发布为 revision 5；V3 不改 detector，只通过 Helm values 将 readiness path 指向不存在的路径，发布为 revision 6。两个 V3 进程均正常，只有 Agent readiness 失败，因此故障面严格限制在平台自身。

Agent `/healthz` 的 `X-K8s-Ops-Version` 与 `X-K8s-Ops-Source` 由 Docker build args 经 linker flags 注入；本地构建仍使用 `dev`/`local` 默认值。这个标识只提供制品追踪，不参与健康判断。chart 的 `agent.readinessProbe.path/port` 默认仍为 `/healthz` 和 `metrics`，V3 的坏路径只存在于 Helm revision 6 values 中。

V3 证明 `Running` 只表示容器进程存在，`Ready=False` 才决定 EndpointSlice endpoint 是否可服务。默认 RollingUpdate 保留了 V2 的 Ready Agent，因而 Service 在 V3 窗口 251/251 请求成功、Deployment `availableReplicas` 仍等于期望值 1。其结果是现有 DeploymentReplicasUnavailable detector 没有输出：这不是漏掉一条已满足规则的事件，而是该规则的输入条件没有成立。Prometheus 的 Pod readiness 指标则在 9.365916780s 明确变为 0，展示了 Agent 的资源语义诊断与 Prometheus 时序状态检测之间的差异。

Rollback 指向历史 revision 5，但 Helm 把“把 revision 5 的 manifest/values 重新应用”记录为一个新的 release 操作，所以历史增长到 revision 7，而不是把当前 revision 数字倒退到 5。revision 6 保留为可审计的 superseded 坏版本。恢复后 Agent/Controller Ready、Agent active diagnosis=0、Prometheus 23/23、Alertmanager 0 active。

这不是生产级自动回滚：实验由人工保存现场、判定失败并调用脚本；没有 progressive delivery controller、SLO/error-budget gate、审批、签名/SBOM、策略准入、数据库兼容验证、多集群编排或自动停止/回滚控制。GitHub Actions 与 GHCR 只有配置，因本地无 remote 未真实验证。
