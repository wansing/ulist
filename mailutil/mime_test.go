package mailutil

import (
	"testing"
)

func TestTryMimeDecode(t *testing.T) {

	tests := []struct {
		input  string
		expect string
	}{
		{"¡Hola, señor!", "¡Hola, señor!"},
		{"=?UTF-8?b?wqFIb2xhLCBzZcOxb3Ih?=", "¡Hola, señor!"},
		{"=?     ?b?wqFIb2xhLCBzZcOxb3Ih?=", "¡Hola, señor!"},
	}

	for _, test := range tests {
		if got := TryMimeDecode(test.input); got != test.expect {
			t.Errorf("got %s, want %s", got, test.expect)
		}
	}
}
