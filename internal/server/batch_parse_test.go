package server

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseBatchCandidatesSupportsJSONJSONLAndText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		body       string
		labels     []string
		tokens     []string
		accountIDs []string
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
				wantAccountID := ""
				if index < len(test.accountIDs) {
					wantAccountID = test.accountIDs[index]
				}
				if items[index].Index != index+1 || items[index].Token != test.tokens[index] || items[index].Label != test.labels[index] || items[index].AccountID != wantAccountID {
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

func TestParseBatchCandidatesScopesSamePATByAccountID(t *testing.T) {
	t.Parallel()
	body := []byte(`[
		{"token":"at-same","account_id":"team-one"},
		{"token":"at-same","chatgpt_account_id":"team-two"},
		{"token":"at-same","team_id":"team-three"},
		{"token":"at-same","workspace_id":"team-four"}
	]`)
	items, err := parseBatchCandidates(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"team-one", "team-two", "team-three", "team-four"}
	if len(items) != len(want) {
		t.Fatalf("items=%d want=%d", len(items), len(want))
	}
	for index, accountID := range want {
		if items[index].Token != "at-same" || items[index].AccountID != accountID {
			t.Fatalf("item[%d]=%#v", index, items[index])
		}
	}
}

func TestParseBatchCandidatesInheritsWrapperAccountID(t *testing.T) {
	t.Parallel()
	items, err := parseBatchCandidates([]byte(`{"account_id":"team-wrapper","tokens":["at-one","at-two"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].AccountID != "team-wrapper" || items[1].AccountID != "team-wrapper" {
		t.Fatalf("wrapper account ID was not inherited: %#v", items)
	}
}
