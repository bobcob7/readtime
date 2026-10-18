// Package estimate turns Jev's calibrated judgements into reading, understanding and grokking times.
package estimate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bobcob7/readtime/internal/jev"
)

const (
	defaultChunkWords  = 1500
	defaultConcurrency = 4
	lowQuantile        = 0.1
	highQuantile       = 0.9
	quantileTolerance  = 1e-9
	kindUnderstand     = "understand"
	kindGrok           = "grok"
	kindFeasible       = "feasible"
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

// Result is the estimate for one audience. Understand and Grok are cumulative, including read time.
type Result struct {
	Audience       Audience
	Read           time.Duration
	Understand     Range
	Grok           Range
	GrokLikelihood float64
}

// Report is the full estimate for a piece of text.
type Report struct {
	Words   int
	Chunks  int
	Model   string
	Results []Result
}

// Estimator asks Jev how hard text is for each audience and converts the answers to durations.
type Estimator struct {
	decider     decider
	model       string
	chunkWords  int
	concurrency int
	logger      *slog.Logger
}

// NewEstimator returns an Estimator that queries the given model.
func NewEstimator(d decider, model string, logger *slog.Logger) *Estimator {
	return &Estimator{decider: d, model: model, chunkWords: defaultChunkWords, concurrency: defaultConcurrency, logger: logger}
}

// Estimate returns per-audience time estimates for text.
func (e *Estimator) Estimate(ctx context.Context, text string) (Report, error) {
	chunks := splitChunks(text, e.chunkWords)
	if len(chunks) == 0 {
		return Report{}, errEmptyInput
	}
	audiences := Audiences()
	responses, err := e.evaluate(ctx, chunks, buildQuestions(audiences))
	if err != nil {
		return Report{}, err
	}
	results := make([]Result, 0, len(audiences))
	words := 0
	for _, c := range chunks {
		words += c.words
	}
	for _, aud := range audiences {
		res, err := aggregate(aud, chunks, responses)
		if err != nil {
			return Report{}, fmt.Errorf("aggregating %s: %w", aud.ID, err)
		}
		results = append(results, res)
	}
	return Report{Words: words, Chunks: len(chunks), Model: responses[0].Model, Results: results}, nil
}

func (e *Estimator) evaluate(ctx context.Context, chunks []chunk, questions map[string]jev.Question) ([]jev.Response, error) {
	responses := make([]jev.Response, len(chunks))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	for i, c := range chunks {
		g.Go(func() error {
			resp, err := e.decider.SystemOne(ctx, jev.Request{State: c.text, Model: e.model, Questions: questions})
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

func buildQuestions(audiences []Audience) map[string]jev.Question {
	questions := make(map[string]jev.Question, len(audiences)*3)
	for _, aud := range audiences {
		questions[questionID(aud, kindUnderstand)] = jev.Score(
			fmt.Sprintf("The reader is %s. How much effort does this reader need to understand what the text says?", aud.Description),
			descriptions(comprehensionLevels())...,
		)
		questions[questionID(aud, kindGrok)] = jev.Score(
			fmt.Sprintf("The reader is %s. Once they understand the text, how much more work does this reader need to grok it: internalise the ideas well enough to explain them, apply them and reason about their implications?", aud.Description),
			descriptions(grokLevels())...,
		)
		questions[questionID(aud, kindFeasible)] = jev.Noul(
			fmt.Sprintf("The reader is %s. Could this reader realistically come to grok this text with effort?", aud.Description),
			"The reader can get there with study and practice",
			"The text is out of reach for this reader",
		)
	}
	return questions
}

func questionID(aud Audience, kind string) string {
	return aud.ID + "_" + kind
}

// band holds expected, low and high values in minutes or as multipliers.
type band struct {
	expected, low, high float64
}

func (b band) add(o band) band {
	return band{expected: b.expected + o.expected, low: b.low + o.low, high: b.high + o.high}
}

func (b band) toRange() Range {
	return Range{Expected: minutes(b.expected), Low: minutes(b.low), High: minutes(b.high)}
}

func minutes(m float64) time.Duration {
	return time.Duration(m * float64(time.Minute))
}

// aggregate sums per-chunk times; summing percentiles assumes chunks of one text are strongly correlated.
func aggregate(aud Audience, chunks []chunk, responses []jev.Response) (Result, error) {
	var understand, grok band
	var readMinutes, likelihood float64
	words := 0
	for i, c := range chunks {
		resp := responses[i]
		u, err := answerBand(resp, questionID(aud, kindUnderstand), comprehensionLevels())
		if err != nil {
			return Result{}, fmt.Errorf("chunk %d: %w", i+1, err)
		}
		g, err := answerBand(resp, questionID(aud, kindGrok), grokLevels())
		if err != nil {
			return Result{}, fmt.Errorf("chunk %d: %w", i+1, err)
		}
		feasible, ok := resp.Answers[questionID(aud, kindFeasible)]
		if !ok {
			return Result{}, fmt.Errorf("chunk %d: %s: %w", i+1, questionID(aud, kindFeasible), errMissingAnswer)
		}
		read := float64(c.words) / aud.WordsPerMinute
		readMinutes += read
		understand = understand.add(band{expected: read * u.expected, low: read * u.low, high: read * u.high})
		grok = grok.add(band{expected: read * u.expected * g.expected, low: read * u.low * g.low, high: read * u.high * g.high})
		likelihood += feasible.Noul * float64(c.words)
		words += c.words
	}
	return Result{
		Audience:       aud,
		Read:           minutes(readMinutes),
		Understand:     understand.toRange(),
		Grok:           grok.toRange(),
		GrokLikelihood: likelihood / float64(words),
	}, nil
}

func answerBand(resp jev.Response, id string, levels []level) (band, error) {
	answer, ok := resp.Answers[id]
	if !ok {
		return band{}, fmt.Errorf("%s: %w", id, errMissingAnswer)
	}
	probs, err := distribution(answer, len(levels))
	if err != nil {
		return band{}, fmt.Errorf("%s: %w", id, err)
	}
	return band{
		expected: expectedMultiplier(probs, levels),
		low:      quantile(probs, levels, lowQuantile),
		high:     quantile(probs, levels, highQuantile),
	}, nil
}

// distribution normalises an answer's level probabilities, treating omitted levels as zero.
func distribution(answer jev.Answer, n int) ([]float64, error) {
	probs := make([]float64, n)
	total := 0.0
	for i := range n {
		probs[i] = answer.Probabilities[strconv.Itoa(i)]
		total += probs[i]
	}
	if total <= 0 {
		return nil, errZeroProbability
	}
	for i := range probs {
		probs[i] /= total
	}
	return probs, nil
}

func expectedMultiplier(probs []float64, levels []level) float64 {
	sum := 0.0
	for i, p := range probs {
		sum += p * levels[i].multiplier
	}
	return sum
}

func quantile(probs []float64, levels []level, q float64) float64 {
	cumulative := 0.0
	for i, p := range probs {
		cumulative += p
		if cumulative >= q-quantileTolerance {
			return levels[i].multiplier
		}
	}
	return levels[len(levels)-1].multiplier
}
