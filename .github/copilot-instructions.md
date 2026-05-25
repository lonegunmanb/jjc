# GitHub Copilot Instructions for JJC (repo-wide)

这是 JJC（军机处）仓库给 **GitHub Copilot 任何入口**（IDE Copilot Chat / github.com Copilot /
Cloud Coding Agent / CLI）的全局工作纪律。完整规则见根目录的 [AGENTS.md](../AGENTS.md)；本文件
把最核心、最容易被遗忘的硬性约束镜像到 Copilot 的默认上下文里，避免任何入口绕过门禁。

> 项目语境：JJC 是一个 Trello webhook 网关，按规则路由到 per-card 的 Copilot worker session。
> 语言 Go，HTTP=`gin`，配置=`hashicorp/hcl/v2`，入口在仓库根 `main.go`，产品代码全部在
> `internal/` 下。

---

## 1. TDD 优先

写 `internal/**` 或 `main.go` 的 Go 代码必须先写失败测试再实现（AGENTS.md §1.1
Red-Green-Refactor）。例外：纯文档（`docs/`、`*.md`、`README.md`、`AGENTS.md`、
`.github/agents/*.md`、`.github/copilot-instructions.md` 本身）以及 playbook 模板
（`examples/**/*.md`、`internal/app/prompts/*.md`）自动豁免，但 lint 仍要过。

## 2. 提交 PR 前必须经过独立子 agent 评审，且评审是**多轮协商**

完整规则在 AGENTS.md §2 / §2.4，要点：

- 评审 agent 在 [.github/agents/code-reviewer.agent.md](agents/code-reviewer.agent.md)，
  **只读、隔离 session、不替你改代码**。
- 每轮 reviewer 给出 **≤10 条** finding（带 `severity` + `confidence`，`low` 自动降级）。
- 你必须对每条 finding **三选一**回应——**不允许沉默，不允许无条件接受所有 finding**：
  - `fix`：同意，按建议改代码（且本轮改动只针对这条 finding，不要顺手带额外重构）
  - `defer`：同意但不在本 PR 范围，必须**当场开 follow-up issue 并贴号**
  - `push-back`：不同意，附技术理由（指向代码 / 测试 / 文档证据）说服 reviewer
- 下一轮 reviewer 把每条 finding 标记为 `RESOLVED` / `DEFERRED` / `WITHDRAWN` / `HELD`，
  循环直到 `APPROVED`。
- **上限 10 轮**（AGENTS.md §2.4）：到第 10 轮残余 `BLOCKER` 仍阻塞合并；残余 `MAJOR` /
  `MINOR` / `NIT` 由你按 `defer` / `push-back` 释放，PR 标题加 `[10-round-cap-released]`
  前缀，由人类合并者最终决断。
- PR 描述必须含 `## Review Round` + `## Dialog Log` 两节（见 AGENTS.md §3 模板），以及
  最终 `APPROVED` 报告全文。
- 体量保护：PR > 30 files / > 1500 行净变化 / 跨 ≥4 个 `internal/<pkg>` 时，reviewer 会直接
  出唯一 `[SCOPE]` finding 要求拆分——不要堆大 PR。

## 3. 本地校验四件套

提交前必须按顺序跑通并粘贴输出到 PR：

```bash
go vet ./...
go build ./...
go test -race -count=1 ./...
golangci-lint run --timeout=5m
```

## 4. CI 必须真的能门禁

[`.github/workflows/ci.yml`](workflows/ci.yml) 在 `pull_request: branches=[main]` 上运行 `test`
/ `lint` / `gosec` 三个 job。任何动 `internal/**` / `main.go` / `go.mod` / `go.sum` 的 PR 必须
命中前两个 job 的门禁。若 diff 命中的路径上 CI 没有对应 PR job：要么在本 PR 内补齐，要么在
PR 描述 `## CI Coverage Self-Check` 显式记 `CI Gap`（AGENTS.md §1.5）。

## 5. 跨平台 + 不硬编码路径

任何文件路径 / 外部二进制（`git`、`cloudflared` 等）/ shell 调用代码都必须考虑 Linux / macOS
/ Windows。**禁止** `C:\project` 之类 Windows-only 默认值；走 [`internal/app/config.go`](../internal/app/config.go)
的 `Config` 注入。平台差异用 `//go:build <tag>` 文件区分（参考 `internal/app/tunnel/stop_unix.go`
/ `stop_windows.go`）。

## 6. 不要在日志里打印凭证

Trello API key / token / secret、OAuth secret、GitHub PAT 等一律走脱敏（参考
`Config.Redacted()` 范式）。日志默认英语，结构化字段用 `event=<token> k1=v1 k2=v2` 风格
（`internal/app/sysevent` 现有 `Format`）。

## 7. 提交消息

Conventional Commits（`feat:` / `fix:` / `refactor:` / `docs:` / `test:` / `chore:`）。
commit footer：

```
Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>
```

---

## 不要做的事

- 不要在没有失败测试的情况下提交 Go 实现代码。
- 不要绕过独立子 agent 评审直接开 PR。
- 不要**无条件接受 reviewer 的每一条 finding**——必须对每条独立判断后 `fix` / `defer` /
  `push-back`，否则会驱动范围蔓延、永远收敛不到 `APPROVED`。
- 不要**未经授权**地修改 [`.github/agents/code-reviewer.agent.md`](agents/code-reviewer.agent.md)
  以放宽评审标准；如果本任务的目标本来就是改评审规则，按正常 PR 流程走（issue + 评审报告
  + 人类合并者审）。
- 不要在一个 PR 里夹带多个 issue 的改动；不要顺手重构任务范围外的文件。
- 不要把已有失败测试 `t.Skip()` 掉换通过；不要把测试改成只断言「不 panic」。
- 不要默默替用户做歧义选择——多种合理解读时停下来在 issue / PR 评论里列选项。
- 不要打印完整凭证；走脱敏。
- 不要在日志里混用中英文（除非日志内容本身是中文，如转发用户输入）。
- 不要硬编码 Windows 风格路径作为默认值。
- 不要在 CI workflow 里用 `paths-ignore` 把本次 diff 影响的路径排除掉以闪过门禁；也不要把关键
  job 只绑在 `push: main` 上、不走 `pull_request`。

---

> **No tests, no code. No independent review, no PR. No CI gate, no commit. Reviewer talks; implementer doesn't just nod.**
