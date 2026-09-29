package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testQuestions is the shape perch actually sends: one choice, one score.
func testQuestions() map[string]Question {
	return map[string]Question{
		"runtime": {Type: TypeChoice, Question: "short or long?",
			Criteria: map[string]string{"short": "quick", "long": "slow"}},
		"eta": {Type: TypeScore, Question: "how long?",
			Criteria: []string{"a minute", "five minutes"}},
	}
}

func TestAskDecodesAnswers(t *testing.T) {
	var gotAuth, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"answers": {
				"runtime": {"type":"choice","choice":"long",
					"probabilities":{"long":0.62,"short":0.38},"confidence":0.24},
				"eta": {"type":"score","score":3}
			},
			"model":"jev-1.13.0",
			"usage":{"input_tokens":420,"output_tokens":0}
		}`)
	}))
	defer srv.Close()

	c := New("secret-key", srv.URL, "", 0, nil)
	if c == nil {
		t.Fatal("New returned nil for a non-empty key")
	}
	if c.Model() != DefaultModel {
		t.Errorf("empty model should fall back to the pinned default, got %q", c.Model())
	}

	ans, err := c.Ask(context.Background(), "classify", map[string]any{"task": "x"}, testQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	var sent request
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("request body is not the documented envelope: %v", err)
	}
	// The pinned version must be on the wire. A "jev-latest" here would
	// move the model under the rubric in internal/app/classify.go.
	if sent.Model != DefaultModel {
		t.Errorf("model on the wire = %q, want %q", sent.Model, DefaultModel)
	}
	if len(sent.Questions) != 2 {
		t.Errorf("questions on the wire = %d, want 2", len(sent.Questions))
	}

	rt := ans["runtime"]
	if rt.Choice != "long" {
		t.Errorf("choice = %q, want long", rt.Choice)
	}
	// Prob is the calibrated probability of the chosen option. Confidence is
	// the vendor's rescaling (2*maxProb-1) and reads far lower for the same
	// answer — this is the pair that must never be confused.
	if rt.Prob() != 0.62 {
		t.Errorf("Prob() = %v, want 0.62", rt.Prob())
	}
	if rt.Confidence == nil || *rt.Confidence != 0.24 {
		t.Errorf("confidence should be carried verbatim for logging, got %v", rt.Confidence)
	}
	if s := ans["eta"].Score; s == nil || *s != 3 {
		t.Errorf("score = %v, want 3", s)
	}
	// The score answer carries no noul field; it must stay nil rather than
	// decode to a fabricated 0.0 that reads as "certainly no".
	if ans["eta"].Noul != nil {
		t.Errorf("absent noul decoded to %v, want nil", *ans["eta"].Noul)
	}
}

func TestAskRetriesRateLimit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"slow down"}`)
			return
		}
		io.WriteString(w, `{"answers":{"runtime":{"type":"choice","choice":"short"}}}`)
	}))
	defer srv.Close()

	start := time.Now()
	ans, err := New("k", srv.URL, "", 0, nil).Ask(context.Background(), "classify", nil, testQuestions())
	if err != nil {
		t.Fatalf("a 429 followed by a 200 should succeed, got %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if ans["runtime"].Choice != "short" {
		t.Errorf("choice = %q", ans["runtime"].Choice)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("Retry-After: 1 was ignored — retried after %v", d)
	}
}

// debugLogger returns a logger at DEBUG (the chattiest setting, so every
// log path this package has is exercised) writing into buf.
func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestAuthIsTerminalAndNeverLeaksKey covers both halves of the 401 contract:
// a bad key is not retried (retrying just burns the rate limit on a request
// that cannot succeed), and the key never reaches the error string or any
// log line. perch logs these errors, and logs get pasted into issues.
func TestAuthIsTerminalAndNeverLeaksKey(t *testing.T) {
	const key = "sk-super-secret-value"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	_, err := New(key, srv.URL, DefaultModel, 0, debugLogger(&logs)).
		Ask(context.Background(), "classify", nil, testQuestions())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if calls != 1 {
		t.Errorf("a 401 must not be retried, got %d calls", calls)
	}
	if strings.Contains(err.Error(), key) {
		t.Error("the API key leaked into the error string")
	}
	if logs.Len() == 0 {
		t.Fatal("a failed call logged nothing")
	}
	if strings.Contains(logs.String(), key) {
		t.Errorf("the API key leaked into the logs:\n%s", logs.String())
	}
	// The WARN has to name the status, or the line is useless for
	// telling "wrong key" apart from "server down".
	if !strings.Contains(logs.String(), "jev call failed") ||
		!strings.Contains(logs.String(), "status=401") {
		t.Errorf("failure log is missing the status:\n%s", logs.String())
	}
}

// TestSuccessLogsDecisionAndNeverLeaksKey is the success-path half of the
// redaction contract: the happy path logs the most (request state, raw
// response, per-answer decisions), so it is where a key would most easily
// end up. It also pins the shape of the two INFO lines an operator reads.
func TestSuccessLogsDecisionAndNeverLeaksKey(t *testing.T) {
	const key = "sk-another-secret-value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{
			"answers": {
				"runtime": {"type":"choice","choice":"long",
					"probabilities":{"long":0.92,"short":0.08},"confidence":0.84},
				"eta": {"type":"score","score":3.99,"legend":{"4":"an hour or more"}}
			},
			"model":"jev-1.13.0",
			"usage":{"input_tokens":420,"output_tokens":7}
		}`)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	_, err := New(key, srv.URL, "", 0, debugLogger(&logs)).
		Ask(context.Background(), "classify", map[string]any{"task": "migrate everything"}, testQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	out := logs.String()
	if strings.Contains(out, key) {
		t.Errorf("the API key leaked into the logs:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "authorization") ||
		strings.Contains(out, "Bearer") {
		t.Errorf("the Authorization header reached the logs:\n%s", out)
	}
	for _, want := range []string{
		`msg="jev call"`, "purpose=classify", "status=200", "latency_ms=",
		"attempts=1", "retries=0", "input_tokens=420", "output_tokens=7",
		`msg="jev answer"`, "choice=long", "prob=0.92",
		// Confidence must arrive labelled. It is 2*maxProb-1 (0.84 for this
		// 0.92 answer) and the label is the only thing standing between a
		// future reader and a threshold on it.
		"vendor_confidence_not_a_probability=0.84",
		"probabilities=", "score=3.99", "an hour or more",
		// The state is DEBUG-only and truncated; the question wording is
		// perch's own, so only the keys are logged.
		`msg="jev request"`, "migrate everything",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q:\n%s", want, out)
		}
	}
}

// TestDebugStateIsTruncated: the logged state is an inbound email body, so
// a long one must not be mirrored into the log file in full.
func TestDebugStateIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"answers":{"runtime":{"type":"choice","choice":"short"}}}`)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	body := strings.Repeat("x", 4000)
	_, err := New("k", srv.URL, "", 0, debugLogger(&logs)).
		Ask(context.Background(), "end_detect", map[string]any{"body": body}, testQuestions())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Contains(logs.String(), body) {
		t.Error("the full state was logged; it must be truncated")
	}
	if !strings.Contains(logs.String(), "truncated") {
		t.Errorf("truncation is not marked in the log:\n%s", logs.String())
	}
}

func TestNewWithoutKeyIsNil(t *testing.T) {
	// The nil client IS the feature flag; callers branch on it once.
	if c := New("  ", "", "", 0, nil); c != nil {
		t.Fatal("New with a blank key should return nil")
	}
	var c *Client
	if _, err := c.Ask(context.Background(), "classify", nil, testQuestions()); !errors.Is(err, ErrNoClient) {
		t.Errorf("nil client Ask = %v, want ErrNoClient", err)
	}
	if c.Model() != "" {
		t.Error("nil client Model should be empty, not panic")
	}
}
