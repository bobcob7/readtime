// Package estimate turns Jev's calibrated judgements into reading, understanding and grokking times.
package estimate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bobcob7/readtime/internal/jev"
)

const (
	defaultChunkWords  = 1500
	defaultConcurrency = 4
)

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

// Result is the estimate for one audience. Read covers the document alone; Understand and Grok are
// cumulative and include linked content weighted by how likely the audience needs it.
type Result struct {
	Audience       Audience
	Read           time.Duration
	Understand     Range
	Grok           Range
	GrokLikelihood float64
}

// LinkResult describes one followed (or skipped) link.
type LinkResult struct {
	Text     string
	Target   string
	External bool
	Words    int
	// NeededBy maps audience ID to the probability that audience must read the link.
	NeededBy map[string]float64
	Skipped  string
}

// Report is the full estimate for a piece of text.
type Report struct {
	Words   int
	Chunks  int
	Model   string
	Results []Result
	Links   []LinkResult
}

// Estimator asks Jev how hard text is for each audience and converts the answers to durations.
type Estimator struct {
	decider     decider
	fetcher     linkFetcher
	model       string
	maxLinks    int
	chunkWords  int
	concurrency int
	logger      *slog.Logger
}

// NewEstimator returns an Estimator that queries model and follows up to maxLinks links (0 disables).
func NewEstimator(d decider, f linkFetcher, model string, maxLinks int, logger *slog.Logger) *Estimator {
	return &Estimator{
		decider:     d,
		fetcher:     f,
		model:       model,
		maxLinks:    maxLinks,
		chunkWords:  defaultChunkWords,
		concurrency: defaultConcurrency,
		logger:      logger,
	}
}

// Estimate returns per-audience time estimates for text, including essential linked content.
func (e *Estimator) Estimate(ctx context.Context, text string) (Report, error) {
	chunks := splitChunks(text, e.chunkWords)
	if len(chunks) == 0 {
		return Report{}, errEmptyInput
	}
	audiences := Audiences()
	docs, err := e.followLinks(ctx, text, chunks, audiences)
	if err != nil {
		return Report{}, err
	}
	responses, err := e.evaluate(ctx, chunks, chunkQuestions(audiences, len(chunks), docs))
	if err != nil {
		return Report{}, err
	}
	results, err := aggregateAll(audiences, chunks, responses)
	if err != nil {
		return Report{}, err
	}
	linkResults, err := applyLinks(results, docs, responses)
	if err != nil {
		return Report{}, err
	}
	return Report{Words: totalWords(chunks), Chunks: len(chunks), Model: responses[0].Model, Results: results, Links: linkResults}, nil
}

// estimateLinked estimates a linked document on its own, without following its links.
func (e *Estimator) estimateLinked(ctx context.Context, chunks []chunk, audiences []Audience) ([]Result, error) {
	responses, err := e.evaluate(ctx, chunks, chunkQuestions(audiences, len(chunks), nil))
	if err != nil {
		return nil, err
	}
	return aggregateAll(audiences, chunks, responses)
}

func (e *Estimator) evaluate(ctx context.Context, chunks []chunk, questions []map[string]jev.Question) ([]jev.Response, error) {
	responses := make([]jev.Response, len(chunks))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	for i, c := range chunks {
		g.Go(func() error {
			resp, err := e.decider.SystemOne(ctx, jev.Request{State: c.text, Model: e.model, Questions: questions[i]})
			if err != nil {
				return fmt.Errorf("evaluating chunk %d: %w", i+1, err)
			}
			e.logger.DebugContext(ctx, "chunk evaluated", "chunk", i+1, "words", c.words, "input_tokens", resp.Usage.InputTokens)
			responses[i] = resp
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return responses, nil
}
