# AGENTS.md — JJC 工程纪律

本仓库面向自动化编码 agent（GitHub Copilot Cloud Agent / Codex / Cline / Amp / Claude Code 等）。
所有 agent **必须**遵守本文件下列硬性约束，违反任意一条即视为任务未完成。

> 项目语境：JJC（军机处）是一个 Trello webhook 网关。语言主要是 Go；HTTP 框架 `gin-gonic/gin`；
> 路由配置 `hashicorp/hcl/v2` + `zclconf/go-cty`；TUI `charmbracelet/bubbletea`；Copilot 调用走
> 官方 SDK `github.com/github/copilot-sdk/go`；隧道走 `cloudflared` 外部进程。
> 仓库无 `cmd/` 与 `pkg/`，所有产品代码在 `internal/` 下，入口是仓库根目录的 `main.go`。

---

## 1. 写代码必须 TDD（Test-Driven Development）

> 适用范围：任何向 `internal/` 或仓库根目录（`main.go` 等）写入 Go 代码的任务。
> 例外：纯文档（`docs/`、`*.md`、`README.md`、`AGENTS.md`、`.github/agents/*.md`）、
> playbook 模板（`examples/**/*.md`）可豁免（但仍鼓励 lint）。

### 1.1 Red-Green-Refactor 循环

对每一个新增的可测试单元（函数、方法、HTTP handler、dispatcher 消息处理、纯函数 helper、
HCL decode/validate、Trello SDK adapter 等）必须严格执行：

1. **Red**：先写一个会失败的测试。提交前必须能演示「测试存在 → 跑 → 看见预期的失败信息」。
2. **Green**：写出**刚好**让测试通过的最小实现。不允许带入未被测试覆盖的额外能力。
3. **Refactor**：在测试保持绿色的前提下整理命名、消除重复、抽象边界。

agent 在 PR 描述里必须给出一句话证据，例如：

- "先写 `TestWorkDirPreparer_BaseDirFromConfig` 跑出 `undefined: SetBaseDirFromConfig`，
  再实现 setter 让其变绿。"

### 1.2 测试质量基线

- 使用 Go 标准库 `testing` + 仓库现有断言风格（多数现有测试用裸 `t.Errorf` / `t.Fatalf`，
  保持一致；不要单方面引入 testify）。
- 默认 table-driven 写法；触及 goroutine 的用例必须配 `t.Parallel()`，并优先用 `context.Context`
  超时而非 `time.Sleep` 做同步。
- 行为而非实现：测公开 API 的可观察行为，不测私有字段。
- 不允许把已有失败测试 `t.Skip()` 或注释掉来强行通过。
- 不允许只写「打印不断言」的伪测试。
- 覆盖率不是目标，**未被测试覆盖的分支不允许进入 PR**——若分支无法测，必须在 PR 中说明原因
  （外部依赖如 Trello API、cloudflared 进程、Copilot SDK 等）并加 `// note: untested because ...`
  单行注释。

### 1.3 必须通过的本地校验

在提交 PR 前 agent 必须按顺序跑通下列命令，并把输出贴到 PR 描述里：

```bash
go vet ./...
go build ./...
go test -race -count=1 ./...
golangci-lint run --timeout=5m
```

`gosec ./...` 也建议本地跑一次（CI 上 `gosec` 是 `-no-fail`，只上传 SARIF，不阻塞；
但 agent 不应制造新的高危 finding）。

### 1.4 写代码前的行为契约（Karpathy 准则）

> 出处：[karpathy-guidelines](https://github.com/multica-ai/andrej-karpathy-skills/blob/main/skills/karpathy-guidelines/SKILL.md)（MIT）。
> 本节是**认知层**纪律，§1.1 是**产物层**纪律，两者必须同时遵守。

#### a. 写代码前先想清楚（Think Before Coding）

- 把**隐含假设**写在 PR 描述里（用 "## Assumptions" 小节，哪怕 1 行）
- 任务有多种合理解读时，**先在 issue / PR 讨论里列出选项**让人类选，**不要默默选一个**
- 如果有更简方案，**点出来**，必要时反对原方案
- 不清楚就 **stop**，明确指出哪里不清楚，再 ask

#### b. 最小代码先行（Simplicity First）

- 只写问题需要的最少代码；不做用户没要求的「扩展点」
- 单次使用的代码不要包抽象层 / interface / option struct
- 不要为不可能的场景写错误处理
- 200 行能 50 行写完，就重写
- 自查：「资深工程师会说这段过度设计吗？」如果会，简化

#### c. 外科手术式修改（Surgical Changes）

- 只动必须动的；不要「顺手」改邻近代码 / 注释 / 格式
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
- 强成功标准让 agent 能独立闭环；弱标准（「让它能跑」）会反复 ping 用户

#### Tradeoff 提示

Karpathy 原文：「these guidelines bias toward caution over speed. For trivial tasks, use judgment.」

本仓库的判定：

- **任何**改动 `internal/**` / `main.go` / `go.mod` / `go.sum` / `.github/workflows/**` 的 PR 都按全套准则走
- 纯文档 / `.agents/skills/**` / `examples/**` 改动可放宽 b 和 c（文档的「简单」和「外科手术」是另一套标准）
- 「trivial」由独立评审 sub-agent 判定，不由实现 sub-agent 自己说了算

### 1.5 CI 工作流覆盖自检（每次 `git commit` 前必做）

本地校验只能证明「在我这台机器上跑得过」。要让团队的合并门禁可信，**CI 必须替我们守门**。
所以 agent 在每次 `git commit` 之前必须做一次结构化核对：

1. 列出本次 staged diff 涉及的路径，按下表分类：

   | diff 命中的路径                                     | CI 必须存在的 PR 检查（在 `.github/workflows/ci.yml`） |
   | --------------------------------------------------- | ----------------------------------------------------- |
   | `internal/**`、`main.go`、`go.mod`、`go.sum`         | `test` job（vet + build + `go test -race -count=1`） + `lint` job（`golangci-lint`） + `gosec` job |
   | `.github/workflows/**`                              | 至少跑 `actionlint`（如缺则在 PR 描述显式记 `CI Gap`） |
   | `.agents/skills/**`、`docs/**`、`*.md`               | 无强制 CI；建议人工 review 即可                       |

2. 打开 `.github/workflows/ci.yml` 实际确认上述 job 存在且 `on: pull_request` 配置正确（不能
   只在 `push: main` 上跑）。当前仓库默认在 `pull_request: branches=[main]` 上跑全部三个 job。
3. 处理结论：
   - **缺失但可在本 PR 内补齐** → 直接补齐对应 workflow，这属于「让 CI 真的能替我们守门」的最小
     必要改动，**不算超出本任务范围**。
   - **当前 PR 无法补齐**（例如需要新增 secret、需要审批等） → 必须在 PR 描述
     `## CI Coverage Self-Check` 段落显式写出「CI Gap」，列出缺失 job、原因、跟进 issue ID。
4. 独立评审子 agent 会照搬上表核对：若发现 diff 命中的路径在 CI 上没有对应 PR 门禁，且 PR 描述
   里也没有 explicit `CI Gap` 说明，**直接判 `MAJOR`**，等同于 `CHANGES_REQUESTED`。

---

## 2. 提交 PR 之前必须经过独立子 agent 代码评审

> 自评不算。必须由 [.github/agents/code-reviewer.agent.md](.github/agents/code-reviewer.agent.md) 这个**只读**子 agent 独立审阅。
>
> 评审是一个**多轮协商**过程，不是「一次性裁决」。实现 agent 与 reviewer 通过结构化对话推进——
> 实现 agent 对每条 finding 用 `fix` / `defer` / `push-back` 三选一回应，reviewer 在下一轮把每条
> finding 标记为 `RESOLVED` / `DEFERRED` / `WITHDRAWN` / `HELD`。这套机制专为防止两类失败模式：
> (a) reviewer 无节制堆 finding 驱动范围蔓延、(b) 实现 agent 无条件接受所有 finding 导致永远收敛
> 不到 `APPROVED`。

### 2.1 强制流程（七步）

1. 实现 agent 完成 TDD 循环，本地校验全绿。
2. **必须**通过子 agent 调用机制（`runSubagent` 或等价的 hand-off）调用 `code-reviewer` 子 agent，
   传入：
   - 对应 issue 编号（如 `#46`）与一句话目标描述
   - 本轮编号 `Round: N`（首轮 `N=1`）
   - 已修改 / 新增的文件清单 + diff 大小（files / additions / deletions）
   - 本地校验命令（vet / build / test -race / golangci-lint）的实际输出
   - 实现 agent 自述的 TDD 证据（Red 测试名 + 失败信息节选 + Green 实现摘要）；纯文档 PR
     须显式说明走 §1.1 豁免
   - **`N ≥ 2` 时**：上一轮 reviewer 的完整报告 + 实现 agent 对每条 finding 的逐条回应
     + 本轮新引入的 diff
3. `code-reviewer` 子 agent 输出一份结构化评审报告（见 §2.3），必须包含明确 verdict：
   - `APPROVED` — 可以提交 PR
   - `CHANGES_REQUESTED` — 需要再走一轮
4. 若 verdict 为 `CHANGES_REQUESTED`，实现 agent 对每条 finding **必须显式三选一回应**——
   不允许沉默，也**不允许无条件全部接受所有 finding 直接改代码**（无条件接受会驱动范围蔓延、
   把任务越拖越大）：
   - `fix` — 同意 finding，按建议修改代码；本轮代码改动只针对这条 finding（或同一轮里的其它
     `fix` finding），不要顺手带入额外重构
   - `defer` — 同意 finding 是真问题但**不在本 PR 范围内**；必须**当场创建 follow-up issue**
     并把 issue 编号记入回应
   - `push-back` — 不同意 finding；必须给出**技术理由**（指向代码 / 测试 / 文档证据）说服 reviewer
5. 应用第 4 步的回应（写代码 / 开 issue / 起草反驳），然后跑一次本地校验确认仍然全绿。
6. 携带「上一轮报告 + 本轮逐条回应 + 新 diff + 新校验输出」**重新触发** reviewer。不允许跳过。
7. 循环直到 `APPROVED`，或触发 §2.4 的 10 轮上限释放条件。最后一轮的 `APPROVED` 报告
   （连同完整 Dialog Log，见 §3）粘贴到 PR 描述里作为合并门禁证据。

### 2.2 评审隔离硬约束

- 评审子 agent **只能读，不能写**。tools 已被限制为 `read, search`，禁止 `edit` 与 `execute`。
- 评审子 agent 每轮以**独立 session** 运行（不复用历史对话上下文以避免 confirmation bias），
  但**必须显式接收**：
  - 上一轮的完整报告（含每条 finding 的 severity / confidence / file:line / 建议）
  - 实现 agent 对每条 finding 的回应（`fix` 附带 commit SHA + diff / `defer` 附带 follow-up
    issue 编号 / `push-back` 附带技术理由）
  - 本轮新引入的 diff
- 实现 agent 不允许**未经授权**地修改评审子 agent 的 prompt 或裁剪其评审清单来换取
  `APPROVED`；如果本任务的目标本来就是改评审规则（例如本份 commit），按正常流程走，但要在
  PR 描述里显式记录这是规则变更 PR。

### 2.3 评审报告必须字段

```
Issue: #<num>
Round: <N>
Verdict: APPROVED | CHANGES_REQUESTED
Summary: <1-3 句>

Findings (this round, ≤10):
  - [BLOCKER|MAJOR|MINOR|NIT|SCOPE] (confidence=high|medium|low) <file:line> — <问题> — <建议>

Carried Forward (status of prior findings):
  - Round <k> [<sev>] <file:line>: <RESOLVED|DEFERRED|WITHDRAWN|HELD> — <一句话理由>
  - (Round 1 时写 "N/A — first round")

TDD Evidence Check:
  - 新增公开符号是否都有对应失败优先的测试: yes/no + 证据 / N/A 文档豁免
  - 是否存在被跳过/注释的测试: yes/no
  - race / lint / vet / build 是否真的跑过且全绿: yes/no

Security & Safety:
  - 输入边界、错误处理、并发安全、敏感数据是否经过检查

Out-of-scope Drift:
  - 是否引入了任务范围外的修改

Re-review Required: yes/no
```

- 单轮 finding（含 carried-forward 的 HELD）总数 **≤ 10**。
- `BLOCKER` 或 `MAJOR` 中任意一条存在（含 HELD）即必须为 `CHANGES_REQUESTED`。
- `confidence = low` 的 `BLOCKER` 自动降 `MAJOR`；`low` 的 `MAJOR` / `MINOR` 自动降 `NIT`；
  `low` 的 `NIT` 不输出。
- `Step 0` 的 `SCOPE` 命中时本报告唯一 finding 是该 `SCOPE`，不允许夹带其它细节 finding。

### 2.4 10 轮上限 + 释放规则

无限循环是评审制度的死敌。设定**单 PR 最多 10 轮**评审协商：

- **任何 `BLOCKER`（含 HELD）在任何轮次都阻塞合并**；到了第 10 轮仍有 `BLOCKER`，PR 必须
  close 或拆小重做。
- 第 10 轮还剩 `MAJOR` / `MINOR` / `NIT`（含 HELD）时，**释放权在实现 agent**——实现 agent
  必须为**每一条**残余 finding 选一项归宿：
  - `defer`（附 follow-up issue 编号），或
  - `push-back`（附技术理由）。

  这两项一旦记入 PR 描述 `## Dialog Log`，等同于把该条 `MAJOR` / `MINOR` / `NIT` 释放——
  reviewer 不再有否决权（已经协商 10 轮）。
- 触发 §2.4 释放时 PR 标题必须加 `[10-round-cap-released]` 前缀，由人类合并者最终决断是否合并。
- 计数规则：`Round 1` = 实现 agent 第一次把任务提交给 reviewer；reviewer 每一次输出报告记为
  下一轮的入口（即 reviewer 的第 N 轮报告 + 实现 agent 第 N 轮回应共同构成 `Round N`）。

> 这条规则**不是放水通道**——`BLOCKER` 永远阻塞；`MAJOR` / `MINOR` / `NIT` 释放需要实现
> agent 在每条上独立给出 `defer` 或 `push-back`，且全部记录在 PR Dialog Log 中可被人类复核。
> 规则的目的是终止「reviewer 每轮再加 1 条新 finding 让 PR 永远 CHANGES_REQUESTED」的死循环。

---

## 3. PR 提交规范

- 分支命名：`issue/<num>-<slug>`，对应 GitHub issue 编号。
- 单 PR 单 issue，禁止跨 issue 夹带。
- PR 标题：建议 `fix(<scope>): <短描述> (#<num>)` 或 `refactor(<scope>): ... (#<num>)`，
  scope 取自顶层包名（如 `workdir`、`dispatcher`、`router`、`tunnel`、`prompttmpl`）。
- PR 描述必须按下面模板填写：

```
## Issue
Closes #<num> — <标题>

## Assumptions
<把隐含假设列出来，无则写 none>

## TDD Evidence
- Red: <第一个失败测试名 + 失败信息节选>
- Green: <最小实现摘要>
- Refactor: <若有，描述重构>

## Local Checks
$ go vet ./...
<output>
$ go build ./...
<output>
$ go test -race -count=1 ./...
<output>
$ golangci-lint run --timeout=5m
<output>

## CI Coverage Self-Check
- diff 命中类别：<internal/Go | workflows | 文档 | …>
- 对应 PR 门禁 workflow：<workflow 文件名 + job 名 + 触发事件>
- CI Gap（如有）：<缺失的 job + 原因 + 跟进 issue ID>；无 gap 写 `none`

## Review Round
- Final Round: <N>（达到 APPROVED 或触发 §2.4 释放时的轮次）
- §2.4 Released: <yes / no>（若 yes，PR 标题须带 `[10-round-cap-released]` 前缀，并列出全部
  `defer` / `push-back` 的 finding 与归宿）

## Dialog Log
Round 1:
  Reviewer findings:
    - [<sev>] (confidence=<c>) <file:line> — <一行摘要>
    - ...
  Implementer responses:
    - <file:line> [<sev>]: fix — <做了什么修改 / 引入的 commit SHA>
    - <file:line> [<sev>]: defer — follow-up issue #<num>
    - <file:line> [<sev>]: push-back — <技术理由摘要>
  Reviewer Round 2 verdict on each: <RESOLVED|DEFERRED|WITHDRAWN|HELD>
Round 2:
  ...
（直到 APPROVED 或触发 §2.4 释放）

## Independent Code Review
<粘贴最后一轮 APPROVED 评审报告全文（含 Findings / Carried Forward / TDD Evidence Check /
 Security & Safety / Out-of-scope Drift / Re-review Required）>

## Out of Scope
<本 PR 显式不做的事>
```

- 任何 PR 缺少上述 section 都会被视为不合规，必须补齐再请求合并。

---

## 4. 其他始终适用的工程纪律

### 4.1 日志规范

- **日志默认使用英语**。例外：日志的内容本身是中文（如转发用户在 Trello 卡片上的中文评论、
  打印用户配置的中文 list 名等），这部分原样保留。
- 结构化字段优先 `event=<token> k1=v1 k2=v2` 风格，与 `internal/app/sysevent` 现有 `Format`
  保持一致。
- 不要在日志里打印完整凭证（Trello API key/token/secret 等）。如需排错，用 fingerprint
  或前缀截断（参考 `Config.Redacted()`）。
- 工人提示词（prompt）的中文叙述属于人机界面，不属于日志；不受本节英语化要求约束。

### 4.2 跨平台

- 任何涉及文件路径、外部二进制（git / cloudflared 等）、shell 调用的代码都必须考虑 Linux /
  macOS / Windows 三个目标。
- 不允许硬编码 Windows 风格路径作为默认值（如 `` `C:\project` ``）；默认路径必须按 OS 分支
  或通过配置注入。
- 跨平台差异用 `//go:build <tag>` 文件区分（参考 `internal/app/tunnel/stop_unix.go` /
  `stop_windows.go`）。

### 4.3 配置

- 用户可配置项一律走 `internal/app/config.go` 中的 `Config` 结构，按 env / flag 双轨注入。
- 新增配置项必须：
  1. 在 `Config` 上声明字段 + 默认值 + 校验。
  2. 在 `Config.Redacted()` 决定是否需要脱敏。
  3. 更新 `README.md` 的环境变量表。

### 4.4 安全

- 任何处理外部输入（webhook body、Trello 卡片字段、HCL 用户配置、命令行参数）的代码都必须有
  显式校验；输入边界已有的防御例（`internal/app/cardid.go`、`validateBasename`、
  `prompttmpl` 的符号链接拒绝）是范式，请保持一致。
- 不要把外部输入直接拼到 `exec.Command` / shell；如必须，先做正则白名单（参考
  `aiassistedrefresh` 对 issue 编号的 `^[1-9][0-9]{0,6}$` 校验）。
- 不要绕过 webhook 签名验证（`internal/app/verify.go`）。

### 4.5 提交消息

- 推荐 Conventional Commits（`feat:` / `fix:` / `refactor:` / `docs:` / `test:` / `chore:`）。
- commit footer 必须包含：
  ```
  Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>
  ```

---

## 5. 项目上下文要点（给 agent 减少探索成本）

- **核心数据流**：Trello webhook → `verify.go` 签名校验 → `slim.go` 裁剪 → `router/` HCL 引擎
  路由 → `dispatcher.go` 按 card 路由到 per-card 的 Copilot worker session → worker 通过
  `trello_tools.go` 注入的工具集回写 Trello。
- **关键抽象**：
  - `WorkDirPreparer`（`internal/app/workdir.go`）—— 每张卡的本地工作目录准备 + Hook 链。
  - `Dispatcher`（`internal/app/dispatcher.go`）—— 进程内 per-card 消息分发与生命周期管理。
  - `CopilotRunner`（`internal/app/runner.go`）—— Copilot 会话封装。
  - `prompttmpl.Renderer`（`internal/app/prompttmpl/`）—— 严格模式模板渲染，未知变量启动期 fatal。
  - `kanban.Resolved`（`internal/app/kanban/`）—— `kanban {}` HCL 块解析后的视图，提供模板变量。
  - `sysevent.Sink`（`internal/app/sysevent/`）—— 全进程结构化事件输出层。
- **依赖选型**：HTTP=`gin`、HCL=`hashicorp/hcl/v2`+`zclconf/go-cty`、TUI=`charmbracelet/bubbletea`、
  Trello=`lonegunmanb/go-trello-sdk`、Copilot=`github.com/github/copilot-sdk/go`、
  配置抓取=`hashicorp/go-getter/v2`。
- **CI**：`.github/workflows/ci.yml` 在 `pull_request` 上跑 `vet + build + test -race` /
  `golangci-lint` / `gosec`。三个 job 都必须绿（gosec 不阻塞但 SARIF 会上传）。

---

## 6. 不要做的事

- 不要在没有失败测试的情况下提交实现代码。
- 不要绕过独立子 agent 评审直接开 PR。
- 不要在一个 PR 里夹带多个 issue 的改动；不要顺手重构任务范围外的文件。
- 不要默默替用户做歧义选择——有多种合理解读时停下来在 issue / PR 评论里列选项。
- 不要在任务范围外「顺手」重构、改格式、调注释；out-of-scope drift 会被独立评审打回。
- 不要**无条件接受 reviewer 的每一条 finding**——必须对每条独立判断后给出 `fix` / `defer` /
  `push-back` 之一（见 §2.1 第 4 步）。无条件接受会驱动范围蔓延、收敛不到 `APPROVED`。
- 不要**未经授权**地修改 [`.github/agents/code-reviewer.agent.md`](.github/agents/code-reviewer.agent.md)
  以放宽评审标准；如果本任务的目标本来就是改评审规则，按正常 PR 流程走（issue + 评审报告
  + 人类合并者审）。
- 不要把已有失败测试 `t.Skip()` 掉换通过；也不要把测试改成只断言「不 panic」。
- 不要硬编码任何用户可见的路径（特别是 `C:\project` 这种 Windows-only 默认值）；走 `Config`。
- 不要在 CI workflow 里用 `paths-ignore` 把本次 diff 影响的路径排除掉以闪过门禁；也不要把关键
  job 只绑在 `push: main` 上、不走 `pull_request`。
- 不要打印完整 Trello 凭证 / OAuth secret / GitHub PAT；走脱敏。
- 不要在日志里混用中英文（除非日志内容本身是中文，如转发用户输入）。

---

## 7. 一句话总结

> **No tests, no code. No independent review, no PR. No CI gate, no commit. Reviewer talks; implementer doesn't just nod.**
