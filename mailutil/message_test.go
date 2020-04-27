package mailutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestMessage(t *testing.T) {

	input := `Received: from example.net by example.com
From: "Alice" <alice@example.com>
Subject: Hi
To: bob@example.com

Here's the message body.`
	want := strings.ReplaceAll(input, "\n", "\r\n")
	got := &bytes.Buffer{}

	message, _ := ReadMessage(strings.NewReader(input))
	message.Save(got)

	if got.String() != want {
		t.Errorf("got %s, want %s", got.String(), want)
	}

	var expectSingleFrom = Addr{
		Display: "Alice",
		Local:   "alice",
		Domain:  "example.com",
	}

	var gotSingleFrom, _ = message.SingleFrom()

	if *gotSingleFrom != expectSingleFrom {
		t.Errorf("got %s, want %s", *gotSingleFrom, expectSingleFrom)
	}
}
