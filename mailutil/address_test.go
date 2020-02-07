package mailutil

import "testing"

func TestDecode(t *testing.T) {

	tests := []struct {
		input   string
		name    string
		address string
		errmsg  string
	}{
		{"Name <address@example.com>", "Name", "address@example.com", ""},
		{"Ääää <ööööööö@üüüüüüü.com>", "Ääää", "ööööööö@üüüüüüü.com", ""},
		{"=?ISO-8859-15?Q?K=F6hler?= <test@example.com>", "Köhler", "test@example.com", ""},
		{"=?utf-8?q?=C2=A1Hola,_se=C3=B1or!?= <test@example.com>", "¡Hola, señor!", "test@example.com", ""},
		{"=?utf-8?B?c3BhbUBleGFtcGxlLmNvbQ==?=", "", "", "mail: no angle-addr"}, // apparently only used by spammers to obfuscate their email address
	}

	for _, test := range tests {
		var a, err = RobustAddressParser.Parse(test.input)
		var name, address, errmsg string
		if a != nil {
			name, address = a.Name, a.Address
		}
		if err != nil {
			errmsg = err.Error()
		}
		if name != test.name || address != test.address || errmsg != test.errmsg {
			t.Errorf("got %s %s %s, want %s %s %s", name, address, errmsg, test.name, test.address, test.errmsg)
		}
	}
}

func TestEquals(t *testing.T) {

	tests := []struct {
		a *Addr
		b *Addr
		want bool
	}{
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, &Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, true},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, &Addr{                 Local: "ANNA", Domain: "EXAMPLE.COM"}, true},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, &Addr{Display: "Anna", Local: "anna", Domain: "example.net"}, false}, // com != net
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, &Addr{Display: "Anna", Local: "bob",  Domain: "example.com"}, false}, // anna != bob
	}

	for _, test := range tests {

		equals := test.a.Equals(test.b)
		if equals != test.want {
			t.Errorf("got %v, want %v", equals, test.want)
		}

		equals = test.b.Equals(test.a)
		if equals != test.want {
			t.Errorf("got %v, want %v", equals, test.want)
		}
	}
}

func TestParseAddress(t *testing.T) {

	tests := []struct {
		input    string
		expected Addr
	}{
		{`alice@example.com`, Addr{"", "alice", "example.com"}},
		{`ALICE@EXAMPLE.COM`, Addr{"", "alice", "example.com"}},
		{`Alice <alice@example.com>`, Addr{"Alice", "alice", "example.com"}},
		{`ALICE <ALICE@EXAMPLE.COM>`, Addr{"ALICE", "alice", "example.com"}},
		{`"Alice" <alice@example.com>`, Addr{"Alice", "alice", "example.com"}},
		{`"ALICE" <ALICE@EXAMPLE.COM>`, Addr{"ALICE", "alice", "example.com"}},
		{`"Alice at home" <"Alice@Home"@EXAMPLE.COM>`, Addr{"Alice at home", "alice@home", "example.com"}},
	}

	for _, test := range tests {
		if result, err := ParseAddress(test.input); err != nil || *result != test.expected {
			t.Errorf("got %v %v, want %s", result, err, test.expected)
		}
	}
}

func TestParseEmptyAddress(t *testing.T) {
	if _, err := ParseAddress(""); err.Error() != ErrInvalidAddress.Error() {
		t.Errorf("got %v, want %v", err, ErrInvalidAddress)
	}
}

func TestParseAddresses(t *testing.T) {

	tests := []struct {
		input         string
		limit         int
		expectedAddrs []Addr
		expectedErrs  []string
	}{
		{`"Ally" <alice@example.com>, bert@example.net, claire@example.org
			<dan@missing-tld`,
			100,
			[]Addr{Addr{"Ally", "alice", "example.com"}, Addr{"", "bert", "example.net"}, Addr{"", "claire", "example.org"}},
			[]string{`error parsing line "<dan@missing-tld": mail: unclosed angle-addr`},
		},
		{`"Ally" <alice@example.com>, bert@example.net, claire@example.org
			<dan@missing-tld`,
			2,
			[]Addr{Addr{"Ally", "alice", "example.com"}, Addr{"", "bert", "example.net"}, Addr{"", "claire", "example.org"}},
			[]string{`please enter not more than 2 addresses`},
		},
	}

	for _, test := range tests {
		result, errs := ParseAddresses(test.input, test.limit)
		for i, r := range result {
			if *r != test.expectedAddrs[i] {
				t.Errorf("got %v, want %s", r, test.expectedAddrs[i])
			}
		}
		for i, e := range errs {
			if e.Error() != test.expectedErrs[i] {
				t.Errorf("got %v, want %s", e, test.expectedErrs[i])
			}
		}
	}
}

func TestRFC5322AddrSpec(t *testing.T) {

	tests := []struct {
		a *Addr
		want string
	}{
		{&Addr{Local: "anna", Domain: "example.com"}, "anna@example.com"},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, "anna@example.com"},
		{&Addr{Local: "i have spaces", Domain: "example.com"}, `"i have spaces"@example.com`},
		{&Addr{Local: `"i have quotes"`, Domain: "example.com"}, `"\"i have quotes\""@example.com`},
	}

	for _, test := range tests {
		got := test.a.RFC5322AddrSpec()
		if got != test.want {
			t.Errorf("got %v, want %v", got, test.want)
		}
	}
}

func TestRFC5322NameAddr(t *testing.T) {

	tests := []struct {
		a *Addr
		want string
	}{
		{&Addr{Local: "anna", Domain: "example.com"}, "<anna@example.com>"},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, `"Anna" <anna@example.com>`},
		{&Addr{Display: `Anna "Ann"`, Local: "anna", Domain: "example.com"}, `"Anna \"Ann\"" <anna@example.com>`},
	}

	for _, test := range tests {
		got := test.a.RFC5322NameAddr()
		if got != test.want {
			t.Errorf("got %v, want %v", got, test.want)
		}
	}
}

func TestRFC6068URI(t *testing.T) {

	tests := []struct {
		a *Addr
		query string
		want string
	}{
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, "", "<mailto:anna@example.com>"},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, "subject=unsubscribe", "<mailto:anna@example.com?subject=unsubscribe>"},
	}

	for _, test := range tests {
		got := test.a.RFC6068URI(test.query)
		if got != test.want {
			t.Errorf("got %v, want %v", got, test.want)
		}
	}
}

func TestDisplayOrLocal(t *testing.T) {

	tests := []struct {
		a *Addr
		want string
	}{
		{&Addr{Local: "anna", Domain: "example.com"}, "anna"},
		{&Addr{Display: "Anna", Local: "anna", Domain: "example.com"}, "Anna"},
		{&Addr{Display: "anna@example.com", Local: "anna", Domain: "example.com"}, "anna"},
	}

	for _, test := range tests {
		got := test.a.DisplayOrLocal()
		if got != test.want {
			t.Errorf("got %v, want %v", got, test.want)
		}
	}
}
