// Package jev is a minimal client for the TypeSafe System One decisions API.
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
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the TypeSafe API origin.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel always resolves to the newest Jev release.
	DefaultModel      = "jev-latest"
	statusOverloaded  = 529
	maxResponseBytes  = 4 << 20
	defaultMaxRetries = 4
	defaultRetryBase  = 500 * time.Millisecond
)

// Question is a typed question sent to System One.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Score builds a question rated on ordered rubric levels, lowest first.
func Score(instructions string, levels ...string) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: levels}
}

// Noul builds a yes/no question with descriptions of what each answer means.
func Noul(instructions, ifTrue, ifFalse string) Question {
	return Question{Type: "noul", Instructions: instructions, Criteria: map[string]string{"true": ifTrue, "false": ifFalse}}
}

// Request is the body of a System One call.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer is the model's answer to one question. Which fields are set depends on Type.
type Answer struct {
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Choice        string             `json:"choice"`
	Noul          float64            `json:"noul"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Usage reports token consumption for a call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the body returned by a System One call.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// APIError is a non-200 response from the API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("system one returned %d: %s", e.StatusCode, e.Body)
}

func (e *APIError) retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode == statusOverloaded
}

// Client calls the System One API, retrying rate-limit and overload responses.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	maxRetries int
	retryBase  time.Duration
	logger     *slog.Logger
}

// NewClient returns a Client that authenticates with apiKey against baseURL.
func NewClient(httpClient *http.Client, baseURL, apiKey string, logger *slog.Logger) *Client {
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		maxRetries: defaultMaxRetries,
		retryBase:  defaultRetryBase,
		logger:     logger,
	}
}

// SystemOne asks the model the request's questions about its state.
func (c *Client) SystemOne(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("encoding request: %w", err)
	}
	for attempt := 0; ; attempt++ {
		resp, err := c.post(ctx, body)
		var apiErr *APIError
		if err == nil || !errors.As(err, &apiErr) || !apiErr.retryable() || attempt >= c.maxRetries {
			return resp, err
		}
		delay := c.retryBase << attempt
		c.logger.WarnContext(ctx, "retrying system one call", "status", apiErr.StatusCode, "attempt", attempt+1, "delay", delay)
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (c *Client) post(ctx context.Context, body []byte) (Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("calling system one: %w", err)
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
	if err != nil {
		return Response{}, fmt.Errorf("reading response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return Response{}, &APIError{StatusCode: httpResp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var out Response
	if err := json.Unmarshal(data, &out); err != nil {
		return Response{}, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}
