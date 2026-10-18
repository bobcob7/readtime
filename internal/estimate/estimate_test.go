package estimate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/links"
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

func noFetcher() *linkFetcherMock {
	return &linkFetcherMock{FetchFunc: func(context.Context, links.Link) (string, error) {
		return "", errors.New("unexpected fetch")
	}}
}

func crossfnResult(t *testing.T, r Report) Result {
	t.Helper()
	for _, res := range r.Results {
		if res.Audience.ID == "crossfn" {
			return res
		}
	}
	require.FailNow(t, "crossfn audience missing")
	return Result{}
}

func TestEstimateUsesRepresentativeTimeOfChosenBucket(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"2": 1}, 0.9), noFetcher(), "jev-latest", 10, testLogger())
	rep, err := est.Estimate(t.Context(), "A short passage.")
	require.NoError(t, err)
	assert.InDelta(t, math.Sqrt(30), crossfnResult(t, rep).Understand.Expected.Minutes(), 1e-6)
}

func TestEstimateNeverUnderstandsFasterThanReading(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.9), noFetcher(), "jev-latest", 10, testLogger())
	rep, err := est.Estimate(t.Context(), strings.Repeat("word ", 476))
	require.NoError(t, err)
	res := crossfnResult(t, rep)
	assert.Equal(t, 2*time.Minute, res.Understand.Expected)
}

func TestEstimateNeverGroksFasterThanUnderstanding(t *testing.T) {
	t.Parallel()
	mock := &deciderMock{SystemOneFunc: func(_ context.Context, req jev.Request) (jev.Response, error) {
		answers := make(map[string]jev.Answer, len(req.Questions))
		for id, q := range req.Questions {
			level := "0"
			if strings.HasSuffix(id, "_"+kindUnderstand) {
				level = "5"
			}
			answers[id] = jev.Answer{Type: q.Type, Probabilities: map[string]float64{level: 1}, Noul: 0.5}
		}
		return jev.Response{Answers: answers}, nil
	}}
	rep, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), "A short passage.")
	require.NoError(t, err)
	res := crossfnResult(t, rep)
	assert.Equal(t, res.Understand, res.Grok)
}

func TestEstimateWithSpreadDistributionProducesWiderBand(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 0.05, "1": 0.15, "2": 0.6, "3": 0.15, "4": 0.05}, 0.5), noFetcher(), "jev-latest", 10, testLogger())
	rep, err := est.Estimate(t.Context(), "A short passage.")
	require.NoError(t, err)
	res := crossfnResult(t, rep)
	assert.InDelta(t, math.Sqrt(3), res.Understand.Low.Minutes(), 1e-6)
	assert.InDelta(t, math.Sqrt(300), res.Understand.High.Minutes(), 1e-6)
}

func TestEstimateCallsDeciderOncePerChunk(t *testing.T) {
	t.Parallel()
	mock := fixedDecider(map[string]float64{"0": 1}, 1)
	est := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger())
	est.chunkWords = 10
	rep, err := est.Estimate(t.Context(), strings.Repeat("word ", 25))
	require.NoError(t, err)
	assert.Len(t, mock.SystemOneCalls(), 3)
	assert.Equal(t, 25, rep.Words)
}

func TestEstimateRejectsEmptyInput(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(nil, 0), noFetcher(), "jev-latest", 10, testLogger())
	_, err := est.Estimate(t.Context(), " \n\n\t ")
	assert.ErrorIs(t, err, errEmptyInput)
}

func TestEstimatePropagatesDeciderErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	mock := &deciderMock{SystemOneFunc: func(context.Context, jev.Request) (jev.Response, error) {
		return jev.Response{}, boom
	}}
	_, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), "some text")
	assert.ErrorIs(t, err, boom)
}

func TestEstimateFailsWhenAnswerMissing(t *testing.T) {
	t.Parallel()
	mock := &deciderMock{SystemOneFunc: func(context.Context, jev.Request) (jev.Response, error) {
		return jev.Response{Answers: map[string]jev.Answer{}}, nil
	}}
	_, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), "some text")
	assert.ErrorIs(t, err, errMissingAnswer)
}

func guideFetcher() *linkFetcherMock {
	return &linkFetcherMock{FetchFunc: func(_ context.Context, link links.Link) (string, error) {
		if link.External {
			return "", errors.New("external links disabled")
		}
		return strings.Repeat("word ", 238), nil
	}}
}

const linkedText = "See [the guide](guide.md) and [the spec](https://example.com/spec) first."

func TestEstimateAddsLinkedContentWeightedByNeed(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), guideFetcher(), "jev-latest", 10, testLogger())
	rep, err := est.Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, crossfnResult(t, rep).Understand.Expected)
}

func TestEstimateReportsNeedAndSkippedLinks(t *testing.T) {
	t.Parallel()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), guideFetcher(), "jev-latest", 10, testLogger())
	rep, err := est.Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	require.Len(t, rep.Links, 2)
	assert.Equal(t, LinkResult{Text: "the guide", Target: "guide.md", Words: 238, NeededBy: map[string]float64{"esl": 0.5, "crossfn": 0.5, "junior": 0.5, "senior": 0.5, "expert": 0.5}}, rep.Links[0])
	assert.Equal(t, LinkResult{Text: "the spec", Target: "https://example.com/spec", External: true, Skipped: "external links disabled"}, rep.Links[1])
}

func TestEstimateAsksLinkQuestionsOnlyInChunkContainingLink(t *testing.T) {
	t.Parallel()
	mock := fixedDecider(map[string]float64{"0": 1}, 0.5)
	est := NewEstimator(mock, guideFetcher(), "jev-latest", 10, testLogger())
	est.chunkWords = 5
	_, err := est.Estimate(t.Context(), "one two three four five\n\n"+linkedText)
	require.NoError(t, err)
	for _, call := range mock.SystemOneCalls() {
		_, asked := call.Req.Questions[linkQuestionID(0, Audiences()[0])]
		assert.Equal(t, strings.Contains(call.Req.State.(string), "[the guide]"), asked)
	}
}

func TestEstimateWithZeroMaxLinksSkipsFetching(t *testing.T) {
	t.Parallel()
	fetcher := guideFetcher()
	est := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), fetcher, "jev-latest", 0, testLogger())
	rep, err := est.Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	assert.Empty(t, fetcher.FetchCalls())
	assert.Empty(t, rep.Links)
}

func TestQuantile(t *testing.T) {
	t.Parallel()
	levels := understandLevels()
	tests := []struct {
		name  string
		probs []float64
		q     float64
		want  float64
	}{
		{name: "all mass on first level", probs: []float64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}, q: 0.9, want: 0.5},
		{name: "low tail lands on first level with mass", probs: []float64{0, 0.2, 0.8, 0, 0, 0, 0, 0, 0, 0}, q: 0.1, want: math.Sqrt(3)},
		{name: "high tail lands on last level with mass", probs: []float64{0, 0.2, 0.7, 0.1, 0, 0, 0, 0, 0, 0}, q: 0.9, want: math.Sqrt(30)},
		{name: "underflowing sum falls back to top level", probs: make([]float64, 10), q: 0.5, want: math.Sqrt(24 * 80 * 3600)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tt.want, quantile(tt.probs, levels, tt.q), 1e-6)
		})
	}
}

func TestRepresentativeIsGeometricMidpoint(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 2*time.Hour, timeLevel{low: time.Hour, high: 4 * time.Hour}.representative())
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
