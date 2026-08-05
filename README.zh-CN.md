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
- 🔒 **安全闸门在代码里** — 发件人白名单 + 去重在 agent 运行**之前**由 Go 强制执行,绝不塞进 prompt。
- 🧵 **对话记忆** — 同一线程内的回复会续用同一个 agent 会话(`--resume`)。
- 📡 **自动推送或轮询** — 服务器支持时用 IMAP `IDLE` 近实时;不支持时(如 163)自动降级为轮询。
- 🤖 **与 agent 无关** — 默认 `claude`,但任何 `-p "<prompt>"` 的 CLI 都能用。
- 🪶 **小而静态** — 一个小 Go 二进制,`CGO_ENABLED=0`,无运行时依赖。

## 🗺️ 工作原理

单条 IMAP 连接,交替循环:

```text
ProcessUnseen(拉取并处理所有未读邮件)
   → IDLE,最多等待 POLL_INTERVAL
       (来新邮件立刻返回 = 近实时;否则超时,当作兜底轮询)
   → 重复
```

每封邮件:`解析 → 去重(\Seen + 内存集合) → 白名单 → 把邮件线程映射到稳定的 agent 会话
→ 运行 agent -p … → 原线程 SMTP 回信 → 标记 \Seen`。

## 🔒 安全模型

发件人白名单和消息去重都在 Go 里、在 agent 被拉起**之前**强制执行。agent 永远看不到非白名单
发件人的邮件。这套逻辑是确定性代码,**不是** prompt——邮件正文没法把 perch 说服绕过它。

## 📦 安装

```bash
go build -o perch ./cmd/perch      # 或:make build (在 ./bin 生成静态二进制)
```

## ⚙️ 配置(环境变量)

| 变量 | 必填 | 默认 | 说明 |
|---|:---:|---|---|
| `AGENT_EMAIL` | ✅ | — | perch 监听的邮箱 |
| `AGENT_AUTH_CODE` | ✅ | — | 邮箱**授权码**(163 授权码),**不是**登录密码 |
| `ALLOW_FROM` | ⚠️ | — | 逗号分隔的允许发件人;为空 = **拒绝所有人** |
| `CLAUDE_BIN` | | `claude` | 要拉起的 agent CLI |
| `CLAUDE_WORKDIR` | | `.` | agent 的工作目录 |
| `CLAUDE_PERMISSION_MODE` | | `acceptEdits` | 让 agent 的工具非交互式运行 |
| `IMAP_ADDR` | | `imap.163.com:993` | 隐式 TLS |
| `SMTP_ADDR` | | `smtp.163.com:465` | 隐式 TLS |
| `POLL_INTERVAL` | | `60s` | 轮询间隔 / IDLE 保活 |
| `TASK_TIMEOUT` | | `30m` | 超时后 SIGTERM→5s→SIGKILL |
| `MAX_PROMPT_BYTES` | | `65536` | 截断超大邮件正文 |
| `SESSION_STORE` | | 临时文件 | 线程→会话 UUID 映射(JSON) |
| `TLS_INSECURE_SKIP_VERIFY` | | `false` | **仅开发/测试** — 接受自签名证书 |

## 🚀 运行

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=你的授权码 \
ALLOW_FROM=alice@163.com \
CLAUDE_WORKDIR=/home/agent/workspace \
./perch
```

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

## 📄 许可

MIT — 见 [LICENSE](LICENSE)。
