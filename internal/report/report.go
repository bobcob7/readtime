// Package report renders estimates as a terminal table or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bobcob7/readtime/internal/estimate"
)

const workDay = 8 * time.Hour

// Table writes a human-readable table of r to w.
func Table(w io.Writer, r estimate.Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AUDIENCE\tWPM\tREAD\tUNDERSTAND\tGROK\tCAN GROK")
	for _, res := range r.Results {
		fmt.Fprintf(tw, "%s\t%.0f\t%s\t%s\t%s\t%.0f%%\n",
			res.Audience.Name, res.Audience.WordsPerMinute, formatDuration(res.Read),
			formatRange(res.Understand), formatRange(res.Grok), res.GrokLikelihood*100)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}
	if _, err := fmt.Fprintf(w, "\n%d words, %d chunk(s), model %s. Read uses words per minute; understand and grok are Jev's\nestimates of cumulative focused working time (d = 8h day). Ranges are 10th-90th percentile.\n", r.Words, r.Chunks, r.Model); err != nil {
		return fmt.Errorf("writing summary: %w", err)
	}
	if len(r.Links) == 0 {
		return nil
	}
	return linkTable(w, r)
}

func linkTable(w io.Writer, r estimate.Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "\nLINK\tWORDS")
	for _, res := range r.Results {
		fmt.Fprintf(tw, "\t%s", strings.ToUpper(res.Audience.ID))
	}
	fmt.Fprintln(tw)
	var skipped []estimate.LinkResult
	for _, link := range r.Links {
		if link.Skipped != "" {
			skipped = append(skipped, link)
			continue
		}
		fmt.Fprintf(tw, "%s\t%d", link.Target, link.Words)
		for _, res := range r.Results {
			fmt.Fprintf(tw, "\t%.0f%%", link.NeededBy[res.Audience.ID]*100)
		}
		fmt.Fprintln(tw)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("writing link table: %w", err)
	}
	for _, link := range skipped {
		fmt.Fprintf(w, "skipped %s: %s\n", link.Target, link.Skipped)
	}
	if _, err := fmt.Fprintln(w, "\nPercentages are the chance each audience must read the link; understand and grok times include linked content weighted by them."); err != nil {
		return fmt.Errorf("writing link summary: %w", err)
	}
	return nil
}

type jsonRange struct {
	ExpectedSeconds float64 `json:"expected_seconds"`
	LowSeconds      float64 `json:"low_seconds"`
	HighSeconds     float64 `json:"high_seconds"`
}

type jsonResult struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	WordsPerMinute float64   `json:"words_per_minute"`
	ReadSeconds    float64   `json:"read_seconds"`
	Understand     jsonRange `json:"understand"`
	Grok           jsonRange `json:"grok"`
	GrokLikelihood float64   `json:"grok_likelihood"`
}

type jsonLink struct {
	Text     string             `json:"text"`
	Target   string             `json:"target"`
	External bool               `json:"external"`
	Words    int                `json:"words,omitempty"`
	NeededBy map[string]float64 `json:"needed_by,omitempty"`
	Skipped  string             `json:"skipped,omitempty"`
}

type jsonReport struct {
	Words     int          `json:"words"`
	Chunks    int          `json:"chunks"`
	Model     string       `json:"model"`
	Audiences []jsonResult `json:"audiences"`
	Links     []jsonLink   `json:"links"`
}

// JSON writes r to w as indented JSON with durations in seconds.
func JSON(w io.Writer, r estimate.Report) error {
	out := jsonReport{Words: r.Words, Chunks: r.Chunks, Model: r.Model, Audiences: make([]jsonResult, 0, len(r.Results)), Links: make([]jsonLink, 0, len(r.Links))}
	for _, link := range r.Links {
		out.Links = append(out.Links, jsonLink(link))
	}
	for _, res := range r.Results {
		out.Audiences = append(out.Audiences, jsonResult{
			ID:             res.Audience.ID,
			Name:           res.Audience.Name,
			WordsPerMinute: res.Audience.WordsPerMinute,
			ReadSeconds:    seconds(res.Read),
			Understand:     toJSONRange(res.Understand),
			Grok:           toJSONRange(res.Grok),
			GrokLikelihood: math.Round(res.GrokLikelihood*1000) / 1000,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding json: %w", err)
	}
	return nil
}

func toJSONRange(r estimate.Range) jsonRange {
	return jsonRange{ExpectedSeconds: seconds(r.Expected), LowSeconds: seconds(r.Low), HighSeconds: seconds(r.High)}
}

func seconds(d time.Duration) float64 {
	return math.Round(d.Seconds()*10) / 10
}

func formatRange(r estimate.Range) string {
	if formatDuration(r.Low) == formatDuration(r.High) {
		return formatDuration(r.Expected)
	}
	return fmt.Sprintf("%s (%s-%s)", formatDuration(r.Expected), formatDuration(r.Low), formatDuration(r.High))
}

func formatDuration(d time.Duration) string {
	if d >= workDay {
		days := math.Round(d.Hours()/workDay.Hours()*10) / 10
		if days >= 10 {
			days = math.Round(days)
		}
		return strconv.FormatFloat(days, 'f', -1, 64) + "d"
	}
	if rounded := d.Round(time.Minute); rounded >= time.Hour {
		h, m := int(rounded.Hours()), int(rounded.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if d >= 10*time.Minute {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m, s := int(d.Minutes()), int(d.Seconds())%60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}
