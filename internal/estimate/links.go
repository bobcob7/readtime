package estimate

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/links"
)

// linkedDoc is a link from the main text together with its own per-audience estimate.
type linkedDoc struct {
	link    links.Link
	words   int
	chunks  int
	results []Result
	skipped error
}

// followLinks fetches and estimates every link in text. Fetch failures mark the link skipped;
// Jev failures abort, since they would also break the main estimate.
func (e *Estimator) followLinks(ctx context.Context, text string, audiences []Audience) ([]linkedDoc, error) {
	if e.maxLinks <= 0 {
		return nil, nil
	}
	found := links.Extract(text)
	if len(found) > e.maxLinks {
		e.logger.WarnContext(ctx, "too many links; following only the first ones", "found", len(found), "max", e.maxLinks)
		found = found[:e.maxLinks]
	}
	docs := make([]linkedDoc, len(found))
	g, ctx := errgroup.WithContext(ctx)
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
			doc := wholeText(content)
			if doc.words == 0 {
				docs[i].skipped = errEmptyInput
				return nil
			}
			results, chunks, err := e.estimateLinked(ctx, doc, content, audiences)
			if err != nil {
				return fmt.Errorf("estimating link %s: %w", link.Target, err)
			}
			docs[i].words, docs[i].chunks, docs[i].results = doc.words, chunks, results
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return docs, nil
}

// estimateLinked estimates a linked document on its own, without following its links.
// It also returns how many chunks the document was split into.
func (e *Estimator) estimateLinked(ctx context.Context, doc chunk, text string, audiences []Audience) ([]Result, int, error) {
	evals, err := e.evaluate(ctx, doc, questionsFor(audiences, nil, text))
	if err != nil {
		return nil, 0, err
	}
	results, err := aggregateAll(audiences, evals)
	return results, len(evals), err
}

// linkInChunk reports whether c mentions link, so Jev judges its necessity in context.
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

// applyLinks folds each linked document's times into results, weighted by how likely each audience needs it.
func applyLinks(results []Result, docs []linkedDoc, evals []evaluation) ([]LinkResult, error) {
	out := make([]LinkResult, 0, len(docs))
	for i, doc := range docs {
		lr := LinkResult{Text: doc.link.Text, Target: doc.link.Target, External: doc.link.External}
		if doc.skipped != nil {
			lr.Skipped = doc.skipped.Error()
			out = append(out, lr)
			continue
		}
		lr.Words, lr.Chunks, lr.NeededBy = doc.words, doc.chunks, make(map[string]float64, len(results))
		for j := range results {
			id := linkQuestionID(i, results[j].Audience)
			answer, ok := findAnswer(evals, id)
			if !ok {
				return nil, fmt.Errorf("%s: %w", id, errMissingAnswer)
			}
			lr.NeededBy[results[j].Audience.ID] = answer.Noul
			results[j].Understand = results[j].Understand.plus(doc.results[j].Understand.scale(answer.Noul))
			results[j].Grok = results[j].Grok.plus(doc.results[j].Grok.scale(answer.Noul))
		}
		out = append(out, lr)
	}
	return out, nil
}
