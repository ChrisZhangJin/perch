package message

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	cn := "任务完成了" // 5 runes, 15 bytes
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"no limit", cn, 0, cn},
		{"under limit", cn, 100, cn},
		{"exact", cn, 15, cn},
		// 13 lands inside the 5th rune (bytes 12-14); back off to 12.
		{"mid-rune", cn, 13, "任务完成"},
		{"mid-rune 2", cn, 14, "任务完成"},
		{"on boundary", cn, 12, "任务完成"},
		{"ascii", "hello", 3, "hel"},
		{"drops everything", cn, 1, ""},
	} {
		got := TruncateUTF8(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("%s: TruncateUTF8(%q, %d) = %q, want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: result is not valid UTF-8: %q", tc.name, got)
		}
	}
}

// TestTruncateUTF8NeverSplitsRune is the property the byte-slice version got
// wrong: for CJK text, most cut points land mid-rune.
func TestTruncateUTF8NeverSplitsRune(t *testing.T) {
	s := strings.Repeat("中文测试", 20)
	for max := 1; max <= len(s); max++ {
		if got := TruncateUTF8(s, max); !utf8.ValidString(got) {
			t.Fatalf("max=%d produced invalid UTF-8: %q", max, got)
		}
	}
}

func TestStripQuotedClientFormats(t *testing.T) {
	const reply = "收到，按你说的改。"

	for name, body := range map[string]string{
		"foxmail_qq": reply + `

------------------ 原始邮件 ------------------
发件人: "Chris"<chris@wiz.ai>;
发送时间: 2026年8月23日(星期六) 下午2:30
收件人: "agent"<agent@163.com>;
主题: Re: 报表

麻烦看一下这个报表。`,

		"chinese_attribution": reply + `

在 2026年8月23日 星期六, Chris <chris@wiz.ai> 写道：
> 麻烦看一下这个报表。
> 谢谢。`,

		"chinese_attribution_yu": reply + `

于 2026-08-23 14:30, Chris 写道:
> 麻烦看一下这个报表。`,

		"gmail": reply + `

On Sat, Aug 23, 2026 at 2:30 PM Chris <chris@wiz.ai> wrote:
> please look at this report`,

		"gmail_wrapped": reply + `

On Sat, Aug 23, 2026 at 2:30 PM Chris <chris@wiz.ai>
wrote:
> please look at this report`,

		"outlook_english": reply + `

-----Original Message-----
From: Chris <chris@wiz.ai>
Sent: Saturday, August 23, 2026 2:30 PM
To: agent@163.com
Subject: Re: report

please look at this report`,

		"outlook_header_block": reply + `

发件人: Chris <chris@wiz.ai>
发送时间: 2026年8月23日 14:30
收件人: agent@163.com
主题: Re: 报表

麻烦看一下这个报表。`,

		"underscore_rule": reply + `

________________________________
From: Chris <chris@wiz.ai>
Sent: Saturday, August 23, 2026

please look at this report`,

		"forward_banner": reply + `

---------- 转发邮件 ----------
发件人: Chris <chris@wiz.ai>
主题: 报表

麻烦看一下。`,

		"bare_quote_lines": reply + `

> 麻烦看一下这个报表。
> 谢谢。`,

		"quote_with_attribution_line": reply + `

Chris <chris@wiz.ai> 写道：
> 麻烦看一下这个报表。`,
	} {
		t.Run(name, func(t *testing.T) {
			got, removed := StripQuoted(body)
			if removed == 0 {
				t.Fatalf("nothing stripped from:\n%s", body)
			}
			if !strings.Contains(got, reply) {
				t.Errorf("the new content was lost; got:\n%s", got)
			}
			for _, leak := range []string{"报表", "report", "写道", "wrote:", "原始邮件", "转发邮件"} {
				if strings.Contains(got, leak) {
					t.Errorf("quoted marker/content %q survived:\n%s", leak, got)
				}
			}
		})
	}
}

// TestStripQuotedKeepsInlineReplies pins the reason only a TRAILING run of
// ">" lines is removed: in an inline reply the sender answers between the
// quoted paragraphs, so cutting at the first ">" would throw away most of
// what they actually wrote.
func TestStripQuotedKeepsInlineReplies(t *testing.T) {
	body := `> 第一个问题是什么？
这是第一个回答。

> 第二个问题呢？
这是第二个回答。
`
	got, _ := StripQuoted(body)
	for _, want := range []string{"这是第一个回答。", "这是第二个回答。"} {
		if !strings.Contains(got, want) {
			t.Errorf("inline reply %q was lost:\n%s", want, got)
		}
	}
}

// TestStripQuotedKeepsBareForward: when the quote IS the whole message
// (a forward with no new text), stripping would leave an empty prompt.
// Returning the original is the lesser evil.
func TestStripQuotedKeepsBareForward(t *testing.T) {
	for name, body := range map[string]string{
		"only marker": "------------------ 原始邮件 ------------------\n发件人: Chris\n\n看看这个。",
		"only quotes": "> 看看这个。\n> 谢谢。\n",
		"whitespace":  "   \n\n> 看看这个。\n",
	} {
		got, removed := StripQuoted(body)
		if got != body || removed != 0 {
			t.Errorf("%s: a quote-only body must be returned unchanged, got %d removed:\n%s", name, removed, got)
		}
	}
}

// TestStripQuotedLeavesCleanBodyAlone: no quote, no change. Guards against a
// marker regex that is too eager on ordinary prose.
func TestStripQuotedLeavesCleanBodyAlone(t *testing.T) {
	for name, body := range map[string]string{
		"plain":            "帮我把昨天的报表导出成 CSV，谢谢。\n",
		"mentions from":    "The error came from: the parser. Please fix it.\n",
		"mentions wrote":   "I wrote: a small script yesterday, see attached.\n",
		"mentions on":      "On call rotation starts Monday. Please confirm.\n",
		"dashes in prose":  "Steps:\n-- first\n-- second\n",
		"chinese mentions": "他在邮件里写道，这个方案可行。请继续。\n",
		"empty":            "",
	} {
		got, removed := StripQuoted(body)
		if removed != 0 || got != body {
			t.Errorf("%s: clean body was modified (%d bytes removed):\ngot:  %q\nwant: %q",
				name, removed, got, body)
		}
	}
}

// TestStripQuotedCutsAtEarliestMarker: a body carrying more than one marker
// (a reply to a forward) must be cut at the first one, not the last.
func TestStripQuotedCutsAtEarliestMarker(t *testing.T) {
	body := `我的回复。

在 2026年8月23日, Chris 写道：
> 转发给你看看
> ---------- 转发邮件 ----------
> 原始内容
`
	got, _ := StripQuoted(body)
	if !strings.Contains(got, "我的回复。") {
		t.Errorf("new content lost:\n%s", got)
	}
	if strings.Contains(got, "转发") || strings.Contains(got, "写道") {
		t.Errorf("cut happened at the wrong marker:\n%s", got)
	}
}

// TestStripQuotedDoesNotCutOnProseMidBody is the real precision guard.
//
// TestStripQuotedLeavesCleanBodyAlone cannot do this job on its own: its
// false-positive candidates sit on the FIRST line, so an over-eager marker
// cuts at offset 0, the result is blank, and the bare-forward guard returns
// the original — the test passes without the regex being correct. Here the
// trigger line sits in the middle, so a bad marker leaves non-blank content
// and silently eats the rest of what the human wrote.
//
// Precision matters more than recall here: a false positive deletes the
// sender's own words and the agent answers the wrong question, while a false
// negative just leaves redundant text in the prompt.
func TestStripQuotedDoesNotCutOnProseMidBody(t *testing.T) {
	const head = "先看这段说明。\n"
	const tail = "然后请帮我改一下，谢谢。\n"

	for name, middle := range map[string]string{
		// "wrote:" in prose, no date — not an attribution.
		"prose wrote":     "I wrote: a small helper script yesterday.\n",
		"prose on":        "On reflection I wrote: it needs a retry.\n",
		"on call":         "On call rotation starts Monday: please confirm.\n",
		"from in prose":   "From: the logs it looks fine.\n",
		"chinese wrote":   "他在邮件里写道：这个方案可行。\n",
		"chinese on":      "在会议上他写道：这个方案可行。\n",
		"short dashes":    "-- 第一步\n-- 第二步\n",
		"few underscores": "____\n",
		"date no wrote":   "在 2026年8月23日 我们讨论过这件事。\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := head + middle + tail
			got, removed := StripQuoted(body)
			if removed != 0 {
				t.Errorf("cut %d bytes from prose; the sender's text after the trigger is gone.\ntrigger: %q\ngot:\n%s",
					removed, strings.TrimSpace(middle), got)
			}
			if !strings.Contains(got, strings.TrimSpace(tail)) {
				t.Errorf("content after the trigger line was lost:\n%s", got)
			}
		})
	}
}

// TestStripQuotedStillCutsRealAttributions is the other half: tightening the
// markers to require a date must not stop them matching what clients emit.
func TestStripQuotedStillCutsRealAttributions(t *testing.T) {
	const reply = "好的，我看一下。\n"
	for name, attr := range map[string]string{
		"cn full":   "在 2026年8月23日 星期六, Chris <chris@wiz.ai> 写道：",
		"cn yu":     "于 2026-08-23 14:30, Chris 写道:",
		"gmail":     "On Sat, Aug 23, 2026 at 2:30 PM Chris <chris@wiz.ai> wrote:",
		"gmail iso": "On 2026-08-23, Chris wrote:",
	} {
		t.Run(name, func(t *testing.T) {
			body := reply + "\n" + attr + "\n> 原始内容 original content\n"
			got, removed := StripQuoted(body)
			if removed == 0 {
				t.Fatalf("real attribution not recognised: %q", attr)
			}
			if strings.Contains(got, "原始内容") || strings.Contains(got, "original content") {
				t.Errorf("quoted content survived:\n%s", got)
			}
			if !strings.Contains(got, "好的，我看一下。") {
				t.Errorf("reply lost:\n%s", got)
			}
		})
	}
}

// --- loop-protection headers ------------------------------------------------

func parseBody(t *testing.T, extraHeaders string) *Message {
	t.Helper()
	raw := "From: bot@x.com\r\nSubject: hi\r\nMessage-ID: <b1@x>\r\n" +
		extraHeaders + "Content-Type: text/plain\r\n\r\nbody\r\n"
	m, err := Parse(strings.NewReader(raw), 1, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func TestIsAutomatedDetects(t *testing.T) {
	for name, tc := range map[string]struct {
		headers string
		want    bool
	}{
		// RFC 3834: absent or "no" means human-generated.
		"absent":                {"", false},
		"auto-submitted no":     {"Auto-Submitted: no\r\n", false},
		"auto-generated":        {"Auto-Submitted: auto-generated\r\n", true},
		"auto-replied":          {"Auto-Submitted: auto-replied\r\n", true},
		"auto-notified":         {"Auto-Submitted: auto-notified\r\n", true},
		"with parameters":       {"Auto-Submitted: auto-replied; owner-token=abc\r\n", true},
		"mixed case":            {"Auto-Submitted: Auto-Replied\r\n", true},
		"precedence bulk":       {"Precedence: bulk\r\n", true},
		"precedence list":       {"Precedence: list\r\n", true},
		"precedence junk":       {"Precedence: junk\r\n", true},
		"precedence urgent":     {"Precedence: urgent\r\n", false},
		"list-id":               {"List-Id: <dev.example.com>\r\n", true},
		"list-unsubscribe":      {"List-Unsubscribe: <mailto:x@y>\r\n", true},
		"list-post":             {"List-Post: <mailto:x@y>\r\n", true},
		"empty list-id ignored": {"List-Id: \r\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, why := parseBody(t, tc.headers).IsAutomated()
			if got != tc.want {
				t.Errorf("IsAutomated() = %v (%q), want %v for headers %q",
					got, why, tc.want, tc.headers)
			}
			if got && why == "" {
				t.Error("a positive result must explain itself for the log")
			}
		})
	}
}

// TestPerchsOwnRepliesAreDetectable closes the loop with the replier: perch
// stamps Auto-Submitted: auto-replied on what it sends, so a second perch
// receiving it must classify it as automated. Two instances that both do this
// cannot ping-pong.
func TestPerchsOwnRepliesAreDetectable(t *testing.T) {
	m := parseBody(t, "Auto-Submitted: auto-replied\r\n")
	if automated, _ := m.IsAutomated(); !automated {
		t.Error("perch must recognise the header perch itself sends")
	}
}
