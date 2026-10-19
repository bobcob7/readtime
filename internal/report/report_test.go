package report

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bobcob7/readtime/internal/estimate"
)

func sampleReport() estimate.Report {
	return estimate.Report{
		Words:     476,
		Documents: 1,
		Evaluated: 1,
		Chunks:    1,
		Model:     "jev-test",
		Results: []estimate.Result{{
			Audience:       estimate.Audience{ID: "adult", Name: "General adult", WordsPerMinute: 238},
			Read:           2 * time.Minute,
			Understand:     estimate.Range{Expected: 3 * time.Minute, Low: 2 * time.Minute, High: 4 * time.Minute},
			Grok:           estimate.Range{Expected: 9 * time.Minute, Low: 5 * time.Minute, High: 20 * time.Minute},
			GrokLikelihood: 0.87,
		}},
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "sub-minute shows seconds", in: 42 * time.Second, want: "42s"},
		{name: "short minutes show seconds", in: 3*time.Minute + 20*time.Second, want: "3m 20s"},
		{name: "whole minutes drop seconds", in: 4 * time.Minute, want: "4m"},
		{name: "long minutes round to minute", in: 25*time.Minute + 40*time.Second, want: "26m"},
		{name: "just under an hour rounds up to hours", in: 59*time.Minute + 50*time.Second, want: "1h"},
		{name: "hours show minutes", in: 2*time.Hour + 5*time.Minute, want: "2h 5m"},
		{name: "working days past 8h", in: 20 * time.Hour, want: "2.5d"},
		{name: "whole working days", in: 16 * time.Hour, want: "2d"},
		{name: "many working days round to whole", in: 99 * time.Hour, want: "12d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatDuration(tt.in))
		})
	}
}

func TestTableIncludesAudienceRow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, Table(&buf, sampleReport()))
	assert.Contains(t, buf.String(), "General adult  238  2m    3m (2m-4m)  9m (5m-20m)  87%")
}

func TestJSONReportsDurationsInSeconds(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, JSON(&buf, sampleReport()))
	var got jsonReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got.Audiences, 1)
	assert.Equal(t, jsonRange{ExpectedSeconds: 540, LowSeconds: 300, HighSeconds: 1200}, got.Audiences[0].Grok)
}

func TestTableListsFollowedAndSkippedLinks(t *testing.T) {
	t.Parallel()
	rep := sampleReport()
	rep.Links = []estimate.LinkResult{
		{Target: "docs/setup.md", Words: 300, Relation: "prerequisite", Engagement: map[string]string{"adult": "skim"}, Added: map[string]estimate.Range{"adult": {Expected: 90 * time.Second}}},
		{Target: "https://example.com", External: true, Skipped: "external links disabled (pass -external)"},
	}
	var buf bytes.Buffer
	require.NoError(t, Table(&buf, rep))
	assert.Contains(t, buf.String(), "docs/setup.md  prerequisite  300    +1m 30s skim")
	assert.Contains(t, buf.String(), "skipped https://example.com: external links disabled (pass -external)")
}

func TestTableOmitsLinkSectionWithoutLinks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, Table(&buf, sampleReport()))
	assert.NotContains(t, buf.String(), "LINK")
}

func TestTableSummarisesDocumentsAndChunksProcessed(t *testing.T) {
	t.Parallel()
	rep := sampleReport()
	rep.Documents, rep.Evaluated, rep.Chunks = 3, 2, 5
	var buf bytes.Buffer
	require.NoError(t, Table(&buf, rep))
	assert.Contains(t, buf.String(), "476 words; processed 3 documents, 2 assessed by Jev in 5 chunks; model jev-test.")
}

func TestTableUsesSingularForOneDocumentAndChunk(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	require.NoError(t, Table(&buf, sampleReport()))
	assert.Contains(t, buf.String(), "processed 1 document, 1 assessed by Jev in 1 chunk;")
}

func TestJSONIncludesDocumentAndChunkCounts(t *testing.T) {
	t.Parallel()
	rep := sampleReport()
	rep.Documents, rep.Chunks = 2, 4
	var buf bytes.Buffer
	require.NoError(t, JSON(&buf, rep))
	var got jsonReport
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, [2]int{2, 4}, [2]int{got.Documents, got.Chunks})
}
