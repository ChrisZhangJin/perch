<div align="center">

# 🐦 perch

**让你的 agent,栖息在你的收件箱上。**

把一个普通邮箱变成触发 CLI 编码 agent 的入口。

[![CI](https://github.com/ChrisZhangJin/perch/actions/workflows/ci.yml/badge.svg)](https://github.com/ChrisZhangJin/perch/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Status](https://img.shields.io/badge/status-prototype-orange)
![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS-lightgrey)

[English](README.md) · [中文](README.zh-CN.md) · [设计文档](docs/DESIGN.md) · [测试指南](docs/TESTING.md)

</div>

---

**perch** 是一个极小的 Go 常驻程序:它监听一个邮箱,**仅对白名单发件人**,把每封来信交给一个
agent(默认 Claude Code,任何 CLI 都行)处理,并在同一邮件线程内回信。

它存在的原因是:像 Claude Code 这样的 CLI agent **没有"来邮件就唤醒"的机制**——它是无状态的
请求→响应工具。perch 就是那个永远在线的**信使 + 安全闸门**:负责监听、鉴权、把 agent 当 worker
拉起来干活,再把结果发回去。

> 💡 灵感来自 [AAMP](https://github.com/larksuite/aamp) 协议"用邮件做 agent 协作"的思路,但刻意
> 做到极简且独立:没有协议头、没有配对、没有 JMAP——只有 IMAP/SMTP + 一个白名单。

## ✨ 特性

- 📬 **邮件 → agent** — 每封白名单邮件都变成一个 agent 任务,答案原线程回信。
- 🔒 **安全闸门在代码里** — 发件人白名单 + 去重在 agent 运行**之前**由 Go 强制执行,绝不塞进 prompt。白名单接受字面量 **和** 正则(`s"..."`)。
- 🧵 **对话记忆** — 同一线程内的回复会续用同一个 agent 会话(`--resume`)。
- 📡 **自动推送或轮询** — 服务器支持时用 IMAP `IDLE` 近实时;不支持时(如 163)自动降级为轮询。
- 🤖 **与 agent 无关** — 默认 `claude`,但任何 `-p "<prompt>"` 的 CLI 都能用。
- 🎩 **常驻角色** — `append_system_prompt` 把一个持久角色(客服台、分诊员)追加到 agent 自身的 system prompt 上,支持从文件读、每封邮件重新读。见 [常驻角色](#-常驻角色append_system_prompt)。
- 🪝 **收信脚本钩子** — 每封被接受的邮件都可以先调一次你自己的脚本(`<email_id> <subject> <body> <sender> <new_thread|reply_thread>`):记录新线程、推手机通知、打点计数。见 [脚本钩子](#-脚本钩子)。
- ⚙️ **分层配置** — YAML 文件、环境变量、内置默认。优先级:env > yaml > default。密钥只能从 env 来。
- 🪶 **小而静态** — 一个小 Go 二进制,`CGO_ENABLED=0`,无运行时依赖。如果系统里有 [UPX](https://upx.github.io/),`make build` 会自动压缩到 ~2.8 MB(linux/amd64)。

## 🗺️ 工作原理

启动时 perch 根据 `internal/provider/registry.go` 里每个供应商的 `Capabilities` 字段
挑一条传输策略:

| 供应商 | `SupportsIDLE` | 策略 | 行为 |
|---|---|---|---|
| 163、126 | `false` | **P** — Poller(短连接) | 每次 `FetchUnseen` / `MarkSeen` 都是一次全新的 IMAP 连接:Dial → 操作 → Logout → Close。服务器宕机或重启只会让下一次 dial 失败,**不会留下永远死掉的连接**。 |
| qq(以及未来任何声明支持 IDLE 的供应商) | `true` | **L** — IMAPMailbox(长连接 + IDLE) | 一条连接复用,`FetchUnseen` 和 `IDLE` 交替循环(IDLE 以 `POLL_INTERVAL` 为兜底超时)。新邮件 1-3 秒内到达,而不是等下一个轮询 tick。 |

将来 `S` 模式(server-push webhook,如 Gmail Pub/Sub)是 `BuildStrategy` 里加一行的事——等真有需要再做。

```text
P 模式: for { sleep(POLL_INTERVAL); fetch(Dial→UIDSearch→Fetch→Logout→Close); mark seen }
L 模式: for { IDLE 最长到 POLL_INTERVAL(有 EXISTS 立刻返回); fetch; }   // 共享连接
```

每封邮件:`解析 → 去重(\Seen + 内存集合) → 白名单 → 把邮件线程映射到稳定的 agent 会话
→ 运行 agent -p … → 原线程 SMTP 回信 → 标记 \Seen`。

**agent 跑不起来的时候**(二进制不存在、崩溃、超时、额度用完),发件人收到的是一封简短的
"我现在不在工位上,请稍后再发一次"的通知,而**不是** Go 的错误信息。错误本身以 ERROR 级别
留在日志里给运维看:`exec: "nanopi": executable file not found in $PATH` 对写信的人毫无
意义,而且会泄露二进制名、路径和 stderr。连续两次跑出空回复也是同样处理:用人话请对方换个
说法再发一次。这两种通知都是 perch 自己写的(没有 agent 参与),所以语言由来信决定——
中文来、中文回。

**重要:** 同一邮件线程里的回复共享同一个 agent 会话,perch 按 `Message-ID` 去重。如果
你发了一封邮件,然后几秒后又在原邮件上 reply,这两封会在同一次 IMAP fetch 里到达——
perch 会处理**最新那一封**(包含你最新的意图),并静默去重掉较早那一封。这一步以 INFO
级别打日志,带上 `subject`,你就能区分"perch 没看到我的邮件"和"perch 处理了 thread
里更新的那一封"。

## 🔒 安全模型

发件人白名单和消息去重都在 Go 里、在 agent 被拉起**之前**强制执行。agent 永远看不到非白名单
发件人的邮件。这套逻辑是确定性代码,**不是** prompt——邮件正文没法把 perch 说服绕过它。

## 📦 安装

```bash
go build -o perch ./cmd/perch      # 或:make build (在 ./bin 生成静态二进制;如有 UPX 会自动压缩)
```

如果 `PATH` 里有 [UPX](https://upx.github.io/),`make build` 会跑 `upx --best` 把刚链接
出来的二进制压到 ~2.8 MB(linux/amd64)。不想压缩就 `PERCH_NO_UPX=1 make build`。
UPX 安装:`apt-get install upx-ucl`。

## ⚙️ 配置

perch 从三层加载配置,优先级由高到低:

1. **环境变量** — 名字与之前一致(`AGENT_EMAIL`、`POLL_INTERVAL` 等)。
2. **YAML 文件** — 先找当前目录的 `perch.yaml`,再找 `~/.perch/perch.yaml`。
   也可以用 `--config <path>` 或 `PERCH_CONFIG=<path>` 指定路径。仓库根目录里有一份样板。
3. **内置默认值** — 163 的合理默认;可以用 YAML 或环境变量覆盖。

**密钥**(`AGENT_AUTH_CODE`)只能从环境变量注入,绝不会从 YAML 里读。

| 变量 / YAML key | 必填 | 默认 | 说明 |
|---|:---:|---|---|
| `AGENT_EMAIL` | ✅ | — | perch 监听的邮箱。环境变量优先,否则读 YAML 里的 `email:`(首次运行向导写入)。 |
| `AGENT_AUTH_CODE` | ✅ | — | 邮箱**授权码**(163 授权码),**不是**登录密码。只能从环境变量注入,绝不写盘。 |
| `allow_from` / `ALLOW_FROM` | ⚠️ | — | 列表 / 逗号分隔的允许发件人。字面量 `*` 表示**接受所有人**(这是 setup 向导写入的 onboarding 默认值,上线前请收紧);否则空列表 = **拒绝所有人**(fail-closed,env-only 部署下的安全默认值)。YAML 条目可以是字面量 (`alice@163.com`),也可以是 `s"..."` 包裹的正则(如 `s".+@(foo\|bar)\.example\.com"`、`s".*agent.*@qq\.com"`、`s"(?i).+@trusted\.org"`)。正则启动时编译,坏正则立刻让 gate 构建失败(perch 绝不在 fail-open 状态下启动)。`ALLOW_FROM` 环境变量只承载字面量。 |
| `email_provider.name`       | —                 | `163`            | `163` / `126` / `qq` — 自动推导端点 |
| `ai_agent.name`             | —                 | `claude`         | `claude` / `nanopi` / `pi` — 自动推导二进制 |
| `ai_agent.workdir`          | —                 | `.`              | 拉起的 agent 工作目录 |
| `ai_agent.permission_mode`  | —                 | `acceptEdits`    | 仅 claude；nanopi/pi 忽略 |
| `ai_agent.append_system_prompt` / `APPEND_SYSTEM_PROMPT` | — | — | 追加到 **agent 自身 system prompt** 的常驻角色设定。可以是文本,也可以是文件路径。见 [常驻角色](#-常驻角色append_system_prompt) |
| `poll_interval` / `POLL_INTERVAL` | | `60s` | 轮询间隔 / IDLE 保活 |
| `task_timeout` / `TASK_TIMEOUT` | | `30m` | 超时后 SIGTERM→5s→SIGKILL |
| `max_prompt_bytes` / `MAX_PROMPT_BYTES` | | `65536` | 截断超大邮件正文 |
| `max_attachment_bytes` / `MAX_ATTACHMENT_BYTES` | | `52428800` (50 MB) | 单个附件大小上限;超限附件被丢弃(解析仍继续) |
| `session_store` / `SESSION_STORE` | | 临时文件 | 线程→会话 UUID 映射(JSON) |
| `hooks.on_email` / `ON_EMAIL_HOOK` | | — | 每封被接受的邮件在跑 agent 之前调用的脚本。见 [脚本钩子](#-脚本钩子) |
| `hooks.timeout` / `HOOK_TIMEOUT` | | `30s` | 单次钩子运行上限(SIGTERM→5s→SIGKILL) |
| `tls_insecure_skip_verify` / `TLS_INSECURE_SKIP_VERIFY` | | `false` | **仅开发/测试** — 接受自签名证书 |

## 🎩 常驻角色(`append_system_prompt`)

perch 每封邮件构造的 prompt 讲的是**这一封**邮件。`append_system_prompt` 是另一个维度:
每次运行都追加到 **agent 自身的 system prompt** 上,用来定义一个跨邮件存在的角色——
客服台、构建看护、on-call 分诊。

```yaml
# perch.yaml
ai_agent:
  name: claude
  workdir: /root/perch
  append_system_prompt: /root/perch/helpdesk.md   # 一个路径……
  # append_system_prompt: |                       # ……或者直接写文本
  #   You are the kulink support desk.
  #   Task definitions live in ./tasks/.
```

取值是**文本或路径**(和 pi 那个 flag 本身的规则一致):单行且指向一个可读文件时按文件读,
其他情况按字面文本用。文件会**每封邮件重新读一次**,所以改角色定义不用重启,下一封邮件
就生效。启动日志会说清楚它按哪种方式解析,路径打错时一眼能看出来:

```
INFO "append_system_prompt enabled" agent=claude source="file /root/perch/helpdesk.md" bytes=1683
```

**agent 不支持这个 flag 时是被检测出来,而不是直接炸。** perch 启动时跑一次
`<agent> --help`,找 `--append-system-prompt`。claude 和 pi 有;nanopi 正在加。
找不到就打一条 WARN 说明丢掉了什么,然后照常运行——而不是让每封邮件都死在未知 flag 上;
等 agent 哪天支持了,perch 会自动开始用,不需要升级 perch 也不用改配置。

### 客服台模板

[`helpdesk.md.example`](helpdesk.md.example) 是一份填空式的客服台角色定义:从工作目录下的
`tasks/` 读任务定义、信息不全时主动追问而不是猜、不自己编造政策和价格、该转人工就转人工、
不把内部信息写进回复。[`tasks.example/complaint-intake.md`](tasks.example/complaint-intake.md)
是单个任务文件的样例。

```bash
cp helpdesk.md.example helpdesk.md          # 然后把每个 [[PLACEHOLDER]] 换掉
grep -n '\[\[' helpdesk.md                  # 应该什么都不输出
cp -r tasks.example /root/perch/tasks       # <workdir>/tasks
```

```yaml
ai_agent:
  workdir: /root/perch
  append_system_prompt: /root/perch/helpdesk.md
```

这个文件是**原样**发给 agent 的。两个由此而来的注意点:

- **一定要填完。** 如果加载到的 prompt 里还有 `[[PLACEHOLDER]]` 或者样例顶部那段 setup
  注释,perch 启动时会打 WARN——没改过的模板等于在告诉 agent "转给 `[[ESCALATION CONTACT]]`"。
  改完文件,下一封邮件就不再报警,不用重启。
- **别放在 agent 的 workdir 里面。** 在 `workdir` 内部,SAFETY PROTOCOL 把这个文件当成可以
  随便写的,agent 能改自己的角色定义。放 `~/.perch/helpdesk.md` 或 `/etc/perch/helpdesk.md`
  仍然读得到(任何位置的只读访问都是允许的),但改不了。

另外保留角色定义里让 agent 依赖的那五个任务文件小标题。

角色设定要短,而且**不要**在里面重复 per-email 的那些契约(回复格式、语言、安全、
grounding)——perch 已经在发了,角色设定跟它们冲突的时候,模型只能挑一边站。

## 🪝 脚本钩子

`hooks.on_email` 指向一个脚本,perch 在**每封被接受的邮件**上、跑 agent 之前调用它一次。
这是给 perch 本身不做的副作用留的接缝:记录每个新线程、推一条通知、打个点。

```yaml
# perch.yaml
hooks:
  on_email: /home/agent/record.sh
  timeout: 30s
```

脚本收到五个位置参数:

| 参数 | 含义 |
|---|---|
| `$1` | `email_id` — 邮件的 `Message-Id` 头(如 `<abc@163.com>`),由发件方邮件服务商生成。邮件没有这个头时是**空字符串**——参数照样传,所以 `$2`…`$5` 的位置永远不会前移。 |
| `$2` | `subject` 主题 |
| `$3` | `body` — `text/plain` 正文,原样(未剥引用历史),超过 64 KiB 截断 |
| `$4` | `sender` — 小写发件人地址,如 `alice@163.com` |
| `$5` | `new_thread`(perch 此前没有该线程的会话,即新线程)或 `reply_thread` |

```bash
#!/usr/bin/env bash
# record.sh — 每个新线程记一行
[ "$5" = new_thread ] || exit 0
printf '%s\t%s\t%s\n' "$(date -Is)" "$4" "$2" >> "$HOME/perch-threads.tsv"
```

```bash
chmod +x /home/agent/record.sh
```

约定:

- **只是观察者,不是闸门。** 脚本不存在、退出码非 0、或者跑超过 `hooks.timeout`,
  都只记一条 WARN 日志,邮件照常处理。钩子无法阻止 perch 回信。
- **只看得到可信发件人。** 钩子在 `allow_from` 与防回环检查**之后**触发,
  被 perch 丢掉的邮件不会跑到你的脚本里。
- 同步执行,`cwd` = `ai_agent.workdir`,继承 perch 的环境变量。请让它跑得快——
  排在后面的邮件在等它。

### 示例:把每封邮件写进飞书多维表格(Bitable)

[`scripts/hooks/lark_bitable.py`](scripts/hooks/lark_bitable.py) 会给每封邮件在
Bitable 里追加一行(`SendTime` / `Subject` / `Content` / `TicketNo` / `sender`)。
只用 Python 3 标准库,perch 所在机器不需要装任何依赖。

```bash
# 飞书自建应用的凭证(不会进版本库,.gitignore 已忽略 .env)
cat > .env <<'EOF'
APP_ID=cli_xxxxxxxx
APP_SECRET=xxxxxxxx
EOF
```

```yaml
# perch.yaml — 两个 id 都能从 Bitable 的 URL 里读出来:
#   https://<host>/base/<APP_TOKEN>?table=<TABLE_ID>&view=...
hooks:
  on_email: /path/to/perch/scripts/hooks/lark_bitable.py
```

换自己的表就设 `LARK_APP_TOKEN` / `LARK_TABLE_ID`;`LARK_ONLY_NEW_THREADS=1`
表示只记录每个线程的第一封。tenant_access_token 会缓存在磁盘上(0600,文件名用
app id 的摘要)并复用到快过期为止,所以大多数邮件只需要一次 HTTP 调用而不是两次
(`LARK_NO_TOKEN_CACHE=1` 可关闭)。其余开关(`LARK_BASE_URL` 用于 Lark 国际版、
`LARK_MAX_CONTENT`、`LARK_TIMEOUT`、`LARK_USE_ENV_PROXY` 等)见脚本开头的
docstring;缓存部分有测试:`make test-hooks`。不需要真邮件也能测——按 perch 的
调用方式直接跑:

```bash
./scripts/hooks/lark_bitable.py "<t1@163.com>" "test subject" "body text" alice@163.com new_thread
```

## 🚀 运行

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=你的授权码 \
ALLOW_FROM=alice@163.com \
./perch
```

或者把非机密的默认值放在 `perch.yaml` 里,只通过环境变量注入凭证:

```yaml
# perch.yaml
poll_interval: 5s
allow_from:
  - alice@163.com
  - s".+@(foo|bar)\.example\.com"      # 正则:foo/bar.example.com 下任何用户
ai_agent:
  workdir: /home/agent/workspace
```

```bash
AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=你的授权码 ./perch
```

### 首次运行向导 vs 无人值守重启

首次运行时,如果 stdin 是 TTY 且必填字段缺失,perch 会启动交互式向导,把非机密字段(邮箱服务商、AI agent、workdir、permission_mode、allow_from、邮箱)写到 `~/.perch/perch.yaml`(权限 0600),并通过 `ReadPassword` 提示输入授权码(不回显、不落盘)。

之后重启就只要一句:

```bash
AGENT_AUTH_CODE=你的授权码 ./perch    # 其它都已经在 YAML 里
```

如果用 systemd / launchd / cron,把密钥放到 env 文件里(权限 0600),perch 会从 YAML 读其它配置:

```ini
# /etc/systemd/system/perch.service
[Service]
EnvironmentFile=/etc/perch/env
ExecStart=/usr/local/bin/perch
Restart=on-failure
```

```bash
# /etc/perch/env (chmod 0600, chown root:root)
AGENT_EMAIL=agent@163.com
AGENT_AUTH_CODE=你的授权码
```

如果没设 `AGENT_AUTH_CODE` 就以非交互模式启动,perch 会输出提示并退出,告诉你该跑向导或写 env 文件。

### 后台运行(`--daemon`)

不想写 systemd/launchd unit 的话,`./perch --daemon` 会把自身重新派生为
一个脱离终端的进程:切断与控制终端的关联(关闭 SSH 也不会被 SIGHUP),
并把 PID 写到 `~/.perch/perch.pid`。停止时给记录的 PID 发 SIGTERM
(或 `pkill -TERM perch`)即可。正常退出时 pidfile 会被自动删除;
下次启动如果发现是残留 pidfile 也会自动清理。

```bash
./perch --daemon           # 别名:-D
kill $(cat ~/.perch/perch.pid)
```

daemon 模式下,交接完成后的所有日志都写到 `/dev/null`——暂时还没有日志
文件。带 rotation 的日志文件在 roadmap 里。

**小贴士:** 把授权码放在一个 gitignore 的文件里(仓库自带 `grant.code`),别每次输:

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE="$(tr -d '\n' < ./grant.code)" \
./perch
```

每个字段都有注释的样板配置:[`perch.yaml.example`](perch.yaml.example)。

## 🧪 测试

```bash
make test                          # 单元测试 + 离线端到端
./scripts/localtest/demo.sh reject # 用本地 Docker 邮件服务器跑真实 IMAP/SMTP
```

一条命令的 demo 会拉起一个临时的 [GreenMail](https://greenmail-mail-test.github.io/greenmail/)
服务器,发一封白名单任务邮件,用桩 agent 跑 perch,展示线程回信——再证明非白名单发件人会被忽略。
三个层级(自动化 → 本地 Docker → 真机 163)详见 [`docs/TESTING.md`](docs/TESTING.md)。

### 手动端到端

1. 建两个邮箱(如 163);开启 IMAP/SMTP;各生成一个授权码。
2. 在邮箱 **B** 上运行 perch,`ALLOW_FROM` 设为邮箱 **A** 的地址。
3. 用 **A** 给 **B** 发一封任务邮件 → B 运行 agent 并原线程回信。
4. 在同一线程里再回一封 → 续用同一个 agent 会话(保留记忆)。
5. 用非白名单地址给 B 发信 → 静默忽略(记日志,不回信)。

## 📡 收信方式:推送 vs 轮询

perch 会自动探测 IMAP `IDLE` 能力,并在启动日志里打印模式:

- 🟢 **`mode=idle+poll`** — 服务器在来新邮件时**主动推送**,perch 近实时响应(被动)。
  Gmail、Outlook、Fastmail、自建 Dovecot 等都支持。
- 🟡 **`mode=poll-only`** — 没有 IDLE,perch 每 `POLL_INTERVAL` 轮询一次(主动)。**163/126**
  就是这种(网易 IMAP 不支持 IDLE)。调小 `POLL_INTERVAL` 可降低延迟。

perch 只用 IMAP——不做 JMAP / JMAP Push,也没有各家 webhook。**国内主流邮箱不支持 JMAP**,
且 163/126 连 IMAP IDLE 都没有,所以在它们上面只能轮询。想要真正的被动推送,请换一个支持 IDLE
的邮箱(perch 会自动切换)。完整的各家能力对照表见
[`docs/DESIGN.md`](docs/DESIGN.md#receiving-passive-push-vs-active-polling)。

## 📮 邮箱说明(163 / 126)

163/126 要求登录前先发 IMAP `ID` 命令(perch 已自动发送),且登录用**授权码**而非账号密码。
主机默认 `imap.163.com:993` 和 `smtp.163.com:465`(均隐式 TLS);126 请覆盖 `IMAP_ADDR`/`SMTP_ADDR`。
网易 IMAP 没有 `IDLE`,所以 perch 在其上以 `poll-only` 运行。

## ⚠️ 状态

原型阶段。与 agent 无关的环境变量目前仍带 `CLAUDE_` 前缀;真实 IMAP/SMTP 路径通过手动测试与本地
Docker demo 验证,尚未进 CI。

**已知边界:** 如果 `replier.Reply(...)` 失败(SMTP 短暂错误),邮件会保持 `UNSEEN`,
等下一次 poll 重试——但去重是按 `Message-ID` 来的,所以同一条 RFC822 重 fetch 时
`FirstSight()=false` 直接被静默丢掉。实际 SMTP 路径稳定性够,这个没复现过,但写在这里
作为已记录的 correctness gap。

## 📄 许可

MIT — 见 [LICENSE](LICENSE)。
