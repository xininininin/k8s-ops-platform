# k8s-ops-platform

Kubernetes 故障诊断与自动化运维平台。详细设计、决策、进度与验证结果统一见 `DESIGN.md`。

[![CI](https://github.com/example/k8s-ops-platform/actions/workflows/ci.yml/badge.svg)](https://github.com/example/k8s-ops-platform/actions/workflows/ci.yml)
[![Release](https://github.com/example/k8s-ops-platform/actions/workflows/release.yml/badge.svg)](https://github.com/example/k8s-ops-platform/actions/workflows/release.yml)

## 快速开始

```sh
make ci
make kind-up
make docker-build
make kind-load
make deploy
make smoke-test
make e2e
```

以上命令需要 Go 1.24、Docker、kubectl、Kind、Helm 和 curl。原始清单部署入口仍保留为 `make deploy-yaml`。

## CI/CD

```text
Git Push / PR → CI → fmt / vet / test / build
main 或 Git tag → 双镜像构建 → GHCR
GHCR SHA 镜像 → Kind → Helm → Rollout → Smoke Test → E2E
                                                └─ 失败 → Helm Rollback
```

CI 在 PR 及 main/master push 上执行 `make ci`。Release 在 main/master push 或 Git tag 上发布：

- `ghcr.io/<owner>/k8s-ops-platform-agent:sha-<12位提交SHA>`
- `ghcr.io/<owner>/k8s-ops-platform-controller:sha-<12位提交SHA>`
- main/master 同时更新 `latest`；Git tag 同时发布同名 tag。

CD 默认只在临时 Kind 集群验证已发布的不可变 SHA 镜像。真实集群只能从 `workflow_dispatch` 选择 `production`，并绑定 GitHub `production` Environment；仓库管理员应为该 Environment 配置 required reviewers。PR 不会触发部署。

本地部署指定镜像：

```sh
make deploy IMAGE_PREFIX=ghcr.io/<owner>/k8s-ops-platform TAG=sha-0123456789ab
make smoke-test
make rollback                       # 自动选择上一 Helm revision
make rollback PREVIOUS_REVISION=2   # 明确回到指定 revision
```

GHCR 发布使用 Actions 自动提供的 `GITHUB_TOKEN`，无需保存明文密码。可选生产部署需要在 `production` Environment 中保存 `KUBECONFIG` Secret；私有 GHCR 还需由目标集群预先配置镜像拉取凭据。Harbor 未在当前版本联调，但 Makefile 保留 registry 前缀接口，可用 `IMAGE_PREFIX=harbor.example.com/team/k8s-ops-platform`；自动发布 Harbor 时应配置 `HARBOR_USERNAME`、`HARBOR_PASSWORD` Secrets，不得写入仓库。

Helm chart 位于 `charts/k8s-ops-platform`。CRD 放在 chart 的 `crds/`，首次安装时创建，Helm upgrade/rollback 不会隐式替换或删除其生命周期；CRD schema 变更应单独评审和升级。

## 目录

- `cmd/`：Diagnosis Agent 与 Action Controller 入口
- `api/`、`controllers/`：Action API 与 Reconcile 控制器
- `internal/`：诊断、策略、执行、验证、指标与审计
- `config/`、`deploy/`：CRD、RBAC、工作负载和可观测性清单
- `charts/`：可覆盖镜像、资源、命名空间和可观测性配置的 Helm chart
- `examples/`：故障与 Action 示例
- `test/`、`scripts/`：单元测试、E2E 和 Kind 自动化
