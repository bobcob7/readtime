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
	chunk   int
	words   int
	results []Result
	skipped error
}

// followLinks fetches and estimates every link in text. Fetch failures mark the link skipped;
// Jev failures abort, since they would also break the main estimate.
func (e *Estimator) followLinks(ctx context.Context, text string, chunks []chunk, audiences []Audience) ([]linkedDoc, error) {
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
			docs[i] = linkedDoc{link: link, chunk: chunkFor(chunks, link)}
			content, err := e.fetcher.Fetch(ctx, link)
			if err != nil {
				e.logger.WarnContext(ctx, "skipping link", "target", link.Target, "error", err)
				docs[i].skipped = err
				return nil
			}
			sub := splitChunks(content, e.chunkWords)
			if len(sub) == 0 {
				docs[i].skipped = errEmptyInput
				return nil
			}
			results, err := e.estimateLinked(ctx, sub, audiences)
			if err != nil {
				return fmt.Errorf("estimating link %s: %w", link.Target, err)
			}
			docs[i].words, docs[i].results = totalWords(sub), results
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return docs, nil
}

// chunkFor finds the chunk where link is used, so Jev judges its necessity in context.
func chunkFor(chunks []chunk, link links.Link) int {
	needles := []string{"[" + link.Text + "]", link.Target}
	for _, needle := range needles {
		for i, c := range chunks {
			if strings.Contains(c.text, needle) {
				return i
			}
		}
	}
	return 0
}

// applyLinks folds each linked document's times into results, weighted by how likely each audience needs it.
func applyLinks(results []Result, docs []linkedDoc, responses []jev.Response) ([]LinkResult, error) {
	out := make([]LinkResult, 0, len(docs))
	for i, doc := range docs {
		lr := LinkResult{Text: doc.link.Text, Target: doc.link.Target, External: doc.link.External}
		if doc.skipped != nil {
			lr.Skipped = doc.skipped.Error()
			out = append(out, lr)
			continue
		}
		lr.Words, lr.NeededBy = doc.words, make(map[string]float64, len(results))
		for j := range results {
			id := linkQuestionID(i, results[j].Audience)
			answer, ok := responses[doc.chunk].Answers[id]
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
