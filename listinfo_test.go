package main

import (
	"github.com/wansing/ulist/mailutil"
	"testing"
)

func TestBounceAddress(t *testing.T) {

	tests := []struct {
		input    ListInfo
		expected string
	}{
		{ListInfo{mailutil.Addr{"", `foo`, `example.com`}}, `foo+bounces@example.com`},
		{ListInfo{mailutil.Addr{"", `foo.bar`, `example.com`}}, `foo.bar+bounces@example.com`},         // one dot is okay
		{ListInfo{mailutil.Addr{"", `foo..bar`, `example.com`}}, `"foo..bar+bounces"@example.com`},     // local-parts with consecutive dots must be quoted
		{ListInfo{mailutil.Addr{"", `foo bar`, `example.com`}}, `"foo bar+bounces"@example.com`},       // some characters are only allowed in quotes
		{ListInfo{mailutil.Addr{"", `foo@bar`, `example.com`}}, `"foo@bar+bounces"@example.com`},       // some characters are only allowed in quotes
		{ListInfo{mailutil.Addr{"", `"foo@bar"`, `example.com`}}, `"\"foo@bar\"+bounces"@example.com`}, // double quotes must be escaped
	}

	for _, test := range tests {
		if result := test.input.BounceAddress(); result != test.expected {
			t.Errorf("got %s, want %s", result, test.expected)
		}
	}
}

func TestPrefixSubject(t *testing.T) {

	list := &List{}
	list.Display = "List"

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
