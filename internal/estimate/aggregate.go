package estimate

import (
	"fmt"
	"strconv"
	"time"

	"github.com/bobcob7/readtime/internal/jev"
)

const (
	lowQuantile       = 0.1
	highQuantile      = 0.9
	quantileTolerance = 1e-9
)

func (r Range) plus(o Range) Range {
	return Range{Expected: r.Expected + o.Expected, Low: r.Low + o.Low, High: r.High + o.High}
}

func (r Range) scale(f float64) Range {
	return Range{Expected: time.Duration(float64(r.Expected) * f), Low: time.Duration(float64(r.Low) * f), High: time.Duration(float64(r.High) * f)}
}

// band holds expected, low and high values in minutes.
type band struct {
	expected, low, high float64
}

func (b band) add(o band) band {
	return band{expected: b.expected + o.expected, low: b.low + o.low, high: b.high + o.high}
}

// atLeast raises each value to the floor's, e.g. so understanding never takes less than reading.
func (b band) atLeast(floor band) band {
	return band{expected: max(b.expected, floor.expected), low: max(b.low, floor.low), high: max(b.high, floor.high)}
}

func (b band) toRange() Range {
	return Range{Expected: minutes(b.expected), Low: minutes(b.low), High: minutes(b.high)}
}

func minutes(m float64) time.Duration {
	return time.Duration(m * float64(time.Minute))
}

func totalWords(chunks []chunk) int {
	words := 0
	for _, c := range chunks {
		words += c.words
	}
	return words
}

func aggregateAll(audiences []Audience, chunks []chunk, responses []jev.Response) ([]Result, error) {
	results := make([]Result, 0, len(audiences))
	for _, aud := range audiences {
		res, err := aggregate(aud, chunks, responses)
		if err != nil {
			return nil, fmt.Errorf("aggregating %s: %w", aud.ID, err)
		}
		results = append(results, res)
	}
	return results, nil
}

// aggregate sums per-chunk times; summing percentiles assumes chunks of one text are strongly correlated.
// Read time comes from reading speed; understand and grok come from Jev's time-bucket judgements.
func aggregate(aud Audience, chunks []chunk, responses []jev.Response) (Result, error) {
	var understand, grok band
	var readMinutes, likelihood float64
	for i, c := range chunks {
		resp := responses[i]
		u, err := answerBand(resp, questionID(aud, kindUnderstand), understandLevels())
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
		u = u.atLeast(band{expected: read, low: read, high: read})
		understand = understand.add(u)
		grok = grok.add(g.atLeast(u))
		likelihood += feasible.Noul * float64(c.words)
	}
	return Result{
		Audience:       aud,
		Read:           minutes(readMinutes),
		Understand:     understand.toRange(),
		Grok:           grok.toRange(),
		GrokLikelihood: likelihood / float64(totalWords(chunks)),
	}, nil
}

func answerBand(resp jev.Response, id string, levels []timeLevel) (band, error) {
	answer, ok := resp.Answers[id]
	if !ok {
		return band{}, fmt.Errorf("%s: %w", id, errMissingAnswer)
	}
	probs, err := distribution(answer, len(levels))
	if err != nil {
		return band{}, fmt.Errorf("%s: %w", id, err)
	}
	return band{
		expected: expectedMinutes(probs, levels),
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

func expectedMinutes(probs []float64, levels []timeLevel) float64 {
	sum := 0.0
	for i, p := range probs {
		sum += p * levels[i].representative().Minutes()
	}
	return sum
}

// quantile returns the representative minutes of the level where cumulative probability reaches q.
func quantile(probs []float64, levels []timeLevel, q float64) float64 {
	cumulative := 0.0
	for i, p := range probs {
		cumulative += p
		if cumulative >= q-quantileTolerance {
			return levels[i].representative().Minutes()
		}
	}
	return levels[len(levels)-1].representative().Minutes()
}
