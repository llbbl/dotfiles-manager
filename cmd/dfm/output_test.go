package main

import (
	"bytes"
	"testing"
)

func TestWriteJSON_PrettyEncodesWithTwoSpaceIndent(t *testing.T) {
	type payload struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	var buf bytes.Buffer
	if err := writeJSON(&buf, payload{ID: 7, Name: "alpha"}); err != nil {
		t.Fatalf("writeJSON returned error: %v", err)
	}

	want := "{\n  \"id\": 7,\n  \"name\": \"alpha\"\n}\n"
	if got := buf.String(); got != want {
		t.Fatalf("writeJSON output mismatch\n got: %q\nwant: %q", got, want)
	}
}
