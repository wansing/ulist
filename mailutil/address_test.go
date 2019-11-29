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

func TestExtractAddress(t *testing.T) {

	tests := []struct {
		input    string
		expected string
	}{
		{`alice@example.com`, `alice@example.com`},
		{`ALICE@EXAMPLE.COM`, `alice@example.com`},
		{`Alice <alice@example.com>`, `alice@example.com`},
		{`ALICE <ALICE@EXAMPLE.COM>`, `alice@example.com`},
		{`"Alice" <alice@example.com>`, `alice@example.com`},
		{`"ALICE" <ALICE@EXAMPLE.COM>`, `alice@example.com`},
	}

	for _, test := range tests {
		if output, err := ExtractAddress(test.input); output != test.expected || err != nil {
			t.Errorf("got %s %v, want %s", output, err, test.expected)
		}
	}
}

func TestExtractAddressEmpty(t *testing.T) {

	if _, err := ExtractAddress(""); err.Error() != ErrInvalidAddress.Error() {
		t.Errorf("got %v, want %v", err, ErrInvalidAddress)
	}
}
