package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/jev"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
)

// TestParseJevAnswersRejectsUnknownChoice pins the rule that cost a sibling
// integration 5125 silently discarded verdicts: an unrecognised choice is an
// error, never quietly folded into a default. Here "short" is the tempting
// default — and taking it would mean an obviously long task gets no ack and
// nobody ever learns the model answered something else.
func TestParseJevAnswersRejectsUnknownChoice(t *testing.T) {
	_, _, _, err := app.ParseJevAnswers(map[string]jev.Answer{
		"runtime": {Type: jev.TypeChoice, Choice: "medium"},
	})
	if err == nil {
		t.Fatal("an unrecognised runtime choice must be an error, not a default")
	}
	if !strings.Contains(err.Error(), "medium") {
		t.Errorf("the error should name the value it rejected, got %v", err)
	}

	// A missing ETA score is NOT an error: it only decorates the ack text,
	// and throwing away a correct runtime verdict over a cosmetic field
	// would be the wrong trade. It degrades to 0, which BuildLongAckBody
	// already renders as a sentence without a duration.
	rt, eta, _, err := app.ParseJevAnswers(map[string]jev.Answer{
		"runtime": {Type: jev.TypeChoice, Choice: "long"},
	})
	if err != nil || rt != "long" || eta != 0 {
		t.Errorf("got (%q, %d, %v), want (long, 0, nil)", rt, eta, err)
	}
}

// TestParseJevAnswersScoreIsZeroBased pins the rubric indexing against what
// the live API actually returns. The first draft of this decoder assumed a
// 1-based rank; the real response for an unmistakably hour-plus task was
// score 3.99 with probabilities {"4": 1.0}, so reading it as 1-based quoted
// every sender exactly one band too fast.
func TestParseJevAnswersScoreIsZeroBased(t *testing.T) {
	score := func(f float64) *float64 { return &f }
	for _, tc := range []struct {
		s    float64
		want int
	}{
		{0.0, 1},   // "under a minute"
		{1.2, 5},   // "a few minutes"
		{3.99, 60}, // rounds to the top band, as observed live
		{9.0, 0},   // out of range: degrade, don't index past the rubric
	} {
		_, eta, _, err := app.ParseJevAnswers(map[string]jev.Answer{
			"runtime": {Type: jev.TypeChoice, Choice: "long"},
			"eta":     {Type: jev.TypeScore, Score: score(tc.s)},
		})
		if err != nil || eta != tc.want {
			t.Errorf("score %v -> (%d, %v), want %d", tc.s, eta, err, tc.want)
		}
	}
}

// TestJevClassifyFailureFallsBackToAgent walks the whole degradation path
// end to end: classifier is "jev", the key is set, but the answer is
// unusable. perch must fall back to the agent probe and still send the
// interim ack — a classifier that is merely unhelpful can never be allowed
// to change what the sender receives.
func TestJevClassifyFailureFallsBackToAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Well-formed envelope, nonsense choice — the failure mode a dead
		// server would not exercise.
		io.WriteString(w, `{"answers":{"runtime":{"type":"choice","choice":"maybe"}}}`)
	}))
	defer srv.Close()

	cfg := config.Defaults()
	cfg.MaxPromptBytes = 4096
	cfg.AgentWorkdir = t.TempDir()
	cfg.LongTaskAck = true
	cfg.Classifier = config.ClassifierJev
	cfg.JevAPIKey = "test-key"
	cfg.JevAPIURL = srv.URL

	run := &mailtest.ScriptedRunner{
		Outs: []string{"analysis\n<<<PERCH_CLASSIFY>>> long 7", "done"},
	}
	mt, err := mailtest.New(cfg, []string{"alice@163.com"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt.Send("alice@163.com", "agent@163.com", "hi", "run the whole suite"); err != nil {
		t.Fatal(err)
	}
	if err := mt.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Two agent runs: the fallback classify probe, then the real task. If
	// the Jev answer had been accepted there would be only one.
	if len(run.Prompts) != 2 {
		t.Fatalf("expected the agent probe to run as fallback (2 calls), got %d", len(run.Prompts))
	}
	if !strings.Contains(run.Prompts[0], "Evaluate the operation") {
		t.Errorf("call 1 should be the classify prompt")
	}
	rs := mt.Replies()
	if len(rs) != 2 {
		t.Fatalf("expected 2 replies (ack then real), got %d", len(rs))
	}
	if !strings.Contains(rs[0].Body, "around 7 minutes") {
		t.Errorf("the ack should carry the agent probe's ETA, got %q", rs[0].Body)
	}
}
