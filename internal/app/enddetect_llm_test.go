package app_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
	"github.com/ChrisZhangJin/perch/internal/replier"
)

// failingServer answers every call like Typesafe does from mainland China.
func failingServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
		io.WriteString(w, `{"title":"Typesafe is not available in your region.","status":451}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// llmServer is an OpenAI-format endpoint answering with the given P(over).
func llmServer(t *testing.T, probOver float64, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		label := "reply"
		if probOver >= 0.5 {
			label = "over"
		}
		lp := func(p float64) float64 {
			if p <= 0 {
				return -100
			}
			return logf(p)
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"logprobs":{"content":[{"token":%q,
			"top_logprobs":[{"token":"over","logprob":%v},{"token":"reply","logprob":%v}]}]}}]}`,
			label, label, lp(probOver), lp(1-probOver))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newFallbackApp(t *testing.T, jevURL, llmURL string) *mailtest.Mailtest {
	return newFallbackAppWithRunner(t, jevURL, llmURL, nil)
}

func newFallbackAppWithRunner(t *testing.T, jevURL, llmURL string, run app.TaskRunner) *mailtest.Mailtest {
	t.Helper()
	cfg := config.Defaults()
	cfg.MaxPromptBytes = 4096
	cfg.AgentWorkdir = t.TempDir()
	cfg.Email = "agent@163.com"
	cfg.EndDetect = true
	if jevURL != "" {
		cfg.JevAPIKey, cfg.JevAPIURL = "test-key", jevURL
	}
	if llmURL != "" {
		cfg.LLMAPIKey, cfg.LLMBaseURL, cfg.LLMModel = "llm-key", llmURL, "test-model"
	}
	mt, err := mailtest.New(cfg, []string{"alice@163.com"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mt.App().SessForTest().Resolve("<root@mailtest>"); err != nil {
		t.Fatal(err)
	}
	return mt
}

// threadMail is a continuation of <root@mailtest> whose References carry
// perchReplies earlier messages minted by perch (agent@163.com).
func threadMail(msgID, body string, perchReplies int) []byte {
	refs := []string{"<root@mailtest>"}
	for i := 0; i < perchReplies; i++ {
		refs = append(refs, fmt.Sprintf("<%d.agent@163.com>", 1000+i))
		refs = append(refs, fmt.Sprintf("<a%d@mailtest>", i))
	}
	var b strings.Builder
	b.WriteString("From: alice@163.com\r\nTo: agent@163.com\r\nSubject: Re: plan\r\n")
	fmt.Fprintf(&b, "Message-ID: %s\r\nIn-Reply-To: %s\r\nReferences: %s\r\n",
		msgID, refs[len(refs)-1], strings.Join(refs, " "))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
	return []byte(b.String())
}

func runOne(t *testing.T, mt *mailtest.Mailtest, raw []byte) []mailtest.Reply {
	t.Helper()
	if err := mt.SendRaw(20, raw); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	return mt.Replies()
}

// TestEndDetectFallsBackToLLM: Jev returns 451, the LLM is confident the
// thread is over, so the closing gets no reply.
func TestEndDetectFallsBackToLLM(t *testing.T) {
	var jc, lc int
	mt := newFallbackApp(t, failingServer(t, &jc).URL, llmServer(t, 0.97, &lc).URL)
	rs := runOne(t, mt, threadMail("<c1@mailtest>", "Likewise.", 3))
	if jc != 1 || lc != 1 {
		t.Errorf("calls jev=%d llm=%d, want 1 and 1", jc, lc)
	}
	if len(rs) != 0 {
		t.Errorf("llm said over: want no reply, got %d", len(rs))
	}
}

// TestEndDetectLLMReplacesJev: no Jev key at all, the LLM alone decides.
func TestEndDetectLLMReplacesJev(t *testing.T) {
	var lc int
	mt := newFallbackApp(t, "", llmServer(t, 0.05, &lc).URL)
	rs := runOne(t, mt, threadMail("<c2@mailtest>", "and can you check the totals?", 3))
	if lc != 1 {
		t.Errorf("llm calls = %d, want 1", lc)
	}
	if len(rs) != 1 || rs[0].Kind == replier.KindNotice {
		t.Fatalf("llm said reply: want one normal reply, got %#v", rs)
	}
}

// TestEndDetectBothFailOnAnsweredThreadSendsNotice is the 2026-10-10 loop:
// Jev 451, LLM down, and perch already answered this thread twice — perch
// sends a notice (never answered by another perch) instead of running the
// agent.
func TestEndDetectBothFailOnAnsweredThreadSendsNotice(t *testing.T) {
	var jc, lc int
	mt := newFallbackApp(t, failingServer(t, &jc).URL, failingServer(t, &lc).URL)
	rs := runOne(t, mt, threadMail("<c3@mailtest>", "Standing by.", 2))
	if len(rs) != 1 || rs[0].Kind != replier.KindNotice {
		t.Fatalf("want exactly one notice, got %#v", rs)
	}
	if !strings.Contains(rs[0].Body, "something is wrong with my email") {
		t.Errorf("notice body = %q", rs[0].Body)
	}
	if seen := mt.SeenUIDs(); len(seen) != 1 {
		t.Errorf("mail must be marked seen, got %v", seen)
	}
}

// TestEndDetectBothFailOnYoungThreadReplies: below the limit a dead
// classifier never costs a request its answer.
func TestEndDetectBothFailOnYoungThreadReplies(t *testing.T) {
	var jc, lc int
	mt := newFallbackApp(t, failingServer(t, &jc).URL, failingServer(t, &lc).URL)
	rs := runOne(t, mt, threadMail("<c4@mailtest>", "one more thing: rerun it", 1))
	if len(rs) != 1 || rs[0].Kind == replier.KindNotice {
		t.Fatalf("want one normal reply, got %#v", rs)
	}
}

func logf(p float64) float64 { return math.Log(p) }

// verdictRunner answers the end_detect agent probe with verdict and any
// other prompt (the real task) with a normal reply.
type verdictRunner struct {
	verdict string
	probes  int
	tasks   int
}

func (r *verdictRunner) Run(_ context.Context, prompt, _ string, _ bool) (string, string, error) {
	if strings.Contains(prompt, "<<<PERCH_END>>>") {
		r.probes++
		return "It's a sign-off.\n<<<PERCH_END>>> " + r.verdict, "", nil
	}
	r.tasks++
	return "Hi alice,\n\nDone.\n\nBest,\nagent", "", nil
}

// TestEndDetectAgentProbeIsLastJudge: Jev and the llm both fail, the agent
// probe says over, so nothing is sent and the task never runs.
func TestEndDetectAgentProbeIsLastJudge(t *testing.T) {
	var jc, lc int
	run := &verdictRunner{verdict: "over"}
	mt := newFallbackAppWithRunner(t, failingServer(t, &jc).URL, failingServer(t, &lc).URL, run)
	rs := runOne(t, mt, threadMail("<c5@mailtest>", "Likewise.", 5))
	if run.probes != 1 || run.tasks != 0 {
		t.Errorf("probes=%d tasks=%d, want 1 and 0", run.probes, run.tasks)
	}
	if len(rs) != 0 {
		t.Errorf("agent probe said over: want no mail, got %#v", rs)
	}
}

// TestEndDetectAgentProbeSaysReply: the probe's "reply" means a normal answer.
func TestEndDetectAgentProbeSaysReply(t *testing.T) {
	run := &verdictRunner{verdict: "reply"}
	mt := newFallbackAppWithRunner(t, "", "", run)
	rs := runOne(t, mt, threadMail("<c6@mailtest>", "rerun the plan please", 5))
	if run.probes != 1 || run.tasks != 1 {
		t.Errorf("probes=%d tasks=%d, want 1 and 1", run.probes, run.tasks)
	}
	if len(rs) != 1 || rs[0].Kind == replier.KindNotice {
		t.Fatalf("want one normal reply, got %#v", rs)
	}
}

func TestParseEndDetectAgentOutput(t *testing.T) {
	if l, err := app.ParseEndDetectAgentOutput("e.g. <<<PERCH_END>>> reply\n...\n<<<PERCH_END>>> OVER"); err != nil || l != "over" {
		t.Errorf("got %q, %v; want last verdict over", l, err)
	}
	if _, err := app.ParseEndDetectAgentOutput("I think it's over."); err == nil {
		t.Error("missing sentinel must be an error, never a guess")
	}
}
