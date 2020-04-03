package listdb

import (
	"regexp"
	"testing"

	"github.com/wansing/ulist/mailutil"
)

var messageIdPattern = regexp.MustCompile("[0-9a-z-_]{32}") // RFC5322 Message-Id compliant

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

// we can't test the uniqueness across test runs here
func TestNewMessageId(t *testing.T) {

	var li = &ListInfo{
		mailutil.Addr{
			Local:  "list",
			Domain: "example.com",
		},
	}

	var gotPrev string
	var want = "<message-id@example.com>"

	for i := 0; i < 1000; i++ {

		var got = li.NewMessageId()

		if got == gotPrev {
			t.Errorf("got previous, want different")
		}
		gotPrev = got

		if got = messageIdPattern.ReplaceAllString(got, "message-id"); got != want {
			t.Errorf("got %s, want %s", got, want)
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
		{"=?UTF-8?Q?=5BList=5D_Hello?=", "[List] Hello"},
	}

	for _, test := range tests {
		if result := list.PrefixSubject(test.input); result != test.expected {
			t.Errorf("got %s, want %s", result, test.expected)
		}
	}
}
