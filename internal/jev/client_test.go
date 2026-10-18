package jev

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient(server.Client(), server.URL, "test-key", slog.New(slog.NewJSONHandler(io.Discard, nil)))
	client.retryBase = time.Millisecond
	return client
}

func TestSystemOneSendsAuthenticatedRequestAndDecodesAnswers(t *testing.T) {
	t.Parallel()
	var got Request
	var auth string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"q":{"type":"score","score":1.4,"probabilities":{"0":0.1,"1":0.5,"2":0.4},"confidence":0.8}},"usage":{"input_tokens":10,"output_tokens":2}}`)
	})
	resp, err := client.SystemOne(t.Context(), Request{State: "hi", Model: DefaultModel, Questions: map[string]Question{"q": Score("How?", "a", "b", "c")}})
	require.NoError(t, err)
	assert.Equal(t, "Bearer test-key", auth)
	assert.Equal(t, "score", got.Questions["q"].Type)
	assert.Equal(t, 0.5, resp.Answers["q"].Probabilities["1"])
}

func TestSystemOneRetriesRateLimitedResponses(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"model":"jev","answers":{}}`)
	})
	_, err := client.SystemOne(t.Context(), Request{State: "hi", Model: DefaultModel})
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load())
}

func TestSystemOneDoesNotRetryValidationErrors(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"questions.q.criteria required"}`)
	})
	_, err := client.SystemOne(t.Context(), Request{State: "hi", Model: DefaultModel})
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusUnprocessableEntity, apiErr.StatusCode)
	assert.Equal(t, int32(1), calls.Load())
}

func TestSystemOneGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(statusOverloaded)
	})
	_, err := client.SystemOne(t.Context(), Request{State: "hi", Model: DefaultModel})
	require.Error(t, err)
	assert.Equal(t, int32(defaultMaxRetries+1), calls.Load())
}
