package estimate

import "strings"

type chunk struct {
	text  string
	words int
}

// splitChunks groups paragraphs into chunks of at most maxWords, splitting oversized paragraphs by word.
func splitChunks(text string, maxWords int) []chunk {
	var chunks []chunk
	var current []string
	currentWords := 0
	flush := func() {
		if currentWords == 0 {
			return
		}
		chunks = append(chunks, chunk{text: strings.Join(current, "\n\n"), words: currentWords})
		current, currentWords = nil, 0
	}
	for _, para := range paragraphs(text) {
		words := strings.Fields(para)
		if len(words) > maxWords {
			flush()
			for start := 0; start < len(words); start += maxWords {
				end := min(start+maxWords, len(words))
				chunks = append(chunks, chunk{text: strings.Join(words[start:end], " "), words: end - start})
			}
			continue
		}
		if currentWords+len(words) > maxWords {
			flush()
		}
		current = append(current, para)
		currentWords += len(words)
	}
	flush()
	return chunks
}

func paragraphs(text string) []string {
	var out []string
	var lines []string
	for line := range strings.Lines(strings.ReplaceAll(text, "\r\n", "\n")) {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, "\n"))
			continue
		}
		if len(lines) > 0 {
			out = append(out, strings.Join(lines, "\n"))
			lines = nil
		}
	}
	if len(lines) > 0 {
		out = append(out, strings.Join(lines, "\n"))
	}
	return out
}
