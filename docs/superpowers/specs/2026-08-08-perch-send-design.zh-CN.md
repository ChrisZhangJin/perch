# `perch-send` —— 一次性 CLI,用于 agent 主动外发邮件

**状态:** 草案
**日期:** 2026-08-08
**作者:** 与项目负责人的 brainstorming(继同日 providers-and-agents 计划之后)

## 动机

现在的 perch 是一个**被动**守护进程:它轮询 / IDLE 一个 IMAP 邮箱,把每封新邮件交给 AI agent,再在原线程里 reply。Agent —— 或者任何把 perch 当工具调用的自动化 —— **没有办法自己开新邮件线程**。

需要把 perch 当通知通道的工作流(CI 上报构建结果、长跑 agent 发日报、cron 发告警)目前没有路径:要么绕开 perch 直接 `sendmail`,要么自己写一套 SMTP 代码。

这次新增一个独立的 `perch-send` binary。它加载 daemon 用的同一份配置,经 SMTP 发一封外发邮件,然后退出。对调用者来说是 fire-and-forget —— 没有队列,没有 socket,没有 mailbox watcher。

## 使用场景

1. CI:`perch-send --to dev@x --subject "build #${BUILD}" --body @build.log --reason "build finished"`,放在 job step 后。
2. Cron / systemd timer:定时 `perch-send --to ops@x --subject "..." --body @status.json`。
3. `agent -p` 里面:agent 把报告写到临时路径,然后 shell out `perch-send --to <reporter> --body @report.md`。

发件人永远是 `cfg.Email`(daemon 监听的同一个邮箱)。收件人是一个或多个 `--to` 地址。

## 非目标

- **完全没有 inbox / mailbox 部分。** `perch-send` 不连 IMAP,只开一个出站 SMTP 会话就关。
- **没有队列 / 重试编排。** 单次 `perch-send` 调用按 daemon reply 路径同款的重试 / 退避(3 次,1s 起步)发一次。要批量就外层脚本循环调 `perch-send`。
- **perch 内不维护 rate-limit 表。** 163 / 126 / qq 在 provider 端有节流;假设"provider 自己有限流"。
- **不处理 reply-all / bounce。** 一次调用一封邮件,没有自动跟进。
- **没有模板 / Markdown 渲染。** body 原样以 `text/plain` 发。
- **没有 MCP / socket / RPC。** 独立进程,fire-and-forget。

## 调用面

```
perch-send --to <addr> [--to <addr> ...] \
           --subject <text> \
           --body "literal text" | --body @path/to/file \
           [--attach <path> ...] \
           [--reason "why I'm sending this"]

  -to         收件人(可重复)。全部必须通过 allow_send_to。
              Loop block:--to cfg.Email 被拒。
  -subject    主题。必填。
  -body       字面文本或 @<file>(最多读 MAX_PROMPT_BYTES 字节)。
  -attach     文件路径(可重复)。单文件上限 MAX_ATTACHMENT_BYTES。
  -reason     审计串。落到 `X-Perch-Reason:` header 和 slog 日志里。
              agent 主动发邮件时强烈建议填。

  exit 0   已发送(SMTP 250)
  exit 1   重试用尽后仍失败
  exit 2   配置 / 鉴权缺失(无 TTY → 走 ErrMissingFields 风格的提示)
```

binary 从新的 `cmd/perch-send/main.go` 编出来,复用 daemon 已经用的每个 internal 包:`internal/config`、`internal/provider`、`internal/replier`。**不**导入 `internal/mailbox`、`internal/runner`、`internal/agent`、`internal/app`、`internal/setup`、`internal/session`。结果就是 binary 小、启动快、零 mailbox / agent / wizard 依赖。

## 线协议

- 新增 `Message-ID:` header,服务端生成(随机 hex + `@<provider-domain>`)。
- 不带 `In-Reply-To:` / `References:` —— 这是全新的线程。
- 若提供了 `--reason`,加 `X-Perch-Reason: <reason>`。
- 常量 `X-Perch-Sender: perch-send/<version>`。

现有的 `Compose` / `ComposeWithAttachments` 已经支持"传空 inReplyTo 和 nil references 就完全不带 threading 头"这个用法。`perch-send` 只需要再加 Message-ID 和 X-Perch-Reason,其余直接复用。

## 配置新增项

| 字段          | YAML key      | Env var          | 默认   | 备注 |
|----------------|---------------|------------------|---------|-------|
| `AllowSendTo`  | `allow_send_to` | `ALLOW_SEND_TO` | `[]`    | 字面值 / 正则白名单。**空 = 拒绝所有出站。** 跟 `allow_from` 分开,两个策略可以独立演化。 |

`allow_send_to` 用与 `allow_from` 相同的字面值 / 正则语法(字面地址,或 `s"..."` 正则)。正则编译失败立即 fail binary —— 与 `allow_from` 同样的 fail-closed 姿态。

Loop block:`--to` 在 lowercase 之后与 `cfg.Email` 比对;命中返回 `exit 1` 并给出清晰提示。

## 审计日志

每次发送产出一条 INFO 行:

```
INFO  send  to=<recipients> subject=<...> size=<bytes> reason=<reason or "-"> session=<n/a>
```

`session` 是 `n/a`,因为 `perch-send` 不加载 session。用户想按 agent / workflow 追溯的话,把身份塞 `--reason` 里。

## 失败语义

- 3 次重试后 SMTP 仍失败 → `exit 1`,stderr 打印最后一次错误。
- 收件人不在 `allow_send_to` → `exit 1`,根本不开 SMTP 会话。
- `--to cfg.Email` → `exit 1`,根本不开 SMTP 会话。
- 配置 / 鉴权缺失 → `exit 2`,提示先交互跑 `perch` 或设环境变量。
- 附件缺失或超大小 → `exit 1`,在开 SMTP 前就退出。

## 架构

```
cmd/perch-send/
    main.go              # CLI flag parse + 调度(没有 subcommand 层;这本身就是唯一模式)

internal/replier/
    compose_new.go       # ComposeNew(fromAddr, to []string, subject, reason, body, attachments) ([]byte, error)
    compose_new_test.go  # 断言:新 Message-ID、无 In-Reply-To / References、X-Perch-Reason 存在 / 不存在

internal/config/
    config.go            # +AllowSendTo 字段、+allow_send_to yaml 字段、+ALLOW_SEND_TO 环境变量,
                         # +Load 时 allowSendTo 正则编译失败立即返回错误
    config_test.go       # +env override 测试、+正则编译失败测试
    deprecation.go       # (不变 —— 这里没有要废弃的 key)
```

`cmd/perch-send/main.go`(~80 行)严格按下列顺序 wiring:

1. `flag.Parse()` —— 见上面的调用面。
2. `cfg, err := config.Load(*configPath)` —— 与 daemon 同样的搜索路径。
3. 把所有 `--to` 与 `cfg.AllowSendTo`、`cfg.Email` 校验。
4. 读 `--body`(字面值或 `@file`)。
5. 每个 `--attach` 校验存在且不超过 `MAX_ATTACHMENT_BYTES`。
6. `p, _ := provider.Lookup(cfg.ProviderName)`。
7. `r := replier.New(cfg, p.SMTPAddr)`。
8. `msg, _ := replier.ComposeNew(cfg.Email, toAddrs, subject, reason, body, attachments)`。
9. `err := r.SendRaw(msg)`。
10. 打日志 + 退出。

`r.SendRaw` 是 `*Replier` 上的一个新方法,接受预先组装的字节而不是 (to, subject, body) —— 这样就避免再跑现有的 `Reply` 那一套(它默认按 reply 路径走,会假设有 threading header)。`SendRaw` 仍然使用同一套 retry / backoff / `defaultDial` / `dial` 注入点,现有测试也都能复用。

## 向后兼容

现有 YAML key 行为一个都不改。`allow_send_to` 是新 key,默认 `[]`,这意味着**刚升完级的 perch 如果没设它,会拒绝所有 `perch-send` 调用**,并打印清晰错误("no recipients in allow_send_to; add yours to perch.yaml")。这是有意的 —— 出站邮件是个新能力,默认 fail-closed。

`perch`(daemon)本体不变。不加 `send` subcommand。

## 技术栈

不引入第三方依赖。与现在一样的 Go 1.25 / `gopkg.in/yaml.v3` / `golang.org/x/term`。