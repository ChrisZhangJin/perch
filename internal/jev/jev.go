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
// The package logs nothing. Errors quote the HTTP status and a truncated
// response body; the API key appears in neither (see TestErrorsNeverLeakKey).
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	// vendor HTML error page into the log.
	maxErrBody = 4096
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
}

// New returns a client, or nil when apiKey is empty.
//
// The nil return is the feature flag: "Jev is not configured" needs no
// separate boolean, and every method here is nil-safe, so callers branch
// once at the call site and nowhere else. Same shape as hook.New.
//
// Empty apiURL / model / timeout fall back to the Default* constants.
func New(apiKey, apiURL, model string, timeout time.Duration) *Client {
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
//
// 429 and 529 are retried with exponential backoff, honouring a Retry-After
// header when the server sends one. Every other non-2xx fails immediately:
// a 401 or a 422 will fail identically on the next attempt and the caller
// has a fallback path that is cheaper than waiting.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error) {
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
		answers, retryIn, err := c.post(ctx, payload)
		if err == nil {
			return answers, nil
		}
		lastErr = err
		if retryIn == nil {
			return nil, err // terminal: 401, 422, bad JSON, transport
		}
		wait = *retryIn
	}
	return nil, fmt.Errorf("%w after %d attempts: %v", ErrOverloaded, maxRetries+1, lastErr)
}

// post makes one attempt. A non-nil second return means "retryable"; its
// value is the server-requested delay, or 0 for "use the caller's backoff".
func (c *Client) post(ctx context.Context, payload []byte) (map[string]Answer, *time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("jev: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)

	resp, err := c.hc.Do(req)
	if err != nil {
		// Transport-level failures (DNS, TLS, timeout) are not retried here:
		// the caller's fallback is an agent run that will succeed, and a
		// second dial costs the mail queue another timeout.
		return nil, nil, fmt.Errorf("jev: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, nil, fmt.Errorf("%w (401): %s", ErrAuth, snippet(body))
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return nil, nil, fmt.Errorf("%w (422): %s", ErrValidation, snippet(body))
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529:
		d := retryAfter(resp.Header.Get("Retry-After"))
		return nil, &d, fmt.Errorf("jev: %d: %s", resp.StatusCode, snippet(body))
	default:
		return nil, nil, fmt.Errorf("jev: unexpected status %d: %s", resp.StatusCode, snippet(body))
	}

	var out response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, nil, fmt.Errorf("jev: decode response: %w", err)
	}
	if len(out.Answers) == 0 {
		return nil, nil, errors.New("jev: response carried no answers")
	}
	return out.Answers, nil, nil
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
