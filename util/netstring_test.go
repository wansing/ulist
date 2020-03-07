package util

import "testing"

func TestNetstring(t *testing.T) {

	tests := []struct {
		str    string
		netstr string
	}{
		{"", "0:,"},
		{"hello world!", "12:hello world!,"},
	}

	for _, test := range tests {
		if got := EncodeNetstring(test.str); string(got) != test.netstr {
			t.Fatalf("got %s, want %s", got, test.netstr)
		}
		if got, err := DecodeNetstring([]byte(test.netstr)); got != test.str || err != nil {
			t.Fatalf("got %s, %v, want %s, %v", got, err, test.str, nil)
		}
	}
}
