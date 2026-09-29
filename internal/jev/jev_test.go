package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

	c := New("secret-key", srv.URL, "", 0)
	if c == nil {
		t.Fatal("New returned nil for a non-empty key")
	}
	if c.Model() != DefaultModel {
		t.Errorf("empty model should fall back to the pinned default, got %q", c.Model())
	}

	ans, err := c.Ask(context.Background(), map[string]any{"task": "x"}, testQuestions())
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
	ans, err := New("k", srv.URL, "", 0).Ask(context.Background(), nil, testQuestions())
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

// TestAuthIsTerminalAndNeverLeaksKey covers both halves of the 401 contract:
// a bad key is not retried (retrying just burns the rate limit on a request
// that cannot succeed), and the key never reaches the error string. perch
// logs these errors, and logs get pasted into issues.
func TestAuthIsTerminalAndNeverLeaksKey(t *testing.T) {
	const key = "sk-super-secret-value"
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	defer srv.Close()

	_, err := New(key, srv.URL, DefaultModel, 0).Ask(context.Background(), nil, testQuestions())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if calls != 1 {
		t.Errorf("a 401 must not be retried, got %d calls", calls)
	}
	if strings.Contains(err.Error(), key) {
		t.Error("the API key leaked into the error string")
	}
}

func TestNewWithoutKeyIsNil(t *testing.T) {
	// The nil client IS the feature flag; callers branch on it once.
	if c := New("  ", "", "", 0); c != nil {
		t.Fatal("New with a blank key should return nil")
	}
	var c *Client
	if _, err := c.Ask(context.Background(), nil, testQuestions()); !errors.Is(err, ErrNoClient) {
		t.Errorf("nil client Ask = %v, want ErrNoClient", err)
	}
	if c.Model() != "" {
		t.Error("nil client Model should be empty, not panic")
	}
}
