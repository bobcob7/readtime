// Package estimate turns Jev's calibrated judgements into reading, understanding and grokking times.
package estimate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bobcob7/readtime/internal/jev"
)

const defaultConcurrency = 4

var (
	errEmptyInput      = errors.New("input contains no words")
	errMissingAnswer   = errors.New("answer missing from response")
	errZeroProbability = errors.New("probabilities sum to zero")
)

// Range is an expected duration with a 10th–90th percentile band.
type Range struct {
	Expected time.Duration
	Low      time.Duration
	High     time.Duration
}

// Result is the estimate for one audience; every time includes the time spent on links.
// Skim and Read come from reading speed, with links capped at that depth of engagement.
// Understand and Grok are Jev's estimates of cumulative focused working time.
type Result struct {
	Audience       Audience
	Skim           time.Duration
	Read           time.Duration
	Understand     Range
	Grok           Range
	GrokLikelihood float64
}

// LinkResult describes how a link adds to the time to understand the main document.
type LinkResult struct {
	Text     string
	Target   string
	External bool
	Words    int
	// Relation is Jev's classification of how the linked content relates to the main document.
	Relation string
	// Evaluated reports whether Jev estimated the linked document itself, done only when someone may study it.
	Evaluated bool
	Chunks    int
	// Engagement maps audience ID to the most likely way that audience uses the link.
	Engagement map[string]string
	// Added maps audience ID to the probability-weighted time the link adds to understanding.
	Added   map[string]Range
	Skipped string
}

// Report is the full estimate for a piece of text. Words counts the main document. Documents counts
// it plus every fetched link; Evaluated and Chunks count the documents and pieces Jev assessed.
type Report struct {
	Words     int
	Documents int
	Evaluated int
	Chunks    int
	Model     string
	Results   []Result
	Links     []LinkResult
}

// Estimator asks Jev how hard text is for each audience and converts the answers to durations.
type Estimator struct {
	decider     decider
	fetcher     linkFetcher
	model       string
	maxLinks    int
	concurrency int
	logger      *slog.Logger
}

// NewEstimator returns an Estimator that queries model and follows up to maxLinks links
// (0 disables links, negative follows all).
func NewEstimator(d decider, f linkFetcher, model string, maxLinks int, logger *slog.Logger) *Estimator {
	return &Estimator{
		decider:     d,
		fetcher:     f,
		model:       model,
		maxLinks:    maxLinks,
		concurrency: defaultConcurrency,
		logger:      logger,
	}
}

// Estimate returns per-audience time estimates for text, including time spent on its links.
func (e *Estimator) Estimate(ctx context.Context, text string) (Report, error) {
	doc := wholeText(text)
	if doc.words == 0 {
		return Report{}, errEmptyInput
	}
	audiences := Audiences()
	docs := e.fetchLinks(ctx, text)
	evals, err := e.evaluate(ctx, doc, questionsFor(audiences, docs, text))
	if err != nil {
		return Report{}, err
	}
	results, err := aggregateAll(audiences, evals)
	if err != nil {
		return Report{}, err
	}
	if err := e.studyLinks(ctx, audiences, docs, evals); err != nil {
		return Report{}, err
	}
	linkResults, err := applyLinks(results, docs, evals)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Words: doc.words, Documents: 1, Evaluated: 1, Chunks: len(evals), Model: evals[0].resp.Model, Results: results, Links: linkResults}
	for _, link := range linkResults {
		if link.Skipped == "" {
			rep.Documents++
		}
		if link.Evaluated {
			rep.Evaluated++
			rep.Chunks += link.Chunks
		}
	}
	return rep, nil
}

// evaluate sends c whole, splitting it in half (recursively) only when it exceeds the model's context length.
func (e *Estimator) evaluate(ctx context.Context, c chunk, questionsFor func(chunk) map[string]jev.Question) ([]evaluation, error) {
	resp, err := e.decider.SystemOne(ctx, jev.Request{State: c.text, Model: e.model, Questions: questionsFor(c)})
	if err == nil {
		e.logger.DebugContext(ctx, "text evaluated", "words", c.words, "input_tokens", resp.Usage.InputTokens)
		return []evaluation{{chunk: c, resp: resp}}, nil
	}
	if !jev.IsContextExceeded(err) {
		return nil, fmt.Errorf("evaluating %d words: %w", c.words, err)
	}
	parts := splitChunks(c.text, (c.words+1)/2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("evaluating %d words: %w", c.words, err)
	}
	parts[0].first = c.first
	e.logger.DebugContext(ctx, "text exceeds model context; splitting", "words", c.words, "parts", len(parts))
	evals := make([][]evaluation, len(parts))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	for i, part := range parts {
		g.Go(func() error {
			var err error
			evals[i], err = e.evaluate(ctx, part, questionsFor)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(evals...), nil
}
