package server

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseBatchCandidatesSupportsJSONJSONLAndText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		labels []string
		tokens []string
	}{
		{
			name:   "json array",
			body:   `[{"token":"at-one","label":"first"},{"codex_access_token":"at-two"}]`,
			labels: []string{"first", ""},
			tokens: []string{"at-one", "at-two"},
		},
		{
			name:   "json wrapper",
			body:   `{"items":["at-one",{"access_token":"at-two","name":"second"}]}`,
			labels: []string{"", "second"},
			tokens: []string{"at-one", "at-two"},
		},
		{
			name:   "jsonl",
			body:   "{\"token\":\"at-one\",\"label\":\"first\"}\n{\"agent_identity\":\"header.payload.signature\"}",
			labels: []string{"first", ""},
			tokens: []string{"at-one", "header.payload.signature"},
		},
		{
			name:   "text",
			body:   "# comment\nat-one\n\nat-two\n",
			labels: []string{"", ""},
			tokens: []string{"at-one", "at-two"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items, err := parseBatchCandidates([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != len(test.tokens) {
				t.Fatalf("items=%d want=%d", len(items), len(test.tokens))
			}
			for index := range items {
				if items[index].Index != index+1 || items[index].Token != test.tokens[index] || items[index].Label != test.labels[index] {
					t.Fatalf("item[%d]=%#v", index, items[index])
				}
			}
		})
	}
}

func TestParseBatchCandidatesRejectsOversizedItemCount(t *testing.T) {
	t.Parallel()
	lines := make([]string, maxBatchImportItems+1)
	for index := range lines {
		lines[index] = fmt.Sprintf("at-%03d", index)
	}
	if _, err := parseBatchCandidates([]byte(strings.Join(lines, "\n"))); err == nil {
		t.Fatal("oversized batch was accepted")
	}
}

func TestParseBatchCandidatesRejectsMissingTokenField(t *testing.T) {
	t.Parallel()
	if _, err := parseBatchCandidates([]byte(`{"label":"missing"}`)); err == nil {
		t.Fatal("JSON item without a token was accepted")
	}
}
