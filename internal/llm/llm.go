// Package llm is a minimal chat-completion client used as the alternative
// to Jev (internal/jev) for perch's two typed questions: "is this thread
// over?" (end_detect) and "will this task run long?" (the duration probe).
//
// Jev is unreachable from some regions (HTTP 451 "Typesafe is not available
// in your region", seen from mainland China on 2026-10-10), and end_detect
// fails open, so a dead Jev quietly removed the only brake on a peer-agent
// loop. This client is the second opinion. It speaks two wire formats,
// because the providers that are reachable from China offer one or both:
//
//	"openai"    — POST {base_url}/chat/completions. DeepSeek, Volcengine Ark,
//	              DashScope compatible-mode, MiMo, ...
//	"anthropic" — POST {base_url}/v1/messages. DeepSeek's and MiMo's
//	              Anthropic-compatible endpoints, or Anthropic itself.
//
// Calibration. Jev returns a calibrated distribution; a chat model returns
// text. On the openai format perch asks for logprobs on the first answer
// token and derives P(label) from them, which is close enough to threshold
// on. The anthropic format has no logprobs, so the answer carries only the
// label the model wrote and Calibrated=false — callers decide how much to
// trust that (see Choice.Calibrated).
//
// The API key is never logged and never appears in an error, same rule as
// internal/jev.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
)

// Wire formats. See the package doc.
const (
	FormatOpenAI    = "openai"
	FormatAnthropic = "anthropic"
)

const (
	// DefaultTimeout is generous because reasoning models (e.g. deepseek)
	// think before answering; with DefaultMaxTokens of headroom a call can
	// take well past Jev's budget. It still runs inline while ProcessUnseen
	// holds the app lock, so a hung provider stalls the mailbox this long.
	DefaultTimeout = 2 * time.Minute
	// DefaultMaxTokens bounds the answer. The label is one word, but
	// reasoning models (e.g. deepseek) count their hidden thinking against
	// max_tokens; a small cap is spent on reasoning and content comes back
	// empty, so leave ample room. Providers reject a max_tokens above their
	// own output cap with HTTP 400 — lower llm.max_tokens for those.
	DefaultMaxTokens = 128 * 1024
	// topLogprobs is the most every logprobs-capable provider checked
	// accepts (DashScope caps at 5).
	topLogprobs = 5
	// maxBody caps how much of a response is read. Reasoning models return
	// their thinking (and per-token logprobs) in the body, which easily
	// passes 64 KiB once max_tokens is large; a cut body fails to decode.
	maxBody = 16 << 20
	// minLabelMass is how much of the first token's top-k probability mass
	// must land on a known label for the logprobs to count. Below it the
	// model did not follow the one-word format and the distribution means
	// nothing — that is "no answer", not "the other label".
	minLabelMass = 0.5
)

// ErrNoClient is returned by methods on a nil *Client.
var ErrNoClient = errors.New("llm: not configured (PERCH_LLM_API_KEY unset)")

// Choice is one classification answer.
type Choice struct {
	// Label is the option the model picked, always one of the options
	// passed to Choose.
	Label string
	// Probabilities is P(option) for every option. Derived from logprobs
	// when Calibrated; otherwise 1 for Label and 0 for the rest.
	Probabilities map[string]float64
	// Calibrated is true when Probabilities came from token logprobs rather
	// than from the label alone.
	Calibrated bool
}

// Prob returns the probability of option.
func (c Choice) Prob(option string) float64 { return c.Probabilities[option] }

// Client is a configured caller. Build one with New.
type Client struct {
	format    string
	url       string
	key       string
	model     string
	maxTokens int
	hc        *http.Client
	log       *slog.Logger
}

// New returns a client, or nil when apiKey, baseURL or model is empty — the
// nil is the feature flag, same shape as jev.New. format defaults to
// "openai"; any other unknown value is an error so a typo in perch.yaml
// does not silently pick a wire format. A timeout or maxTokens of 0 or less
// means DefaultTimeout / DefaultMaxTokens.
func New(format, baseURL, apiKey, model string, timeout time.Duration, maxTokens int, log *slog.Logger) (*Client, error) {
	apiKey, baseURL, model = strings.TrimSpace(apiKey), strings.TrimSpace(baseURL), strings.TrimSpace(model)
	if apiKey == "" || baseURL == "" || model == "" {
		return nil, nil
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = FormatOpenAI
	}
	base := strings.TrimRight(baseURL, "/")
	var url string
	switch format {
	case FormatOpenAI:
		url = base + "/chat/completions"
	case FormatAnthropic:
		url = base + "/v1/messages"
	default:
		return nil, fmt.Errorf("llm: unknown format %q (want %s or %s)", format, FormatOpenAI, FormatAnthropic)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	return &Client{format: format, url: url, key: apiKey, model: model,
		maxTokens: maxTokens, hc: &http.Client{Timeout: timeout}, log: log}, nil
}

// Model returns the configured model, or "" for a nil client.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Format returns the wire format, or "" for a nil client.
func (c *Client) Format() string {
	if c == nil {
		return ""
	}
	return c.format
}

// Choose asks the model to answer prompt with exactly one of options.
// purpose names the caller on every log line ("end_detect", "classify").
//
// An answer that is not one of options is a hard error, never folded into
// a default — the lesson internal/jev records about unrecognised choices.
func (c *Client) Choose(ctx context.Context, purpose, system, prompt string, options []string) (Choice, error) {
	if c == nil {
		return Choice{}, ErrNoClient
	}
	if len(options) < 2 {
		return Choice{}, errors.New("llm: need at least two options")
	}
	system += "\n\nAnswer with exactly one word, one of: " + strings.Join(options, ", ") +
		". No punctuation, no explanation."

	var payload any
	if c.format == FormatOpenAI {
		payload = map[string]any{
			"model":        c.model,
			"temperature":  0,
			"max_tokens":   c.maxTokens,
			"logprobs":     true,
			"top_logprobs": topLogprobs,
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": prompt},
			},
		}
	} else {
		payload = map[string]any{
			"model":       c.model,
			"temperature": 0,
			"max_tokens":  c.maxTokens,
			"system":      system,
			"messages":    []map[string]string{{"role": "user", "content": prompt}},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Choice{}, fmt.Errorf("llm: encode request: %w", err)
	}

	start := time.Now()
	raw, status, err := c.post(ctx, body)
	took := time.Since(start)
	if err != nil {
		c.warn("llm call failed", "purpose", purpose, "format", c.format, "model", c.model,
			"status", status, "latency_ms", took.Milliseconds(), "err", err)
		return Choice{}, err
	}

	var ch Choice
	if c.format == FormatOpenAI {
		ch, err = parseOpenAI(raw, options)
	} else {
		ch, err = parseAnthropic(raw, options)
	}
	if err != nil {
		c.warn("llm answer unusable", "purpose", purpose, "format", c.format, "model", c.model,
			"latency_ms", took.Milliseconds(), "body_bytes", len(raw), "body", snippet(raw), "err", err)
		return Choice{}, err
	}
	if c.log != nil {
		c.log.Info("llm call", "purpose", purpose, "format", c.format, "model", c.model,
			"status", status, "latency_ms", took.Milliseconds(), "choice", ch.Label,
			"prob", ch.Prob(ch.Label), "calibrated", ch.Calibrated)
	}
	return ch, nil
}

func (c *Client) post(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("llm: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The only place the key is used.
	if c.format == FormatOpenAI {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else {
		req.Header.Set("x-api-key", c.key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("llm: request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("llm: read response (%d bytes so far): %w", len(raw), err)
	}
	if len(raw) > maxBody {
		return nil, resp.StatusCode, fmt.Errorf("llm: response larger than %d bytes", maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("llm: unexpected status %d: %s", resp.StatusCode, snippet(raw))
	}
	return raw, resp.StatusCode, nil
}

func (c *Client) warn(msg string, args ...any) {
	if c.log != nil {
		c.log.Warn(msg, args...)
	}
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Logprobs *struct {
			Content []struct {
				Token       string `json:"token"`
				TopLogprobs []struct {
					Token   string  `json:"token"`
					Logprob float64 `json:"logprob"`
				} `json:"top_logprobs"`
			} `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
}

func parseOpenAI(raw []byte, options []string) (Choice, error) {
	var r openAIResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return Choice{}, fmt.Errorf("llm: decode response: %w", err)
	}
	if len(r.Choices) == 0 {
		return Choice{}, errors.New("llm: response carried no choices")
	}
	label, err := matchLabel(r.Choices[0].Message.Content, options)
	if err != nil {
		return Choice{}, err
	}
	// Logprobs of the first token that carries text. Providers that ignore
	// the logprobs flag simply omit the block; that is an uncalibrated
	// answer, not an error.
	if lp := r.Choices[0].Logprobs; lp != nil {
		for _, tok := range lp.Content {
			if strings.TrimSpace(tok.Token) == "" {
				continue
			}
			mass := make(map[string]float64, len(options))
			total := 0.0
			for _, t := range tok.TopLogprobs {
				if o := tokenOption(t.Token, options); o != "" {
					p := math.Exp(t.Logprob)
					mass[o] += p
					total += p
				}
			}
			if total < minLabelMass {
				return Choice{}, fmt.Errorf("llm: only %.2f of first-token mass on a known label", total)
			}
			probs := make(map[string]float64, len(options))
			for _, o := range options {
				probs[o] = mass[o] / total
			}
			return Choice{Label: label, Probabilities: probs, Calibrated: true}, nil
		}
	}
	return uncalibrated(label, options), nil
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func parseAnthropic(raw []byte, options []string) (Choice, error) {
	var r anthropicResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return Choice{}, fmt.Errorf("llm: decode response: %w", err)
	}
	// Thinking-capable models put "thinking" blocks first; the answer is
	// the first text block.
	for _, b := range r.Content {
		if b.Type == "text" {
			label, err := matchLabel(b.Text, options)
			if err != nil {
				return Choice{}, err
			}
			return uncalibrated(label, options), nil
		}
	}
	return Choice{}, errors.New("llm: response carried no text block")
}

func uncalibrated(label string, options []string) Choice {
	probs := make(map[string]float64, len(options))
	for _, o := range options {
		probs[o] = 0
	}
	probs[label] = 1
	return Choice{Label: label, Probabilities: probs}
}

// matchLabel maps the model's text onto exactly one option.
func matchLabel(text string, options []string) (string, error) {
	w := strings.ToLower(strings.Trim(strings.TrimSpace(text), " \t\r\n.,!\"'`*"))
	for _, o := range options {
		if w == strings.ToLower(o) {
			return o, nil
		}
	}
	return "", fmt.Errorf("llm: unrecognised answer %q", snippet([]byte(text)))
}

// tokenOption maps one first-position token onto the option it begins, or
// "" when it begins none or several. A label may be split across tokens
// ("rep"+"ly"), so a prefix counts.
func tokenOption(tok string, options []string) string {
	t := strings.ToLower(strings.TrimSpace(tok))
	if t == "" {
		return ""
	}
	found := ""
	for _, o := range options {
		if strings.HasPrefix(strings.ToLower(o), t) {
			if found != "" {
				return ""
			}
			found = o
		}
	}
	return found
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
