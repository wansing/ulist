//go:generate go run assets_gen.go

package main

import (
	"database/sql"
	"io"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"
	"github.com/wansing/ulist/mailutil"
)

const testDbPath = "/tmp/ulist-test.sqlite3"

var messageChannel = make(chan string, 100)

func init() {
	mta = ChanMTA(messageChannel)
	SpoolDir = "/tmp/"
	Testmode = true
	WebUrl = "https://lists.example.com"
}

func lmtpTransaction(t *testing.T, envelopeFrom string, envelopeTo []string, data io.Reader) []string {

	backend := &LMTPBackend{}

	session, err := backend.AnonymousLogin(nil)
	if err != nil {
		t.Fatal(err)
	}

	err = session.Mail(envelopeFrom, smtp.MailOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, to := range envelopeTo {
		err := session.Rcpt(to)
		if err != nil {
			t.Fatal(err)
		}
	}

	err = session.Data(data)
	if err != nil {
		t.Fatal(err)
	}

	return nil
}

type testAlerter struct{}

func (testAlerter) Alertf(format string, a ...interface{}) {
	log.Fatalf(format, a...)
}

func (testAlerter) Successf(format string, a ...interface{}) {}

func TestCRUD(t *testing.T) {

	var err error

	_ = os.Remove(testDbPath)

	Db, _ = OpenDatabase("sqlite3", testDbPath)
	if err != nil {
		t.Fatal(err)
	}

	// create list

	if _, err = CreateList("list_a@example.com", "A", "chris@example.com, norah@example.net", testAlerter{}); err != nil {
		t.Fatal(err)
	}

	if _, err = CreateList("list_b@example.com", "B", "otto@example.org", testAlerter{}); err != nil {
		t.Fatal(err)
	}

	// load list

	listAddr, _ := mailutil.ParseAddress("list_a@example.com")

	listA, err := GetList(listAddr)
	if err != nil {
		t.Fatal(err)
	}

	// add member

	claire, err := mailutil.ParseAddress("claire@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = listA.AddMember(claire, true, false, false, false)
	if err != nil {
		t.Fatal(err)
	}

	// add members

	noemi, err := mailutil.ParseAddress("noemi@example.net")
	if err != nil {
		t.Fatal(err)
	}

	oscar, err := mailutil.ParseAddress("oscar@example.org")
	if err != nil {
		t.Fatal(err)
	}

	listA.AddMembers(false, []*mailutil.Addr{noemi, oscar}, false, true, false, false, testAlerter{})

	// add known

	casey, err := mailutil.ParseAddress("casey@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = listA.AddKnown(casey)
	if err != nil {
		t.Fatal(err)
	}

	// add knowns

	noah, err := mailutil.ParseAddress("noah@example.net")
	if err != nil {
		t.Fatal(err)
	}

	owen, err := mailutil.ParseAddress("owen@example.org")
	if err != nil {
		t.Fatal(err)
	}

	listA.AddKnowns([]*mailutil.Addr{noah, owen}, testAlerter{})

	// get members

	members, err := listA.Members()
	if err != nil {
		t.Fatal(err)
	}

	expectedListAInfo := ListInfo{
		mailutil.Addr{
			Display: "A",
			Local:   "list_a",
			Domain:  "example.com",
		},
	}

	expectedMembers := []Membership{
		Membership{
			ListInfo:      expectedListAInfo,
			MemberAddress: "chris@example.com",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectedListAInfo,
			MemberAddress: "claire@example.com",
			Receive:       true,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectedListAInfo,
			MemberAddress: "noemi@example.net",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectedListAInfo,
			MemberAddress: "norah@example.net",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectedListAInfo,
			MemberAddress: "oscar@example.org",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
	}

	if len(expectedMembers) != len(members) {
		t.Fatalf("expected %d members, got %d", len(expectedMembers), len(members))
	}

	for i := range expectedMembers {
		if members[i] != expectedMembers[i] {
			t.Fatal()
		}
	}

	// get knowns

	expectedKnowns := []string{
		"casey@example.com",
		"noah@example.net",
		"owen@example.org",
	}

	knowns, err := listA.Knowns()
	if err != nil {
		t.Fatal(err)
	}

	for i := range expectedKnowns {
		if knowns[i] != expectedKnowns[i] {
			t.Fatal()
		}
	}

	// send mail

	lmtpTransaction(t, "some_envelope@example.com", []string{"list_a@example.com", "list_b@example.com"}, strings.NewReader(
`From: chris@example.com
To: list_a@example.com, list_b@example.com
Subject: foo

Hello`))

	expectedMails := []string{
`Content-Type: text/plain; charset=utf-8
From: "A" <list_a@example.com>
Subject: [A] Welcome
To: claire@example.com

Welcome to the mailing list list_a@example.com.

If you want to unsubscribe, please send an email with the subject "unsubscribe" to list_a@example.com.
`,
`From: "chris via A" <list_a@example.com>
List-Id: "A" <list_a@example.com>
List-Post: <mailto:list_a@example.com>
List-Unsubscribe: <mailto:list_a@example.com?subject=unsubscribe>
Reply-To: <chris@example.com>
Subject: [A] foo
To: list_a@example.com, list_b@example.com

Hello`,

`Content-Type: text/plain; charset=utf-8
From: "B" <list_b@example.com>
Subject: [B] A message needs moderation
To: otto@example.org

A message at "B" <list_b@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/list_b%40example.com
`,
	}

	for i, expectedMail := range expectedMails {
		expectedMail = strings.ReplaceAll(expectedMail, "\n", "\r\n") // this file has LF, mail header (RFC 5322 2.2) and text/plain body (RFC 2046 4.1.1) have CRLF line breaks
		if expectedMail != <- messageChannel {
			t.Fatalf("failed at message %d", i)
		}
	}

	// delete list

	err = listA.Delete()
	if err != nil {
		t.Fatal(err)
	}

	// check that list is deleted

	listA, err = GetList(listAddr)
	if err != sql.ErrNoRows {
		t.Fatalf("List has not been deleted, expected %v, got %v", sql.ErrNoRows, err)
	}
}
