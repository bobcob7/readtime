package estimate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bobcob7/readtime/internal/jev"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// fixedDecider answers every score question with probs and every noul question with noul.
func fixedDecider(probs map[string]float64, noul float64) *deciderMock {
	return &deciderMock{SystemOneFunc: func(_ context.Context, req jev.Request) (jev.Response, error) {
		answers := make(map[string]jev.Answer, len(req.Questions))
		for id, q := range req.Questions {
			answers[id] = jev.Answer{Type: q.Type, Probabilities: probs, Noul: noul}
		}
		return jev.Response{Model: "jev-test", Answers: answers}, nil
	}}
}

func adultResult(t *testing.T, r Report) Result {
	t.Helper()
	for _, res := range r.Results {
		if res.Audience.ID == "adult" {
			return res
		}
	}
	require.FailNow(t, "adult audience missing")
	return Result{}
}

func TestEstimateWithEffortlessTextScalesReadTimeByLowestMultipliers(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.9), "jev-latest", testLogger())
	rep, err := est.Estimate(t.Context(), strings.Repeat("word ", 476))
	require.NoError(t, err)
	adult := adultResult(t, rep)
	assert.Equal(t, 2*time.Minute, adult.Read)
	assert.Equal(t, 2*time.Minute, adult.Understand.Expected)
	assert.InDelta(t, (144 * time.Second).Seconds(), adult.Grok.Expected.Seconds(), 0.001)
}

func TestEstimateWithSpreadDistributionProducesWiderBand(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 0.05, "1": 0.15, "2": 0.6, "3": 0.15, "4": 0.05}, 0.5), "jev-latest", testLogger())
	rep, err := est.Estimate(t.Context(), strings.Repeat("word ", 476))
	require.NoError(t, err)
	adult := adultResult(t, rep)
	assert.Equal(t, time.Duration(2.8*float64(time.Minute)), adult.Understand.Low)
	assert.Equal(t, 6*time.Minute, adult.Understand.High)
}

func TestEstimateCallsDeciderOncePerChunk(t *testing.T) {
	t.Parallel()
	mock := fixedDecider(map[string]float64{"0": 1}, 1)
	est := NewEstimator(mock, "jev-latest", testLogger())
	est.chunkWords = 10
	rep, err := est.Estimate(t.Context(), strings.Repeat("word ", 25))
	require.NoError(t, err)
	assert.Len(t, mock.SystemOneCalls(), 3)
	assert.Equal(t, 25, rep.Words)
}

func TestEstimateRejectsEmptyInput(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(nil, 0), "jev-latest", testLogger())
	_, err := est.Estimate(t.Context(), " \n\n\t ")
	assert.ErrorIs(t, err, errEmptyInput)
}

func TestEstimatePropagatesDeciderErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	mock := &deciderMock{SystemOneFunc: func(context.Context, jev.Request) (jev.Response, error) {
		return jev.Response{}, boom
	}}
	_, err := NewEstimator(mock, "jev-latest", testLogger()).Estimate(t.Context(), "some text")
	assert.ErrorIs(t, err, boom)
}

func TestEstimateFailsWhenAnswerMissing(t *testing.T) {
	t.Parallel()
	mock := &deciderMock{SystemOneFunc: func(context.Context, jev.Request) (jev.Response, error) {
		return jev.Response{Answers: map[string]jev.Answer{}}, nil
	}}
	_, err := NewEstimator(mock, "jev-latest", testLogger()).Estimate(t.Context(), "some text")
	assert.ErrorIs(t, err, errMissingAnswer)
}

func TestQuantile(t *testing.T) {
	t.Parallel()
	levels := comprehensionLevels()
	tests := []struct {
		name  string
		probs []float64
		q     float64
		want  float64
	}{
		{name: "all mass on first level", probs: []float64{1, 0, 0, 0, 0}, q: 0.9, want: 1.0},
		{name: "low tail lands on first level with mass", probs: []float64{0, 0.2, 0.8, 0, 0}, q: 0.1, want: 1.4},
		{name: "high tail lands on last level with mass", probs: []float64{0, 0.2, 0.7, 0.1, 0}, q: 0.9, want: 2.0},
		{name: "underflowing sum falls back to top level", probs: []float64{0, 0, 0, 0, 0}, q: 0.5, want: 5.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, quantile(tt.probs, levels, tt.q))
		})
	}
}

func TestDistributionNormalisesProbabilities(t *testing.T) {
	t.Parallel()
	probs, err := distribution(jev.Answer{Probabilities: map[string]float64{"0": 1, "1": 3}}, 2)
	require.NoError(t, err)
	assert.Equal(t, []float64{0.25, 0.75}, probs)
}

func TestDistributionTreatsOmittedLevelsAsZero(t *testing.T) {
	t.Parallel()
	probs, err := distribution(jev.Answer{Probabilities: map[string]float64{"0": 1}}, 2)
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 0}, probs)
}

func TestDistributionRejectsAllZeroProbabilities(t *testing.T) {
	t.Parallel()
	_, err := distribution(jev.Answer{}, 2)
	assert.ErrorIs(t, err, errZeroProbability)
}

func TestSplitChunks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		text      string
		maxWords  int
		wantWords []int
	}{
		{name: "blank input yields no chunks", text: "  \n\n ", maxWords: 10, wantWords: nil},
		{name: "small paragraphs merge", text: "a b\n\nc d\r\n\r\ne", maxWords: 10, wantWords: []int{5}},
		{name: "paragraphs split at limit", text: "a b c\n\nd e f", maxWords: 4, wantWords: []int{3, 3}},
		{name: "oversized paragraph splits by word", text: "a b c d e", maxWords: 2, wantWords: []int{2, 2, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []int
			for _, c := range splitChunks(tt.text, tt.maxWords) {
				got = append(got, c.words)
			}
			assert.Equal(t, tt.wantWords, got)
		})
	}
}
