package estimate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/links"
)

const (
	// studyThreshold is the study probability above which a linked document is sent to Jev itself.
	studyThreshold = 0.05
	glanceMinutes  = 0.5
	lookupMinutes  = 3
)

// linkedDoc is a link from the main text, its fetched content and, if anyone may study it, its understand times.
type linkedDoc struct {
	link    links.Link
	content chunk
	skipped error
	// study holds per-audience understand time in minutes, in Audiences order; nil when not evaluated.
	study  []band
	chunks int
}

// fetchLinks fetches every link in text; failures mark the link skipped rather than aborting.
func (e *Estimator) fetchLinks(ctx context.Context, text string) []linkedDoc {
	if e.maxLinks == 0 {
		return nil
	}
	found := links.Extract(text)
	if e.maxLinks > 0 && len(found) > e.maxLinks {
		e.logger.WarnContext(ctx, "too many links; following only the first ones", "found", len(found), "max", e.maxLinks)
		found = found[:e.maxLinks]
	}
	docs := make([]linkedDoc, len(found))
	var g errgroup.Group
	g.SetLimit(e.concurrency)
	for i, link := range found {
		g.Go(func() error {
			docs[i] = linkedDoc{link: link}
			content, err := e.fetcher.Fetch(ctx, link)
			if err != nil {
				e.logger.WarnContext(ctx, "skipping link", "target", link.Target, "error", err)
				docs[i].skipped = err
				return nil
			}
			docs[i].content = wholeText(content)
			if docs[i].content.words == 0 {
				docs[i].skipped = errEmptyInput
			}
			return nil
		})
	}
	_ = g.Wait()
	return docs
}

// studyLinks asks Jev how long linked documents take to understand, but only those some audience may study.
func (e *Estimator) studyLinks(ctx context.Context, audiences []Audience, docs []linkedDoc, evals []evaluation) error {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(e.concurrency)
	for i := range docs {
		if docs[i].skipped != nil || !mayStudy(i, audiences, evals) {
			continue
		}
		g.Go(func() error {
			linked, err := e.evaluate(ctx, docs[i].content, func(chunk) map[string]jev.Question { return understandQuestions(audiences) })
			if err != nil {
				return fmt.Errorf("estimating link %s: %w", docs[i].link.Target, err)
			}
			study := make([]band, len(audiences))
			for j, aud := range audiences {
				if study[j], err = understandBand(aud, linked); err != nil {
					return fmt.Errorf("estimating link %s: %w", docs[i].link.Target, err)
				}
			}
			docs[i].study, docs[i].chunks = study, len(linked)
			return nil
		})
	}
	return g.Wait()
}

func mayStudy(linkIndex int, audiences []Audience, evals []evaluation) bool {
	for _, aud := range audiences {
		if answer, ok := findAnswer(evals, linkEngagementID(linkIndex, aud)); ok && answer.Probabilities[engageStudy] >= studyThreshold {
			return true
		}
	}
	return false
}

// engagementMinutes is the time a reader spends on a linked document of the given read time in each mode.
func engagementMinutes(mode string, read float64, study band) band {
	switch mode {
	case engageGlance:
		return flat(min(read, glanceMinutes))
	case engageLookup:
		return flat(min(read, lookupMinutes))
	case engageSkim:
		return flat(read * skimFraction)
	case engageRead:
		return flat(read)
	case engageStudy:
		return study.atLeast(flat(read))
	default:
		return band{}
	}
}

// engagementOrder lists modes from least to most effort.
func engagementOrder() []string {
	return []string{engageSkip, engageGlance, engageLookup, engageSkim, engageRead, engageStudy}
}

// capMode limits mode to ceiling, e.g. a reader skimming the text only skims a link they would otherwise study.
func capMode(mode, ceiling string) string {
	order := engagementOrder()
	if slices.Index(order, mode) > slices.Index(order, ceiling) {
		return ceiling
	}
	return mode
}

// linkTimes is the probability-weighted time a link adds when skimming, reading and understanding the text.
func linkTimes(probs map[string]float64, read float64, study band) (band, band, band) {
	var skim, readThrough, understand band
	for _, mode := range engagementOrder() {
		p := probs[mode]
		skim = skim.add(engagementMinutes(capMode(mode, engageSkim), read, study).scale(p))
		readThrough = readThrough.add(engagementMinutes(capMode(mode, engageRead), read, study).scale(p))
		understand = understand.add(engagementMinutes(mode, read, study).scale(p))
	}
	return skim, readThrough, understand
}

// linkInChunk reports whether c mentions link, so Jev judges it in context.
// Links that cannot be located anywhere are asked about in the opening chunk.
func linkInChunk(link links.Link, c chunk, fullText string) bool {
	needles := []string{"[" + link.Text + "]", link.Target}
	located := false
	for _, needle := range needles {
		if strings.Contains(c.text, needle) {
			return true
		}
		located = located || strings.Contains(fullText, needle)
	}
	return c.first && !located
}

func findAnswer(evals []evaluation, id string) (jev.Answer, bool) {
	for _, ev := range evals {
		if answer, ok := ev.resp.Answers[id]; ok {
			return answer, true
		}
	}
	return jev.Answer{}, false
}

// applyLinks adds each link's probability-weighted engagement time to every audience's times.
func applyLinks(results []Result, docs []linkedDoc, evals []evaluation) ([]LinkResult, error) {
	out := make([]LinkResult, 0, len(docs))
	for i, doc := range docs {
		lr := LinkResult{Text: doc.link.Text, Target: doc.link.Target, External: doc.link.External}
		if doc.skipped != nil {
			lr.Skipped = doc.skipped.Error()
			out = append(out, lr)
			continue
		}
		relation, ok := findAnswer(evals, linkRelationID(i))
		if !ok {
			return nil, fmt.Errorf("%s: %w", linkRelationID(i), errMissingAnswer)
		}
		lr.Words, lr.Relation, lr.Evaluated, lr.Chunks = doc.content.words, relation.Choice, doc.study != nil, doc.chunks
		lr.Engagement, lr.Added = make(map[string]string, len(results)), make(map[string]Range, len(results))
		for j := range results {
			aud := results[j].Audience
			answer, ok := findAnswer(evals, linkEngagementID(i, aud))
			if !ok {
				return nil, fmt.Errorf("%s: %w", linkEngagementID(i, aud), errMissingAnswer)
			}
			read := float64(doc.content.words) / aud.WordsPerMinute
			study := flat(read)
			if doc.study != nil {
				study = doc.study[j]
			}
			skim, readThrough, added := linkTimes(answer.Probabilities, read, study)
			lr.Engagement[aud.ID], lr.Added[aud.ID] = answer.Choice, added.toRange()
			results[j].Skim += minutes(skim.expected)
			results[j].Read += minutes(readThrough.expected)
			results[j].Understand = results[j].Understand.plus(added.toRange())
			results[j].Grok = results[j].Grok.plus(added.toRange())
		}
		out = append(out, lr)
	}
	return out, nil
}
