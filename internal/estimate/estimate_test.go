package estimate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
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

// engagingDecider answers score questions with probs, noul questions with noul, link relations
// with "source" and link engagement with mode.
func engagingDecider(probs map[string]float64, noul float64, mode string) *deciderMock {
	return &deciderMock{SystemOneFunc: func(_ context.Context, req jev.Request) (jev.Response, error) {
		answers := make(map[string]jev.Answer, len(req.Questions))
		for id, q := range req.Questions {
			answer := jev.Answer{Type: q.Type, Probabilities: probs, Noul: noul}
			if q.Type == "choice" {
				answer.Choice = mode
				if strings.HasSuffix(id, "_relation") {
					answer.Choice = "source"
				}
				answer.Probabilities = map[string]float64{answer.Choice: 1}
			}
			answers[id] = answer
		}
		return jev.Response{Model: "jev-test", Answers: answers}, nil
	}}
}

func fixedDecider(probs map[string]float64, noul float64) *deciderMock {
	return engagingDecider(probs, noul, engageSkim)
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

// overflowingDecider rejects any request whose state matches tooLong, as Jev does past its context length.
func overflowingDecider(tooLong func(state string) bool, mode string) *deciderMock {
	inner := engagingDecider(map[string]float64{"0": 1}, 0.5, mode)
	return &deciderMock{SystemOneFunc: func(ctx context.Context, req jev.Request) (jev.Response, error) {
		if tooLong(req.State.(string)) {
			return jev.Response{}, &jev.APIError{StatusCode: http.StatusBadRequest, Body: `{"error":"max_tokens_exceeded"}`}
		}
		return inner.SystemOne(ctx, req)
	}}
}

func TestEstimateSendsWholeDocumentInOneRequest(t *testing.T) {
	t.Parallel()
	mock := fixedDecider(map[string]float64{"0": 1}, 1)
	paragraph := strings.Repeat("word ", 1000)
	rep, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), paragraph+"\n\n"+paragraph+"\n\n"+paragraph)
	require.NoError(t, err)
	assert.Len(t, mock.SystemOneCalls(), 1)
	assert.Equal(t, 1, rep.Chunks)
}

func TestEstimateSplitsInHalfWhenContextExceeded(t *testing.T) {
	t.Parallel()
	mock := overflowingDecider(func(state string) bool { return len(strings.Fields(state)) > 100 }, engageSkim)
	paragraph := strings.Repeat("word ", 60)
	rep, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), strings.Repeat(paragraph+"\n\n", 4))
	require.NoError(t, err)
	assert.Equal(t, 4, rep.Chunks)
	assert.Equal(t, 240, rep.Words)
	assert.Len(t, mock.SystemOneCalls(), 7)
}

func TestEstimateFailsWhenTextCannotBeSplitSmallEnough(t *testing.T) {
	t.Parallel()
	mock := overflowingDecider(func(string) bool { return true }, engageSkim)
	_, err := NewEstimator(mock, noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), "too long")
	assert.True(t, jev.IsContextExceeded(err))
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

func estimateLinked(t *testing.T, mode string) (Report, *deciderMock) {
	t.Helper()
	mock := engagingDecider(map[string]float64{"0": 1}, 0.5, mode)
	rep, err := NewEstimator(mock, guideFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	return rep, mock
}

func TestEstimateSkimmedLinkAddsFractionOfReadTimeWithoutEvaluatingIt(t *testing.T) {
	t.Parallel()
	rep, mock := estimateLinked(t, engageSkim)
	assert.InDelta(t, 48, crossfnResult(t, rep).Understand.Expected.Seconds(), 1e-6)
	assert.Len(t, mock.SystemOneCalls(), 1)
}

func TestEstimateStudiedLinkAddsItsUnderstandTime(t *testing.T) {
	t.Parallel()
	rep, mock := estimateLinked(t, engageStudy)
	assert.Equal(t, 90*time.Second, crossfnResult(t, rep).Understand.Expected)
	assert.Len(t, mock.SystemOneCalls(), 2)
}

func TestEstimateUnopenedLinkAddsNothing(t *testing.T) {
	t.Parallel()
	rep, _ := estimateLinked(t, engageSkip)
	assert.Equal(t, 30*time.Second, crossfnResult(t, rep).Understand.Expected)
}

func TestEstimateReportsLinkRelationAndEngagement(t *testing.T) {
	t.Parallel()
	rep, _ := estimateLinked(t, engageSkim)
	require.Len(t, rep.Links, 2)
	guide := rep.Links[0]
	assert.Equal(t, [4]any{"source", engageSkim, 238, false}, [4]any{guide.Relation, guide.Engagement["crossfn"], guide.Words, guide.Evaluated})
	assert.InDelta(t, 18, guide.Added["crossfn"].Expected.Seconds(), 1e-6)
}

func TestEstimateReportsSkippedLinks(t *testing.T) {
	t.Parallel()
	rep, _ := estimateLinked(t, engageSkim)
	require.Len(t, rep.Links, 2)
	assert.Equal(t, LinkResult{Text: "the spec", Target: "https://example.com/spec", External: true, Skipped: "external links disabled"}, rep.Links[1])
}

func TestEstimateAsksLinkQuestionsOnlyInChunkContainingLink(t *testing.T) {
	t.Parallel()
	mock := overflowingDecider(func(state string) bool {
		return strings.Contains(state, "one two") && strings.Contains(state, "[the guide]")
	}, engageSkim)
	_, err := NewEstimator(mock, guideFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), "one two three four five\n\n"+linkedText)
	require.NoError(t, err)
	for _, call := range mock.SystemOneCalls() {
		state := call.Req.State.(string)
		_, asked := call.Req.Questions[linkRelationID(0)]
		assert.Equal(t, strings.Contains(state, "[the guide]"), asked, state)
	}
}

func TestLinkInChunkFallsBackToOpeningChunk(t *testing.T) {
	t.Parallel()
	link := links.Link{Text: "escaped", Target: "my%20file.md"}
	assert.True(t, linkInChunk(link, chunk{text: "intro", first: true}, "intro [esc\\aped](<my file.md>)"))
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

func TestEstimateCountsStudiedLinksAndTheirChunks(t *testing.T) {
	t.Parallel()
	mock := overflowingDecider(func(state string) bool { return strings.Count(state, "word") > 200 }, engageStudy)
	rep, err := NewEstimator(mock, guideFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	assert.Equal(t, [3]int{2, 2, 3}, [3]int{rep.Documents, rep.Evaluated, rep.Chunks})
}

func TestEstimateCountsFetchedButUnevaluatedLinksAsDocuments(t *testing.T) {
	t.Parallel()
	rep, _ := estimateLinked(t, engageSkim)
	assert.Equal(t, [3]int{2, 1, 1}, [3]int{rep.Documents, rep.Evaluated, rep.Chunks})
}

func TestEstimateWithNegativeMaxLinksFollowsAll(t *testing.T) {
	t.Parallel()
	var text strings.Builder
	for i := range 25 {
		fmt.Fprintf(&text, "[doc %d](doc%d.md) ", i, i)
	}
	fetcher := guideFetcher()
	rep, err := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), fetcher, "jev-latest", -1, testLogger()).Estimate(t.Context(), text.String())
	require.NoError(t, err)
	assert.Len(t, rep.Links, 25)
}

func TestEstimateCapsLinksAtMaxLinks(t *testing.T) {
	t.Parallel()
	rep, err := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), guideFetcher(), "jev-latest", 1, testLogger()).Estimate(t.Context(), linkedText)
	require.NoError(t, err)
	assert.Len(t, rep.Links, 1)
}

func TestEstimateSkimIsFractionOfReadTime(t *testing.T) {
	t.Parallel()
	rep, err := NewEstimator(fixedDecider(map[string]float64{"0": 1}, 0.5), noFetcher(), "jev-latest", 10, testLogger()).Estimate(t.Context(), strings.Repeat("word ", 476))
	require.NoError(t, err)
	assert.InDelta(t, 36, crossfnResult(t, rep).Skim.Seconds(), 1e-6)
}

func TestEstimateCapsStudiedLinkAtReadAndSkimDepth(t *testing.T) {
	t.Parallel()
	rep, _ := estimateLinked(t, engageStudy)
	res := crossfnResult(t, rep)
	rootRead := 7.0 / 238 * 60
	assert.InDelta(t, rootRead+60, res.Read.Seconds(), 1e-6)
	assert.InDelta(t, rootRead*skimFraction+18, res.Skim.Seconds(), 1e-6)
}

func TestCapMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode, ceiling, want string
	}{
		{mode: engageStudy, ceiling: engageSkim, want: engageSkim},
		{mode: engageGlance, ceiling: engageSkim, want: engageGlance},
		{mode: engageRead, ceiling: engageRead, want: engageRead},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"_under_"+tt.ceiling, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, capMode(tt.mode, tt.ceiling))
		})
	}
}
