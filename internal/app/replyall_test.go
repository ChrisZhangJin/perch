package app_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
)

// TestReplyAllCcsOtherRecipients: alice mails perch and bob together; the
// reply goes To alice and Cc everyone else, never to perch itself.
func TestReplyAllCcsOtherRecipients(t *testing.T) {
	run := &mailtest.ScriptedRunner{Outs: []string{"the answer"}}
	mt := newRecipientApp(t, run)

	raw := rawMailWithCc(21, testAgentEmail+", Bob@example.com", "carol@example.com, alice@163.com", "do the thing")
	if err := mt.SendRaw(21, raw); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs := mt.Replies()
	if len(rs) != 1 {
		t.Fatalf("want one reply, got %#v", rs)
	}
	if rs[0].To != "alice@163.com" {
		t.Errorf("To = %q, want alice@163.com", rs[0].To)
	}
	if want := []string{"bob@example.com", "carol@example.com"}; !reflect.DeepEqual(rs[0].Cc, want) {
		t.Errorf("Cc = %#v, want %#v", rs[0].Cc, want)
	}
	if rs[0].Kind != "reply" {
		t.Errorf("Kind = %q, want reply", rs[0].Kind)
	}
}

// TestInboundPerchAckIsSkipped: another perch's interim ack must never be
// answered — answering it is what started the ack/收到 ping-pong.
func TestInboundPerchAckIsSkipped(t *testing.T) {
	for _, kind := range []string{"ack", "notice"} {
		t.Run(kind, func(t *testing.T) {
			run := &mailtest.ScriptedRunner{Outs: []string{"收到"}}
			mt := newRecipientApp(t, run)
			raw := string(rawMailWithCc(31, testAgentEmail, "", "请稍等"))
			raw = strings.Replace(raw, "MIME-Version:", "X-Perch-Kind: "+kind+"\r\nMIME-Version:", 1)
			if err := mt.SendRaw(31, []byte(raw)); err != nil {
				t.Fatal(err)
			}
			if err := mt.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if rs := mt.Replies(); len(rs) != 0 {
				t.Errorf("perch %s was answered: %#v", kind, rs)
			}
			if len(run.Prompts) != 0 {
				t.Errorf("agent ran %d times for a perch %s", len(run.Prompts), kind)
			}
			if seen := mt.SeenUIDs(); len(seen) != 1 {
				t.Errorf("perch %s not marked seen: %v", kind, seen)
			}
		})
	}
}

func TestQuoteHistory(t *testing.T) {
	d := time.Date(2026, 9, 30, 9, 5, 0, 0, time.UTC)

	en := app.QuoteHistory("alice@163.com", "Alice", d, "line one\n\n> older", false)
	want := "\n\nOn 2026-09-30 09:05, Alice <alice@163.com> wrote:\n> line one\n>\n>> older\n"
	if en != want {
		t.Errorf("en:\n got %q\nwant %q", en, want)
	}

	zh := app.QuoteHistory("alice@163.com", "", d, "请处理", true)
	if !strings.Contains(zh, "写道：") || !strings.Contains(zh, "> 请处理") {
		t.Errorf("zh quote = %q", zh)
	}

	if got := app.QuoteHistory("alice@163.com", "", d, "  \n", false); got != "" {
		t.Errorf("empty body should quote nothing, got %q", got)
	}
}
