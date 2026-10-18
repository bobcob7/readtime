package estimate

import (
	"fmt"
	"maps"
	"math"
	"time"

	"github.com/bobcob7/readtime/internal/jev"
)

// All text sent to Jev lives in this file: rubric levels, question wording and link questions.

const (
	kindUnderstand = "understand"
	kindGrok       = "grok"
	kindFeasible   = "feasible"
)

// timeLevel is a rubric level covering a span of focused working time (8h day, 40h week).
type timeLevel struct {
	description string
	low, high   time.Duration
}

// representative is the geometric midpoint of the span, or half the upper bound for an open-ended first level.
func (l timeLevel) representative() time.Duration {
	if l.low == 0 {
		return l.high / 2
	}
	return time.Duration(math.Sqrt(float64(l.low) * float64(l.high)))
}

const (
	workDay  = 8 * time.Hour
	workWeek = 5 * workDay
)

// understandLevels span the time, including reading, to understand what a passage says.
func understandLevels() []timeLevel {
	return []timeLevel{
		{"Under 1 minute", 0, time.Minute},
		{"1 to 3 minutes", time.Minute, 3 * time.Minute},
		{"3 to 10 minutes", 3 * time.Minute, 10 * time.Minute},
		{"10 to 30 minutes", 10 * time.Minute, 30 * time.Minute},
		{"30 minutes to 1 hour", 30 * time.Minute, time.Hour},
		{"1 to 2 hours", time.Hour, 2 * time.Hour},
		{"2 to 4 hours", 2 * time.Hour, 4 * time.Hour},
		{"4 to 8 hours (up to a working day)", 4 * time.Hour, workDay},
		{"1 to 3 working days", workDay, 3 * workDay},
		{"More than 3 working days", 3 * workDay, 2 * workWeek},
	}
}

// grokLevels span the total time, including understanding, to internalise a passage's ideas.
func grokLevels() []timeLevel {
	return []timeLevel{
		{"Under 5 minutes", 0, 5 * time.Minute},
		{"5 to 15 minutes", 5 * time.Minute, 15 * time.Minute},
		{"15 to 30 minutes", 15 * time.Minute, 30 * time.Minute},
		{"30 minutes to 1 hour", 30 * time.Minute, time.Hour},
		{"1 to 3 hours", time.Hour, 3 * time.Hour},
		{"3 to 8 hours (up to a working day)", 3 * time.Hour, workDay},
		{"1 to 3 working days", workDay, 3 * workDay},
		{"3 working days to 2 weeks", 3 * workDay, 2 * workWeek},
		{"2 weeks to 2 months", 2 * workWeek, 8 * workWeek},
		{"More than 2 months", 8 * workWeek, 24 * workWeek},
	}
}

func descriptions(levels []timeLevel) []string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = l.description
	}
	return out
}

func buildQuestions(audiences []Audience) map[string]jev.Question {
	questions := make(map[string]jev.Question, len(audiences)*3)
	for _, aud := range audiences {
		questions[questionID(aud, kindUnderstand)] = jev.Score(
			fmt.Sprintf("The reader is %s. In focused working time, including reading, how long would this reader need to understand what this passage says?", aud.Description),
			descriptions(understandLevels())...,
		)
		questions[questionID(aud, kindGrok)] = jev.Score(
			fmt.Sprintf("The reader is %s. In focused working time, including reading and understanding it, how long would this reader need to grok this passage: internalise the ideas well enough to explain them, apply them and reason about their implications?", aud.Description),
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

// questionsFor returns a builder giving each chunk the base questions plus link questions for the links it contains.
func questionsFor(audiences []Audience, docs []linkedDoc, fullText string) func(chunk) map[string]jev.Question {
	base := buildQuestions(audiences)
	return func(c chunk) map[string]jev.Question {
		questions := maps.Clone(base)
		for i, doc := range docs {
			if doc.skipped != nil || !linkInChunk(doc.link, c, fullText) {
				continue
			}
			for _, aud := range audiences {
				questions[linkQuestionID(i, aud)] = jev.Noul(
					fmt.Sprintf("The reader is %s. The text links to %q (%s). Would this reader need to read the linked content to understand the text?", aud.Description, doc.link.Text, doc.link.Target),
					"The reader must read the linked content; the text depends on it",
					"The link is optional background or reference for this reader",
				)
			}
		}
		return questions
	}
}

func questionID(aud Audience, kind string) string {
	return aud.ID + "_" + kind
}

func linkQuestionID(linkIndex int, aud Audience) string {
	return fmt.Sprintf("link%d_%s_needed", linkIndex, aud.ID)
}
