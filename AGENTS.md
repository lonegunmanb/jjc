# AGENTS.md — ai-institute 工程纪律

本仓库面向自动化编码 agent（GitHub Copilot Cloud Agent / Codex / Cline / Amp / Claude Code 等）。
所有 agent **必须**遵守本文件下列硬性约束，违反任意一条即视为任务未完成。

---

## 1. 写代码必须 TDD（Test-Driven Development）

> 适用范围：任何向 `cmd/`、`internal/`、`pkg/` 写入 Go 代码的任务。
> 例外：纯文档（`docs/`、`*.md`）、prompt 文件（`prompts/*.md`）、Bicep / Dockerfile 等基础设施模板可豁免（但仍鼓励 lint）。

### 1.1 Red-Green-Refactor 循环

对每一个新增的可测试单元（函数、方法、HTTP handler、actor message handler、纯函数 helper 等）必须严格执行：

1. **Red**：先写一个会失败的测试。提交前必须能演示「测试存在 → 跑 → 看见预期的失败信息」。
2. **Green**：写出**刚好**让测试通过的最小实现。不允许带入未被测试覆盖的额外能力。
3. **Refactor**：在测试保持绿色的前提下整理命名、消除重复、抽象边界。

agent 在 PR 描述里必须给出一句话证据，例如：
- “先写 `TestWhiteboardActor_SubmitCard_GateRejects` 跑出 `undefined: SubmitCard`，再实现 handler 让其变绿。”

### 1.2 测试质量基线

- 使用 `stretchr/testify`（`require` 用于前置条件、`assert` 用于多断言）。详见 [.agents/skills/golang-stretchr-testify/SKILL.md](.agents/skills/golang-stretchr-testify/SKILL.md)。
- 默认 table-driven 写法；并发或长跑用例必须配 `t.Parallel()` 与 `goleak`。详见 [.agents/skills/golang-testing/SKILL.md](.agents/skills/golang-testing/SKILL.md)。
- 行为而非实现：测公开 API 的可观察行为，不测私有字段。
- 不允许把已有失败测试 `t.Skip()` 或注释掉来强行通过。
- 不允许只写「打印不断言」的伪测试。
- 覆盖率不是目标，**未被测试覆盖的分支不允许进入 PR**——若分支无法测，必须在 PR 中说明原因（外部依赖、平台限制等）并加 `// note: untested because ...` 单行注释。

### 1.3 必须通过的本地校验

在提交 PR 前 agent 必须按顺序跑通下列命令，并把输出贴到 PR 描述里：

```bash
go vet ./...
go test ./... -race -count=1
golangci-lint run
```

如果新增模块带集成测试（如 Dapr / MinIO / Azurite），必须提供 `make test-integration` 入口并在 PR 中证明已跑过。

### 1.5 写代码前的行为契约（Karpathy 准则）

> 出处：[karpathy-guidelines](https://github.com/multica-ai/andrej-karpathy-skills/blob/main/skills/karpathy-guidelines/SKILL.md)（MIT）。
> 本节是**认知层**纪律，§1.1 是**产物层**纪律，两者必须同时遵守。

#### a. 写代码前先想清楚（Think Before Coding）

- 把**隐含假设**写在 PR 描述里（用 "## Assumptions" 小节，哪怕 1 行）
- 任务有多种合理解读时，**先在 issue / PR 讨论里列出选项**让人类选，**不要默默选一个**
- 如果有更简方案，**点出来**，必要时反对原方案
- 不清楚就 **stop**，明确指出哪里不清楚，再 ask

#### b. 最小代码先行（Simplicity First）

- 只写问题需要的最少代码；不做用户没要求的"扩展点"
- 单次使用的代码不要包抽象层 / interface / option struct
- 不要为不可能的场景写错误处理
- 200 行能 50 行写完，就重写
- 自查：「资深工程师会说这段过度设计吗？」如果会，简化

#### c. 外科手术式修改（Surgical Changes）

- 只动必须动的；不要"顺手"改邻近代码 / 注释 / 格式
- 不要 refactor 没坏的东西
- **匹配仓库现有风格**，即使你个人会另写
- 看见无关 dead code，**在 PR 描述里提一句**，不要顺手删
- 你自己改动产生的孤儿 import / 变量 / 函数 → **必须**删
- 测试：**每一行变更都必须能追溯回 issue / 任务**

#### d. 目标驱动执行（Goal-Driven Execution）

- 把任务翻译成**可验证的成功标准**——这正是 §1.1 TDD Red 步骤
- 多步任务要在 PR 描述列出最简计划：
  ```
  1. <步骤> → 验证：<怎么检查>
  2. <步骤> → 验证：<怎么检查>
  ```
- 强成功标准让 agent 能独立闭环；弱标准（"让它能跑"）会反复 ping 用户

#### Tradeoff 提示

Karpathy 原文：「these guidelines bias toward caution over speed. For trivial tasks, use judgment.」

本仓库的判定：

- **任何**改动 `cmd/` / `internal/` / `pkg/` / `*.bicep` / `Dockerfile` 的 PR 都按全套准则走
- 纯文档 / `prompts/` / `.agents/skills/**` 改动可放宽 b 和 c（文档的"简单"和"外科手术"是另一套标准）
- 「trivial」由独立评审 sub-agent 判定，不由实现 sub-agent 自己说了算

### 1.4 CI 工作流覆盖自检（每次 `git commit` 前必做）

本地校验只能证明「在我这台机器上跑得过」。要让团队的合并门禁可信，**CI 必须替我们守门**。所以 agent 在每次 `git commit` 之前必须做一次结构化核对：

1. 列出本次 staged diff 涉及的路径，按下表分类：

   | diff 命中的路径                                     | CI 必须存在的 PR 检查                                                                                          |
   | --------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
   | `cmd/**`、`internal/**`、`pkg/**`、`go.mod`、`go.sum` | 至少一个 workflow 在 `pull_request` 事件上跑 `go test ./... -race -count=1` + `go vet ./...` + `golangci-lint run`，且 `paths` / `paths-ignore` 过滤不能把本次改动目录排除掉 |
   | `Dockerfile`、`deploy/docker/**`、`build/**`         | 至少一个 workflow 在 `pull_request` 上跑 `docker build`（或 `docker buildx build`），并跑 `trivy`（或等价 SCA）扫描，HIGH+ 漏洞必须 fail |
   | `deploy/azure/**`、`*.bicep`                         | 至少一个 workflow 跑 `bicep build` + `bicep lint`（或 `az bicep build`）                                       |
   | `.github/workflows/**`                              | actionlint                                                                                                     |

2. 打开 `.github/workflows/*.yml` 实际确认上述 job 存在且 `on: pull_request` 配置正确（不能只在 `push: main` 上跑）。
3. 处理结论：
   - **缺失但可在本 PR 内补齐** → 直接补齐对应 workflow，这属于「让 CI 真的能替我们守门」的最小必要改动，**不算超出本任务范围**。
   - **当前 PR 无法补齐**（例如需要新增 secret、需要审批等） → 必须在 PR 描述 `## CI Coverage Self-Check` 段落显式写出「CI Gap」，列出缺失 job、原因、跟进任务 ID。
4. 独立评审子 agent 会照搬上表核对：若发现 diff 命中的路径在 CI 上没有对应 PR 门禁，且 PR 描述里也没有 explicit `CI Gap` 说明，**直接判 `MAJOR`**，等同于 `CHANGES_REQUESTED`。

> 例外：纯 `docs/**`、`*.md`、`prompts/**`、`.agents/skills/**`、`README.md` 类改动可以只跑 markdown lint / 链接检查；这些 workflow 若缺失，建议补但不阻塞。

---

## 2. 提交 PR 之前必须经过独立子 agent 代码评审

> 自评不算。必须由 [.github/agents/code-reviewer.agent.md](.github/agents/code-reviewer.agent.md) 这个**只读**子 agent 独立审阅。

### 2.1 强制流程

1. 实现 agent 完成 TDD 循环，本地校验全绿。
2. **必须**通过子 agent 调用机制（`runSubagent` 或等价的 hand-off）调用 `code-reviewer` 子 agent，传入：
   - 本次任务 ID（如 `P1-T12`）
   - 任务目标的一句话描述
   - 已修改 / 新增的文件清单
   - 本地校验命令的实际输出
3. `code-reviewer` 子 agent 会输出一份结构化评审报告（见 §2.3），必须包含明确 verdict：
   - `APPROVED` — 可以提交 PR
   - `CHANGES_REQUESTED` — 不允许提交 PR，必须先修
4. 若 verdict 为 `CHANGES_REQUESTED`，实现 agent 修复后**重新触发**子 agent 评审（不允许跳过），直到 `APPROVED`。
5. PR 描述里必须粘贴最后一次 `APPROVED` 评审报告全文，作为合并门禁证据。

### 2.2 评审隔离硬约束

- 评审子 agent **只能读，不能写**。tools 已被限制为 `read, search`，禁止 `edit` 与 `execute`。
- 评审子 agent 必须以**独立 session** 运行（context 隔离），不复用实现 agent 的对话历史，避免 confirmation bias。
- 实现 agent 不允许「修改评审子 agent 的 prompt 或裁剪其评审清单」来换取 `APPROVED`。

### 2.3 评审报告必须字段

```
Task: P1-T##
Verdict: APPROVED | CHANGES_REQUESTED
Summary: <1-2 句>
Findings:
  - [BLOCKER|MAJOR|MINOR|NIT] <file:line> — <问题描述> — <建议修复>
TDD Evidence Check:
  - 新增公开符号是否都有对应失败优先的测试: yes/no + 证据
  - 是否存在被跳过/注释的测试: yes/no
  - race / lint / vet 是否真的跑过: yes/no
Security & Safety:
  - 输入边界、错误处理、并发安全、敏感数据是否经过检查
Out-of-scope Drift:
  - 是否引入了任务范围外的修改
```

`BLOCKER` 或 `MAJOR` 中任意一条存在即必须为 `CHANGES_REQUESTED`。

---

## 3. PR 提交规范

- 分支命名：`task/P1-T##-<slug>`，对应《实施路线图.md》§2.8 的任务 ID。
- 单 PR 单任务，禁止跨任务夹带。
- PR 描述必须按下面模板填写：

```
## Task
P1-T## — <标题>

## TDD Evidence
- Red: <第一个失败测试名 + 失败信息节选>
- Green: <最小实现摘要>
- Refactor: <若有，描述重构>

## Local Checks
$ go vet ./...
<output>
$ go test ./... -race -count=1
<output>
$ golangci-lint run
<output>

## CI Coverage Self-Check
- diff 命中类别：<Go 代码 | Dockerfile | Bicep | 文档 | …>
- 对应 PR 门禁 workflow：<workflow 文件名 + job 名 + 触发事件>
- CI Gap（如有）：<缺失的 job + 原因 + 跟进任务 ID>；无 gap 写 `none`

## Independent Code Review
<粘贴 code-reviewer 子 agent 的最终 APPROVED 报告全文>

## Out of Scope
<本 PR 显式不做的事>
```

- 任何 PR 缺少上述五个 section 都会被视为不合规，必须补齐再请求合并。

---

## 4. 其他始终适用的工程纪律

- 配置 / CLI：见 [.agents/skills/golang-cli/SKILL.md](.agents/skills/golang-cli/SKILL.md)、[.agents/skills/golang-spf13-cobra/SKILL.md](.agents/skills/golang-spf13-cobra/SKILL.md)、[.agents/skills/golang-spf13-viper/SKILL.md](.agents/skills/golang-spf13-viper/SKILL.md)。
- 日志 / 错误：使用 `log/slog` + `samber/oops`，见 [.agents/skills/golang-error-handling/SKILL.md](.agents/skills/golang-error-handling/SKILL.md)、[.agents/skills/golang-samber-oops/SKILL.md](.agents/skills/golang-samber-oops/SKILL.md)。
- 项目布局与命名：见 [.agents/skills/golang-project-layout/SKILL.md](.agents/skills/golang-project-layout/SKILL.md)、[.agents/skills/golang-naming/SKILL.md](.agents/skills/golang-naming/SKILL.md)。
- 安全：见 [.agents/skills/golang-security/SKILL.md](.agents/skills/golang-security/SKILL.md)。
- 评审视角清单（实现 agent 自查也建议读）：见 [.agents/skills/code-review-excellence/SKILL.md](.agents/skills/code-review-excellence/SKILL.md)。

## 5. 项目上下文要点（给 agent 减少探索成本）

- **白板研究流程（W3，已完成）**：白板（`WhiteboardActor`）由 chair-rule 顺序召唤分析师，**同一时刻只有一位 analyst 在跑**；analyst 通过 `HandoffSuggestion` 指定下一棒；所有 analyst 都发言过后白板 emit `analyst_phase.completed` 并**显式**召唤 `forecaster_final_predictor`。**forecaster 永远不在 analyst 队列。** 完整设计 + ASCII 流程图 + chair-rule 决策表见 [docs/whiteboard-flow.md](docs/whiteboard-flow.md)；任何"重新引入并行 broadcast / 多个 goroutine 同时召唤 analyst"的改动都视为回归，请先开 issue 讨论。
- **关键库选型**：HTTP=`go-chi/chi`、日志=`log/slog`、配置=`spf13/cobra+viper`、测试=`stretchr/testify`、错误=`samber/oops`、Actor=`github.com/dapr/go-sdk`、Copilot SDK=`github.com/lonegunmanb/copilot-sdk@v0.3.2`（通过 `go.mod` `replace` 覆盖官方 SDK）。
- **文档命名**：仓库语言主要是中文文档 + Go 代码。文档命名为中文文件名是约定，不要重命名。

## 6. 不要做的事

- 不要在没有失败测试的情况下提交实现代码。
- 不要绕过独立子 agent 评审直接开 PR。
- 不要在一个 PR 里夹带多个任务的改动；不要顺手重构任务范围外的文件。
- 不要默默替用户做歧义选择——有多种合理解读时停下来在 issue / PR 评论里列选项。
- 不要在任务范围外"顺手"重构、改格式、调注释；out-of-scope drift 会被独立评审打回。
- 不要修改 [`.github/agents/code-reviewer.agent.md`](.github/agents/code-reviewer.agent.md) 来「让评审更宽松」。
- 不要把已有失败测试 `t.Skip()` 掉换通过；也不要把测试改成只断言「不 panic」。
- 不要使用官方 Copilot Go SDK，必须走 `lonegunmanb/copilot-sdk` adapter 路径。
- 不要在 CI workflow 里用 `paths-ignore` 把本次 diff 影响的路径排除掉以闪过单测门禁；也不要把关键 job 只绑在 `push: main` 上、不走 `pull_request`。
- **不要重新引入"并行 broadcast / 多个 goroutine 同时召唤 analyst"** —— W3 已经把这条路径完全删除，详见 [docs/whiteboard-flow.md §1 设计目标 + §4 为什么不并行](docs/whiteboard-flow.md)。
- **不要把 `forecaster_final_predictor` 当成 analyst 放进 analyst 队列**；它由白板在 `analyst_phase.completed` 时（或 deadline 兜底时）显式召唤，走 `roleForcedPredictor` 路径。

---

## 7. 一句话总结

> **No tests, no code. No independent review, no PR. No CI gate, no commit.**
