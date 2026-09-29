// Package jev is a minimal client for TypeSafe AI's "System One" model.
//
// Jev answers typed questions instead of writing prose: you hand it an
// arbitrary JSON `state` plus a map of named questions (choice / score /
// noul) and it returns one calibrated answer per question, sampled in
// parallel rather than token by token. perch uses it for the long-task
// duration probe — exactly one Choice ("short|long") and one Score (how
// long) — which until now cost a whole extra agent invocation per email.
//
// There is no official Go SDK (Python and JS only), which is why this file
// exists. It speaks the vendor's own endpoint and nothing else: several
// third-party sites mirror the API surface under different hostnames and
// are not the same service.
//
// Three traps are baked into the types below. All three were paid for in a
// sibling integration; none of them are obvious from the wire format:
//
//   - The vendor's `confidence` field is NOT a probability and must never
//     be used as a gate. It is 2*max(probabilities) - 1, so a decisive
//     62/38 answer reports 0.24 and any sane-looking threshold throws away
//     most correct answers. Confirmed twice: once in a sibling integration
//     on 2026-09-22, and again from perch on 2026-09-29, where a 0.92/0.08
//     choice reported confidence 0.84. Read
//     Answer.Probabilities[chosen] instead; Confidence is carried only so
//     it can be logged.
//   - Every numeric field is a *float64. A missing field has to decode to
//     nil, not to a fabricated 0.0 — for a noul, 0.0 reads as "certainly
//     no", which is the opposite of "the model didn't answer".
//   - The model version is pinned (DefaultModel). The `jev-latest` alias
//     moves under your prompts and thresholds with no warning, so perch
//     never sends it.
//
// Logging: one INFO "jev call" per Ask (status, latency, attempts, tokens)
// and one INFO "jev answer" per decoded answer; request and raw response
// bodies at DEBUG; failures at WARN. The API key is never an attribute and
// never appears in an error — the Authorization header is set on the
// request and read nowhere else (see TestAuthIsTerminalAndNeverLeaksKey,
// which greps every emitted log line as well as the error).
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultAPIURL is TypeSafe AI's first-party System One endpoint.
	DefaultAPIURL = "https://api.typesafe.ai/v1/systemone"
	// DefaultModel is pinned on purpose — see the package doc. Never
	// substitute the "jev-latest" alias.
	DefaultModel = "jev-1.13.0"
	// DefaultTimeout is deliberately short. perch's probe runs inline in
	// ProcessUnseen, which holds the app lock for its whole body, so a slow
	// Jev call stalls every email queued behind it. Jev's own quoted latency
	// is 70-500ms; anything near 10s means the far end is unwell and the
	// agent fallback is the faster path anyway.
	DefaultTimeout = 10 * time.Second

	// maxRetries bounds the 429/529 retry loop (so up to 3 attempts total).
	maxRetries = 2
	// retryBase is the first backoff step; doubled per attempt. A Retry-After
	// header, when present, wins over both.
	retryBase = time.Second
	// maxErrBody caps how much of a failing response is quoted into the
	// error. Enough to identify the problem, short enough not to dump a
	// vendor HTML error page into the log. The same cap bounds the raw
	// body logged at DEBUG — the response is never read past it.
	maxErrBody = 4096
	// maxLogLegend caps the rubric label shown on a score answer's log
	// line: enough to name the band, not the whole sentence.
	maxLogLegend = 40
	// maxLogState caps the request state echoed at DEBUG. The state is an
	// inbound email body: useful to see which mail produced a verdict,
	// ruinous to mirror in full into the log file.
	maxLogState = 500
)

// Question types. Choice picks one named option, Score grades against an
// ordered rubric, Noul answers yes/no as a probability.
const (
	TypeChoice = "choice"
	TypeScore  = "score"
	TypeNoul   = "noul"
)

// Sentinel errors. Callers distinguish "the key is wrong" (stop, tell the
// operator) from "the far end is busy" (fall back and try again later).
var (
	// ErrAuth is a 401: the key is missing, wrong, or not entitled. Never
	// retried — retrying a bad key just burns the rate limit.
	ErrAuth = errors.New("jev: api key rejected")
	// ErrValidation is a 422: perch built a request the model won't accept.
	// Never retried; retrying a malformed request is pointless.
	ErrValidation = errors.New("jev: request rejected")
	// ErrOverloaded means 429/529 survived every retry.
	ErrOverloaded = errors.New("jev: overloaded")
	// ErrNoClient is returned by methods on a nil *Client, so callers that
	// forgot the nil check get a clear error instead of a panic.
	ErrNoClient = errors.New("jev: not configured (TYPESAFE_API_KEY unset)")
)

// Question is one thing to ask. Criteria is the option->description map for
// a choice, an ordered []string rubric for a score, and omitted for a noul.
type Question struct {
	Type     string `json:"type"`
	Question string `json:"question"`
	Criteria any    `json:"criteria,omitempty"`
}

// Answer is one reply. Which field is populated depends on Type.
//
// Choice is a plain string because the empty string is already an
// unambiguous "absent" — but note that callers must treat an unrecognised
// value as a hard error, never quietly map it onto a default. In the
// sibling integration a "unknown -> FALSE" fallback silently swallowed 5125
// of 7412 verdicts before anyone noticed.
type Answer struct {
	Type   string `json:"type"`
	Choice string `json:"choice,omitempty"`
	// Score is a ZERO-based, fractional position in the question's rubric —
	// the probability-weighted expectation over rubric indices, not the
	// argmax and not a 1-based rank. Verified live 2026-09-29: a clear
	// top-band answer returned 3.99 with probabilities {"4": 1.0}. Round it.
	Score *float64 `json:"score,omitempty"`
	Noul  *float64 `json:"noul,omitempty"`
	// Legend echoes a score question's rubric back, keyed by index as a
	// string ("0", "1", ...). Carried so a logged score answer is readable
	// without cross-referencing the request.
	Legend map[string]string `json:"legend,omitempty"`
	// Probabilities is the calibrated distribution. THIS is what to
	// threshold on, not Confidence.
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Confidence is the vendor's own rescaling of Probabilities and is not
	// a probability. Log it, never gate on it. See the package doc.
	Confidence *float64 `json:"confidence,omitempty"`
}

// Prob returns the calibrated probability of the chosen option, or 0 when
// the model did not report one.
func (a Answer) Prob() float64 { return a.Probabilities[a.Choice] }

type request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

type response struct {
	Answers map[string]Answer `json:"answers"`
	Model   string            `json:"model"`
	Usage   *Usage            `json:"usage"`
}

// Usage is the token accounting the API returns. Output tokens are free at
// the time of writing; input is billed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Client is a configured Jev caller. The zero value is not usable; build one
// with New.
type Client struct {
	url   string
	key   string
	model string
	hc    *http.Client
	// log may be nil; every logging call here tolerates that, so tests and
	// throwaway callers need not build a logger.
	log *slog.Logger
}

// New returns a client, or nil when apiKey is empty.
//
// The nil return is the feature flag: "Jev is not configured" needs no
// separate boolean, and every method here is nil-safe, so callers branch
// once at the call site and nowhere else. Same shape as hook.New, which
// also takes the logger here rather than per call.
//
// Empty apiURL / model / timeout fall back to the Default* constants.
func New(apiKey, apiURL, model string, timeout time.Duration, log *slog.Logger) *Client {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	if apiURL = strings.TrimSpace(apiURL); apiURL == "" {
		apiURL = DefaultAPIURL
	}
	if model = strings.TrimSpace(model); model == "" {
		model = DefaultModel
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		url:   apiURL,
		key:   apiKey,
		model: model,
		hc:    &http.Client{Timeout: timeout},
		log:   log,
	}
}

// Model reports the pinned model version, for provenance in logs. Nil-safe.
func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Ask sends state plus questions and returns one Answer per question key.
// purpose names the caller ("classify", "end_detect") and appears on every
// log line so a log reader can tell two concurrent probes apart.
//
// 429 and 529 are retried with exponential backoff, honouring a Retry-After
// header when the server sends one. Every other non-2xx fails immediately:
// a 401 or a 422 will fail identically on the next attempt and the caller
// has a fallback path that is cheaper than waiting.
func (c *Client) Ask(ctx context.Context, purpose string, state any, questions map[string]Question) (map[string]Answer, error) {
	if c == nil {
		return nil, ErrNoClient
	}
	if len(questions) == 0 {
		return nil, errors.New("jev: no questions to ask")
	}
	payload, err := json.Marshal(request{State: state, Model: c.model, Questions: questions})
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}
	c.logRequest(purpose, state, questions)

	// Latency is measured across the whole Ask, backoff included: that is
	// the number that matters to the caller, which is holding the app lock
	// for the duration.
	start := time.Now()
	var lastErr error
	wait := time.Duration(0)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if wait <= 0 {
				wait = retryBase << (attempt - 1)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		res := c.post(ctx, payload)
		if res.err == nil {
			c.logCall(purpose, res, attempt+1, time.Since(start))
			c.logAnswers(purpose, res.answers)
			return res.answers, nil
		}
		lastErr = res.err
		c.logFailure(purpose, res, attempt+1, time.Since(start))
		if res.retryIn == nil {
			return nil, res.err // terminal: 401, 422, bad JSON, transport
		}
		wait = *res.retryIn
	}
	return nil, fmt.Errorf("%w after %d attempts: %v", ErrOverloaded, maxRetries+1, lastErr)
}

// result is one attempt's outcome. retryIn non-nil means "retryable"; its
// value is the server-requested delay, or 0 for "use the caller's backoff".
// status is 0 when the request never reached a response (transport error).
type result struct {
	answers map[string]Answer
	usage   *Usage
	status  int
	body    []byte
	retryIn *time.Duration
	err     error
}

// post makes one attempt.
func (c *Client) post(ctx context.Context, payload []byte) result {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return result{err: fmt.Errorf("jev: build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	// The only place the key is ever used. It is deliberately not held in
	// any variable that a log or error path can reach.
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.hc.Do(req)
	if err != nil {
		// Transport-level failures (DNS, TLS, timeout) are not retried here:
		// the caller's fallback is an agent run that will succeed, and a
		// second dial costs the mail queue another timeout.
		//
		// url.Error quotes the request URL, never the headers, so this
		// wrapping cannot carry the key.
		return result{err: fmt.Errorf("jev: request failed: %w", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	r := result{status: resp.StatusCode, body: body}

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		r.err = fmt.Errorf("%w (401): %s", ErrAuth, snippet(body))
		return r
	case resp.StatusCode == http.StatusUnprocessableEntity:
		r.err = fmt.Errorf("%w (422): %s", ErrValidation, snippet(body))
		return r
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529:
		d := retryAfter(resp.Header.Get("Retry-After"))
		r.retryIn, r.err = &d, fmt.Errorf("jev: %d: %s", resp.StatusCode, snippet(body))
		return r
	default:
		r.err = fmt.Errorf("jev: unexpected status %d: %s", resp.StatusCode, snippet(body))
		return r
	}

	var out response
	if err := json.Unmarshal(body, &out); err != nil {
		r.err = fmt.Errorf("jev: decode response: %w", err)
		return r
	}
	if len(out.Answers) == 0 {
		r.err = errors.New("jev: response carried no answers")
		return r
	}
	r.answers, r.usage = out.Answers, out.Usage
	return r
}

// ---------------------------------------------------------------------------
// Logging
//
// Shape: one "jev call" line per Ask with the transport facts, one "jev
// answer" line per decoded answer with the meaning. Both at INFO, because
// an operator who turned the classifier on wants to see what it decided
// without switching to DEBUG; the raw bodies, which are large and contain
// the sender's email, stay at DEBUG.
//
// Every helper tolerates a nil client and a nil logger.
// ---------------------------------------------------------------------------

func (c *Client) logger() *slog.Logger {
	if c == nil {
		return nil
	}
	return c.log
}

// logRequest echoes the outgoing call at DEBUG. The state is truncated
// (maxLogState) because it is an email body; the questions are perch's own
// fixed wording, so only their keys are worth a line.
func (c *Client) logRequest(purpose string, state any, questions map[string]Question) {
	log := c.logger()
	if log == nil || !log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	b, err := json.Marshal(state)
	if err != nil {
		b = []byte(fmt.Sprintf("(unmarshalable state: %v)", err))
	}
	log.Debug("jev request", "purpose", purpose, "model", c.model,
		"questions", strings.Join(sortedKeys(questions), ","),
		"state", truncate(string(b), maxLogState))
}

// logCall is the one line that always exists for a successful Ask.
func (c *Client) logCall(purpose string, r result, attempts int, took time.Duration) {
	log := c.logger()
	if log == nil {
		return
	}
	in, out := 0, 0
	if r.usage != nil {
		in, out = r.usage.InputTokens, r.usage.OutputTokens
	}
	log.Info("jev call", "purpose", purpose, "model", c.model, "status", r.status,
		"latency_ms", took.Milliseconds(), "attempts", attempts, "retries", attempts-1,
		"input_tokens", in, "output_tokens", out)
	if log.Enabled(context.Background(), slog.LevelDebug) {
		log.Debug("jev response", "purpose", purpose, "body", snippet(r.body))
	}
}

// logAnswers renders one line per decoded answer.
//
// confidence is logged under the key "vendor_confidence_not_a_probability"
// on purpose. It is the field every reader assumes they can threshold on,
// it is not a probability (see the package doc), and a name is the only
// documentation that travels with a log line.
func (c *Client) logAnswers(purpose string, answers map[string]Answer) {
	log := c.logger()
	if log == nil {
		return
	}
	for _, k := range sortedKeys(answers) {
		a := answers[k]
		attrs := []any{"purpose", purpose, "question", k, "type", a.Type}
		if a.Choice != "" {
			attrs = append(attrs, "choice", a.Choice, "prob", a.Prob())
		}
		if a.Score != nil {
			attrs = append(attrs, "score", *a.Score)
			// Legend is keyed by the rubric index as a string; show the band
			// the score rounds to, so the line reads without the request.
			// Clipped because a rubric entry is a sentence, and the point
			// here is to name the band, not to reprint the question.
			if len(a.Legend) > 0 {
				if lbl, ok := a.Legend[strconv.Itoa(int(*a.Score+0.5))]; ok {
					attrs = append(attrs, "level", truncate(lbl, maxLogLegend))
				}
			}
		}
		if a.Noul != nil {
			attrs = append(attrs, "noul", *a.Noul)
		}
		if len(a.Probabilities) > 0 {
			attrs = append(attrs, "probabilities", probsString(a.Probabilities))
		}
		if a.Confidence != nil {
			attrs = append(attrs, "vendor_confidence_not_a_probability", *a.Confidence)
		}
		log.Info("jev answer", attrs...)
	}
}

// logFailure records one failed attempt. It fires per attempt, not per
// Ask, so a retried 429 leaves a trail showing the wait that was honoured.
func (c *Client) logFailure(purpose string, r result, attempt int, took time.Duration) {
	log := c.logger()
	if log == nil {
		return
	}
	attrs := []any{"purpose", purpose, "model", c.model, "status", r.status,
		"attempt", attempt, "latency_ms", took.Milliseconds(), "err", r.err.Error()}
	if r.retryIn != nil {
		attrs = append(attrs, "retryable", true, "retry_after", *r.retryIn)
	}
	if len(r.body) > 0 {
		attrs = append(attrs, "body", snippet(r.body))
	}
	log.Warn("jev call failed", attrs...)
}

// probsString renders a probability map deterministically: sorted by key so
// two log lines for the same question are diffable.
func probsString(p map[string]float64) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%.4g", k, p[k])
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// truncate cuts s to at most n bytes, on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…(truncated)"
}

// retryAfter parses the header's delta-seconds form. Anything else (absent,
// HTTP-date, garbage) yields 0, meaning "use the caller's own backoff".
// Capped at 30s: a longer hint is not worth holding the mail queue for when
// there is a working fallback.
func retryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 0
	}
	if d := time.Duration(n) * time.Second; d < 30*time.Second {
		return d
	}
	return 30 * time.Second
}

// snippet renders a response body for an error message on a single line.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" {
		return "(empty body)"
	}
	return s
}
