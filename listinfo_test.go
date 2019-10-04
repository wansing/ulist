package main

import "testing"

func TestBounceAddress(t *testing.T) {

	tests := []struct {
		input    ListInfo
		expected string
	}{
		{ListInfo{Address: `foo@example.com`}, `foo+bounces@example.com`},
		{ListInfo{Address: `"foo@bar"@example.com`}, `"foo@bar"+bounces@example.com`},
	}

	for _, test := range tests {
		if result := test.input.BounceAddress(); result != test.expected {
			t.Errorf("got %s, want %s", result, test.expected)
		}
	}
}

func TestIsBounceAddress(t *testing.T) {

	tests := []struct {
		input   string
		cleaned string
		is      bool
	}{
		{``, ``, false},
		{`foo`, `foo`, false},
		{`foo+bounces`, `foo+bounces`, false},
		{`foo@example.com`, `foo@example.com`, false},
		{`foo+bounces@example.com`, `foo@example.com`, true},
	}

	for _, test := range tests {
		if cleaned, is := IsBounceAddress(test.input); cleaned != test.cleaned || is != test.is {
			t.Errorf("got %s %t, want %s %t", cleaned, is, test.cleaned, test.is)
		}
	}
}

func TestPrefixSubject(t *testing.T) {

	list := &List{}
	list.Name = "List"

	tests := []struct {
		input    string
		expected string
	}{
		{"", "[List] "},
		{"Foo", "[List] Foo"},
		{"Re: Foo", "[List] Re: Foo"},
		{"[List] Foo", "[List] Foo"},
		{"Re: FW: [List] Foo", "Re: FW: [List] Foo"},
		{"[", "[List] ["},
		{"Re: [", "[List] Re: ["},
		{"[ <- Bracket", "[List] [ <- Bracket"},
		{"Re: [ <- Bracket", "[List] Re: [ <- Bracket"},
		{"[Bar] [List]", "[List] [Bar] [List]"},
		{"Re: [Bar] [List]", "[List] Re: [Bar] [List]"},
	}

	for _, test := range tests {
		if result := list.PrefixSubject(test.input); result != test.expected {
			t.Errorf("got %s, want %s", result, test.expected)
		}
	}
}
