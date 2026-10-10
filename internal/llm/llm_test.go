package llm

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var opts = []string{"over", "reply"}

func serve(t *testing.T, status int, body string, got *map[string]any, hdr *http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hdr != nil {
			*hdr = r.Header.Clone()
		}
		if got != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, got)
			(*got)["_path"] = r.URL.Path
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewNilWhenUnconfigured(t *testing.T) {
	for _, c := range [][3]string{{"", "http://x", "m"}, {"k", "", "m"}, {"k", "http://x", ""}} {
		if cl, err := New("openai", c[1], c[0], c[2], 0, 0, nil); cl != nil || err != nil {
			t.Errorf("New(%v) = %v, %v; want nil, nil", c, cl, err)
		}
	}
	if _, err := New("gemini", "http://x", "k", "m", 0, 0, nil); err == nil {
		t.Error("unknown format must be an error")
	}
}

func TestOpenAILogprobs(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	body := `{"choices":[{"message":{"content":"over"},"logprobs":{"content":[{"token":"over",
		"top_logprobs":[{"token":"over","logprob":` + f(math.Log(0.9)) + `},
		{"token":" Over","logprob":` + f(math.Log(0.05)) + `},
		{"token":"rep","logprob":` + f(math.Log(0.05)) + `}]}]}}]}`
	srv := serve(t, 200, body, &got, &hdr)
	c, _ := New("openai", srv.URL+"/", "secret", "deepseek-flash", 0, 0, nil)
	ch, err := c.Choose(context.Background(), "t", "sys", "hi", opts)
	if err != nil {
		t.Fatal(err)
	}
	if got["_path"] != "/chat/completions" || got["logprobs"] != true || got["model"] != "deepseek-flash" {
		t.Errorf("request = %v", got)
	}
	if hdr.Get("Authorization") != "Bearer secret" {
		t.Errorf("auth header = %q", hdr.Get("Authorization"))
	}
	if ch.Label != "over" || !ch.Calibrated || math.Abs(ch.Prob("over")-0.95) > 1e-9 {
		t.Errorf("choice = %+v, want over/0.95 calibrated", ch)
	}
}

func TestOpenAIWithoutLogprobsIsUncalibrated(t *testing.T) {
	srv := serve(t, 200, `{"choices":[{"message":{"content":" Reply."}}]}`, nil, nil)
	c, _ := New("", srv.URL, "k", "m", 0, 0, nil)
	ch, err := c.Choose(context.Background(), "t", "s", "p", opts)
	if err != nil || ch.Label != "reply" || ch.Calibrated || ch.Prob("over") != 0 {
		t.Errorf("got %+v, %v", ch, err)
	}
}

func TestOpenAIOffFormatMassIsError(t *testing.T) {
	body := `{"choices":[{"message":{"content":"over"},"logprobs":{"content":[{"token":"over",
		"top_logprobs":[{"token":"over","logprob":` + f(math.Log(0.2)) + `},{"token":"I","logprob":` + f(math.Log(0.8)) + `}]}]}}]}`
	srv := serve(t, 200, body, nil, nil)
	c, _ := New("openai", srv.URL, "k", "m", 0, 0, nil)
	if _, err := c.Choose(context.Background(), "t", "s", "p", opts); err == nil {
		t.Error("mass mostly off-label must be an error")
	}
}

func TestAnthropicSkipsThinkingBlock(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := serve(t, 200, `{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"over"}]}`, &got, &hdr)
	c, _ := New("anthropic", srv.URL+"/anthropic", "secret", "mimo", 0, 0, nil)
	ch, err := c.Choose(context.Background(), "t", "sys", "hi", opts)
	if err != nil || ch.Label != "over" || ch.Calibrated || ch.Prob("over") != 1 {
		t.Fatalf("got %+v, %v", ch, err)
	}
	if got["_path"] != "/anthropic/v1/messages" || hdr.Get("x-api-key") != "secret" || hdr.Get("anthropic-version") == "" {
		t.Errorf("path=%v headers=%v", got["_path"], hdr)
	}
}

func TestUnrecognisedAnswerIsError(t *testing.T) {
	srv := serve(t, 200, `{"choices":[{"message":{"content":"maybe"}}]}`, nil, nil)
	c, _ := New("openai", srv.URL, "k", "m", 0, 0, nil)
	if _, err := c.Choose(context.Background(), "t", "s", "p", opts); err == nil {
		t.Error("an answer outside the options must be an error")
	}
}

func TestErrorNeverCarriesKey(t *testing.T) {
	srv := serve(t, 401, `{"error":"bad key"}`, nil, nil)
	c, _ := New("openai", srv.URL, "sk-very-secret", "m", 0, 0, nil)
	_, err := c.Choose(context.Background(), "t", "s", "p", opts)
	if err == nil || strings.Contains(err.Error(), "sk-very-secret") {
		t.Errorf("err = %v", err)
	}
}

func TestNilClient(t *testing.T) {
	var c *Client
	if _, err := c.Choose(context.Background(), "t", "s", "p", opts); err != ErrNoClient {
		t.Errorf("err = %v", err)
	}
}

func f(x float64) string { b, _ := json.Marshal(x); return string(b) }
