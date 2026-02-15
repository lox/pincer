package agent

import (
	"testing"
)

func TestSearchArgsValidation(t *testing.T) {
	client := NewKagiClient("test-key")
	_, err := client.Search(t.Context(), SearchArgs{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestSummarizeArgsValidation(t *testing.T) {
	client := NewKagiClient("test-key")
	_, err := client.Summarize(t.Context(), SummarizeArgs{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty url")
	}
}
