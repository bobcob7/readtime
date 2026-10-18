package estimate

import (
	"fmt"
	"maps"

	"github.com/bobcob7/readtime/internal/jev"
)

// All text sent to Jev lives in this file: rubric levels, question wording and link questions.

const (
	kindUnderstand = "understand"
	kindGrok       = "grok"
	kindFeasible   = "feasible"
)

type level struct {
	description string
	multiplier  float64
}

// comprehensionLevels scale read time into time to understand what the text says.
func comprehensionLevels() []level {
	return []level{
		{"Understood fully on a single read with no extra effort", 1.0},
		{"Needs occasional pauses or re-reading of a few sentences", 1.4},
		{"Needs slow, careful reading and re-reading of several passages", 2.0},
		{"Needs looking up terms or background information while reading", 3.0},
		{"Needs substantial background study before the text makes sense", 5.0},
	}
}

// grokLevels scale understand time into time to internalise the ideas well enough to explain and apply them.
func grokLevels() []level {
	return []level{
		{"Ideas are simple; once understood they are fully internalised", 1.2},
		{"Needs brief reflection to connect the ideas together", 2.0},
		{"Needs worked examples or a second pass to be able to apply the ideas", 3.5},
		{"Needs deliberate practice or experimentation to internalise", 6.0},
		{"Needs extended study and hands-on work over multiple sessions", 12.0},
	}
}

func descriptions(levels []level) []string {
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

// chunkQuestions adds, to each chunk's base questions, one question per audience for every link that chunk contains.
func chunkQuestions(audiences []Audience, chunkCount int, docs []linkedDoc) []map[string]jev.Question {
	base := buildQuestions(audiences)
	out := make([]map[string]jev.Question, chunkCount)
	for i := range out {
		out[i] = maps.Clone(base)
	}
	for i, doc := range docs {
		if doc.skipped != nil {
			continue
		}
		for _, aud := range audiences {
			out[doc.chunk][linkQuestionID(i, aud)] = jev.Noul(
				fmt.Sprintf("The reader is %s. The text links to %q (%s). Would this reader need to read the linked content to understand the text?", aud.Description, doc.link.Text, doc.link.Target),
				"The reader must read the linked content; the text depends on it",
				"The link is optional background or reference for this reader",
			)
		}
	}
	return out
}

func questionID(aud Audience, kind string) string {
	return aud.ID + "_" + kind
}

func linkQuestionID(linkIndex int, aud Audience) string {
	return fmt.Sprintf("link%d_%s_needed", linkIndex, aud.ID)
}
