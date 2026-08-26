# Task Spec Template

> 复制本文件 → 重命名为 `task-<语义>-<限定>.md` → 按提示填写。
> 命名强制约定：`task-<动词或语义>-[<区域>/<周期>/<限定>].md`

---

## Head Metadata（必填，3 行）

| 字段 | 值 | 说明 |
|---|---|---|
| **Task ID** | `task-xxx` | 与文件名一致；调用方唯一标识 |
| **Runtime** | `instant` \| `short` \| `long` \| `daemon` | 见下方"Runtime 标签"，决定 agent 策略 |
| **Calls** | `path/to/script arg1 arg2` | 一行写完整调用命令；agent 直接复制执行 |

### Runtime 标签（4 选 1）

- **`instant`** — < 5s，纯检查/读状态。**不**触发任何进程，只 `pgrep / ls / cat`。
  - 例子：审计 `memory/MEMORY.md` 链接、验证 pidfile。
- **`short`** — 5s ~ 1min，跑一个一次性进程，跑完即退。
  - 例子：`statistic_daily` 每日聚合。
- **`long`** — > 1min，可能跨小时/跨天。需要残程检查 + 日志末尾 tail + 幂等保护。
  - 例子：`bills_month.sh` 月度批量、`bills_daily.sh`（视数据量）。
- **`daemon`** — 常驻进程，拉起后只验证存活与 cwd，不重启。
  - 例子：`perch -D`、`runsvdir`。

> 这一个标签决定了 agent 在 §Acceptance 里要不要写"残程检查""日志末尾"等条目。

---

## 1. Background（≤ 6 行）

回答三个问题，**不要复述脚本功能**：

1. **这条线在数据流中的位置**（上游 / 下游是什么任务）
2. **触发条件**（开机后？今日产物缺失？无残程？）
3. **本次调用与同类任务的边界**（例："同脚本还有 `task-bills-daily-ph`，本 task 只覆盖月度回填"）

```
模板：
- 数据线：<上游 task> → 【本 task】 → <下游 task>
- 触发：<who -b 已就绪> + <今日/本月产物缺失> + <无残程>
- 边界：<与哪条同类任务区分开；本次参数锁定了什么>
```

---

## 2. Environment（一段命令即可）

只列**绝对路径 + 校验命令**，agent 直接复制粘贴执行：

```bash
workdir: /home/agent/...
binary/script: /abs/path/to/thing    # test -x <path>
config (if any): /abs/path/to/yaml   # test -s <path>
artifacts:
  log: <path>            # 失败时 tail 这份日志
  output: <path>         # 产物是否生成的判定点
  pidfile: <path>        # daemon 任务才有
memory refs (执行前必读):
  - ../wiz-pump-overview.md
  - ../wiz-assembler-overview.md
```

---

## 3. Constraints（一组 ❌ / ✅）

把 `Background` 里"不该做的"集中到这里，**只列与本次调用相关的**，不复述通用规则：

- ❌ **不要** <具体动作> — <会破坏什么>
- ❌ **不要** <改文件名/参数> — <混用会导致...>
- ✅ <必须保留的步骤>，移除会导致...
- ✅ <失败时只观察，不修复>（修复属于另一 task）

> 通用 Out-of-Scope（不修脚本/不互相替代启动）放在仓库根 `README.md`，这里不再重复。

---

## 4. Acceptance

合并原"Checklist + Report Schema + Done 定义"为一段。**Checklist 只列本次特有的步骤**，通用前置（who -b）已在 `Background` 写明则不必重复。

```
== <TASK_ID> @ <ISO timestamp> ==
<按 Runtime 选字段>
  instant:    probe: <key/value>   status: PASS|FAIL
  short:      outputs: <count>    log: <path>    residual: <none|pid>
  long:       attempted: <N>  ok: <N>  failed: <N>   log-tail: <last line>   residual: <none|pid>
  daemon:     pidfile: <pid>  alive: yes/no  cwd: <expected>  status: PASS|FAIL
critical: <本次唯一最关键的成功条件，比如 bills-month 的 "days attempted == 当月天数">
status: PASS|FAIL
==
```

**PASS 单一判据**：写一句话，明确 `<critical 字段> == <期望值>` 才算 PASS；其他都 FAIL，列出原因，**不尝试修复**。

---

## 反模式（不要这样写）

- ❌ 把 `bills_daily.sh` 的完整 shell 代码粘进 Background（脚本存在即可，agent 自己读）
- ❌ Checklist 里罗列通用前置（"确认系统已启动""确认脚本存在"等 5 步无信息量条目）
- ❌ Report Schema 用 Markdown 表格而不是 ASCII 块（agent 输出时会破坏对齐）
- ❌ 同时写"Out of Scope"和"Do-Not"两节重复内容
- ❌ 给 `daemon` 类任务写"残程检查"——daemon 本就应该常驻，残程是正常的

---

## 最小可工作示例（`task-audit-memory.md` 类）

```markdown
## Head Metadata
| Task ID | Runtime | Calls |
|---|---|---|
| `task-audit-memory` | `instant` | `read + grep`（只读审计） |

## 1. Background
- 数据线：人工触发 / SessionEnd 复检 → 【本 task】 → 输出审计报告字符串
- 触发：SessionEnd 时由 nanopi 主动调用，无时间窗口
- 边界：只审计 `memory/MEMORY.md` 引用的链接，不动索引本身

## 2. Environment
workdir: /home/agent/perch
memory index: memory/MEMORY.md
memory refs (执行前必读):
  - memory/MEMORY.md

## 3. Constraints
- ❌ 不要修改 `memory/MEMORY.md` 或被引用的 md
- ❌ 不要尝试修复发现的断裂链接
- ✅ 报告必须严格按 §4 的 Schema 输出

## 4. Acceptance
```
== task-audit-memory @ <ts> ==
Total: <N>  Valid: <N1>  Broken: <N2>  Empty: <N3>
critical: Broken == 0 && Empty == 0
status: PASS|FAIL
==
```
```

---

## 校验清单（写完后过一遍）

- [ ] 文件名 = `task-<x>-<y>.md`
- [ ] Head Metadata 三字段都填了
- [ ] `Runtime` 标签选了 4 个之一
- [ ] Background ≤ 6 行，回答了"位置 / 触发 / 边界"
- [ ] Environment 全是绝对路径 + 可执行校验命令
- [ ] Constraints 每条 ❌ 都附了"破坏什么"的一句话原因
- [ ] Report Schema 是 ASCII 块，不是表格
- [ ] PASS 单一判据一句话能念完