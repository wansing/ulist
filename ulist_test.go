package main

import (
	"database/sql"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

const testDbPath = "/tmp/ulist-test.sqlite3"
const testGDPRLogPath = "/tmp/gdpr.log"

var expectGDPRLog string
var messageChannel = make(chan *ChanMTAMessage, 100)

func init() {
	mta = ChanMTA(messageChannel)
	SpoolDir = "/tmp/"
	Testmode = true
	WebUrl = "https://lists.example.com"

	_ = os.Remove(testGDPRLogPath)
	var err error
	if gdprLogger, err = util.NewFileLogger(testGDPRLogPath); err != nil {
		log.Fatalf("error creating GDPR logfile: %v", err)
	}
}

func expectErr(t *testing.T, err error, expect string) {
	if err.Error() != expect {
		t.Fatalf("got %v, expected %s", err, expect)
	}
}

func expectMessage(t *testing.T, envelopeFrom string, envelopeTo []string, message string) {

	message = strings.ReplaceAll(message, "\n", "\r\n") // this file has LF, mail header (RFC 5322 2.2) and text/plain body (RFC 2046 4.1.1) have CRLF line breaks
	got := <-messageChannel

	if envelopeFrom != got.EnvelopeFrom {
		t.Fatalf("expected envelope-from %s, got %s", envelopeFrom, got.EnvelopeFrom)
	}

	if len(envelopeTo) != len(got.EnvelopeTo) {
		t.Fatalf("expected %d envelope-to addresses, got %d", len(envelopeTo), len(got.EnvelopeTo))
	}

	for i := range envelopeTo {
		if envelopeTo[i] != got.EnvelopeTo[i] {
			t.Fatalf("expected envelope-to %s, got %s", envelopeTo[i], got.EnvelopeTo[i])
		}
	}

	if message != got.Message {
		t.Fatalf("expected message %s, got %s", message, got.Message)
	}
}

func lmtpTransaction(envelopeFrom string, envelopeTo []string, data string) error {

	backend := &LMTPBackend{}

	session, err := backend.AnonymousLogin(nil)
	if err != nil {
		return err
	}

	err = session.Mail(envelopeFrom, smtp.MailOptions{})
	if err != nil {
		return err
	}

	for _, to := range envelopeTo {
		err := session.Rcpt(to)
		if err != nil {
			return err
		}
	}

	return session.Data(strings.NewReader(data))
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

	// create list with "+bounces" address

	_, err = CreateList("list_b+bounces@example.com", "B", "otto@example.org", testAlerter{})

	expectErr(t, err, `list address can't end with "+bounces"`)

	// create list

	if _, err = CreateList("list_a@example.com", "A", "chris@example.com, norah@example.net", testAlerter{}); err != nil {
		t.Fatal(err)
	}

	if _, err = CreateList("list_b@example.com", "B", "otto@example.org", testAlerter{}); err != nil {
		t.Fatal(err)
	}

	// load list

	listAddrA, _ := mailutil.ParseAddress("list_a@example.com")

	listA, err := GetList(listAddrA)
	if err != nil {
		t.Fatal(err)
	}

	listAddrB, _ := mailutil.ParseAddress("list_b@example.com")

	listB, err := GetList(listAddrB)
	if err != nil {
		t.Fatal(err)
	}

	// listB set public signup

	if err = listB.Update("B", true, false, Pass, Pass, Pass, Mod); err != nil {
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

	chris, err := mailutil.ParseAddress("chris@example.com")
	if err != nil {
		t.Fatal(err)
	}

	err = listB.AddKnown(chris)
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

	membersA, err := listA.Members()
	if err != nil {
		t.Fatal(err)
	}

	expectListInfoA := ListInfo{
		mailutil.Addr{
			Display: "A",
			Local:   "list_a",
			Domain:  "example.com",
		},
	}

	expectMembersA := []Membership{
		Membership{
			ListInfo:      expectListInfoA,
			MemberAddress: "chris@example.com",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectListInfoA,
			MemberAddress: "claire@example.com",
			Receive:       true,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectListInfoA,
			MemberAddress: "noemi@example.net",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectListInfoA,
			MemberAddress: "norah@example.net",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectListInfoA,
			MemberAddress: "oscar@example.org",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
	}

	if len(expectMembersA) != len(membersA) {
		t.Fatalf("expected %d members, got %d", len(expectMembersA), len(membersA))
	}

	for i := range expectMembersA {
		if membersA[i] != expectMembersA[i] {
			t.Fatal()
		}
	}

	// get knowns

	expectKnownsA := []string{
		"noah@example.net",
		"owen@example.org",
	}

	expectKnownsB := []string{
		"chris@example.com",
	}

	knownsA, err := listA.Knowns()
	if err != nil {
		t.Fatal(err)
	}

	knownsB, err := listB.Knowns()
	if err != nil {
		t.Fatal(err)
	}

	for i := range expectKnownsA {
		if knownsA[i] != expectKnownsA[i] {
			t.Fatal()
		}
	}

	for i := range expectKnownsB {
		if knownsB[i] != expectKnownsB[i] {
			t.Fatal()
		}
	}

	// send mail to two lists

	err = lmtpTransaction("some_envelope@example.com", []string{"list_a@example.com", "list_b@example.com"},
		`From: chris@example.com
To: list_a@example.com, list_b@example.com
Subject: foo

Hello`)

	if err != nil {
		t.Fatal(err)
	}

	expectMessage(t, "list_a+bounces@example.com", []string{"claire@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "A" <list_a@example.com>
Subject: [A] Welcome
To: claire@example.com

Welcome to the mailing list list_a@example.com.

If you want to unsubscribe, please send an email with the subject "unsubscribe" to list_a@example.com.
`)

	expectMessage(t, "list_a+bounces@example.com", []string{"chris@example.com", "claire@example.com", "norah@example.net"}, `From: "chris via A" <list_a@example.com>
List-Id: "A" <list_a@example.com>
List-Post: <mailto:list_a@example.com>
List-Unsubscribe: <mailto:list_a@example.com?subject=unsubscribe>
Reply-To: <chris@example.com>
Subject: [A] foo
To: list_a@example.com, list_b@example.com

Hello`)

	expectMessage(t, "list_b+bounces@example.com", []string{"otto@example.org"}, `From: "chris via B" <list_b@example.com>
List-Id: "B" <list_b@example.com>
List-Post: <mailto:list_b@example.com>
List-Unsubscribe: <mailto:list_b@example.com?subject=unsubscribe>
Reply-To: <chris@example.com>
Subject: [B] foo
To: list_a@example.com, list_b@example.com

Hello`)

	// send mail which is moderated because of the "From" header

	err = lmtpTransaction("some_envelope@example.com", []string{"list_b@example.com"},
		`From: norah@example.net
To: list_b@example.com
Subject: foo

Hello`)

	if err != nil {
		t.Fatal(err)
	}

	expectMessage(t, "list_b+bounces@example.com", []string{"otto@example.org"}, `Content-Type: text/plain; charset=utf-8
From: "B" <list_b@example.com>
Subject: [B] A message needs moderation
To: otto@example.org

A message at "B" <list_b@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/list_b%40example.com
`)

	// join a list which allows for public signup

	err = lmtpTransaction("some_envelope@example.com", []string{"list_b@example.com"},
		`From: cleo@example.com
To: list_b@example.com
Subject: subscribe

Hello`)

	if err != nil {
		t.Fatal(err)
	}

	expectGDPRLog += "subscribing cleo@example.com to the list list_b@example.com, reason: email\n"

	expectMessage(t, "list_b+bounces@example.com", []string{"cleo@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "B" <list_b@example.com>
Subject: [B] Welcome
To: cleo@example.com

Welcome to the mailing list list_b@example.com.

If you want to unsubscribe, please send an email with the subject "unsubscribe" to list_b@example.com.
`)

	// send mail which is moderated because of the "X-Spam-Status" header

	err = lmtpTransaction("some_envelope@example.com", []string{"list_a@example.com"},
		`From: norah@example.net
To: list_a@example.com
Subject: foo
X-Spam-Status: Yes, score=12

Hello`)

	if err != nil {
		t.Fatal(err)
	}

	expectMessage(t, "list_a+bounces@example.com", []string{"chris@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "A" <list_a@example.com>
Subject: [A] A message needs moderation
To: chris@example.com

A message at "A" <list_a@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/list_a%40example.com
`)

	expectMessage(t, "list_a+bounces@example.com", []string{"norah@example.net"}, `Content-Type: text/plain; charset=utf-8
From: "A" <list_a@example.com>
Subject: [A] A message needs moderation
To: norah@example.net

A message at "A" <list_a@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/list_a%40example.com
`)

	// send looped mail with List-Id header

	err = lmtpTransaction("some_envelope@example.com", []string{"list_a@example.com"},
		`From: chris@example.com
To: list_a@example.com
List-Id: "A" <list_a@example.com>
Subject: foo

Hello`)

	expectErr(t, err, "email loop detected: list_a@example.com")

	// send email to bounce address

	err = lmtpTransaction("some_envelope@example.com", []string{"list_a+bounces@example.com"},
		`From: chris@example.com
To: list_a+bounces@example.com
Subject: foo

bar`)

	expectErr(t, err, "bounce address accepts only bounce notifications (with empty envelope-from)")

	// send bounce notification to list address

	err = lmtpTransaction("", []string{"list_a@example.com"},
		`From: chris@example.com
To: list_a@example.com
Subject: foo

bar`)

	expectErr(t, err, "got bounce notification (with empty envelope-from) to non-bounce address")

	// send bounce notification to bounce address

	err = lmtpTransaction("", []string{"list_a+bounces@example.com"},
		`From: chris@example.com
To: list_a+bounces@example.com
Subject: Some Subject

This is a bounce notification blah blah.`)

	expectMessage(t, "", []string{"chris@example.com", "norah@example.net"}, `Content-Type: text/plain; charset=utf-8
From: "A" <list_a@example.com>
Subject: [A] Bounce notification: Some Subject
To: list_a+bounces@example.com

This is a bounce notification blah blah.`)

	// send message with list not in To or Cc

	err = lmtpTransaction("some_envelope@example.net", []string{"list_a@example.com"},
		`From: norah@example.net
To: something_else@example.com
Cc: more@example.com
Subject: Some Subject

Hi`)

	expectErr(t, err, "list address list_a@example.com is not in To or Cc")

	// send same message with list in Cc

	err = lmtpTransaction("some_envelope@example.net", []string{"list_a@example.com"},
		`From: norah@example.net
To: something_else@example.com
Cc: more@example.com, list_a@example.com
Subject: Some Subject

Hi`)

	expectMessage(t, "list_a+bounces@example.com", []string{"chris@example.com", "claire@example.com", "norah@example.net"},
`Cc: more@example.com, list_a@example.com
From: "norah via A" <list_a@example.com>
List-Id: "A" <list_a@example.com>
List-Post: <mailto:list_a@example.com>
List-Unsubscribe: <mailto:list_a@example.com?subject=unsubscribe>
Reply-To: <norah@example.net>
Subject: [A] Some Subject
To: something_else@example.com

Hi`)

	// test encoding of special characters

	if _, err = CreateList("list_ue@example.com", "List Ü", "user_ue@example.com", testAlerter{}); err != nil {
		t.Fatal(err)
	}

	err = lmtpTransaction("user_ue@example.com", []string{"list_ue@example.com"},
		`From: =?utf-8?q?User_=C3=9C?= <user_ue@example.com>
To: "List Ü" <list_ue@example.com>
Subject: =?utf-8?q?Hell=C3=B6?=

Hi`) // note that the "To" header is not encoded properly

	expectMessage(t, "list_ue+bounces@example.com", []string{"user_ue@example.com"},
`From: =?utf-8?q?User_=C3=9C_via_List_=C3=9C?= <list_ue@example.com>
List-Id: =?utf-8?q?List_=C3=9C?= <list_ue@example.com>
List-Post: <mailto:list_ue@example.com>
List-Unsubscribe: <mailto:list_ue@example.com?subject=unsubscribe>
Reply-To: =?utf-8?q?User_=C3=9C?= <user_ue@example.com>
Subject: =?utf-8?q?[List_=C3=9C]_Hell=C3=B6?=
To: "List Ü" <list_ue@example.com>

Hi`) // the "To" header stays unencoded, as we're minimally invasive here

	// delete list

	err = listA.Delete()
	if err != nil {
		t.Fatal(err)
	}

	err = listB.Delete()
	if err != nil {
		t.Fatal(err)
	}

	// check that list is deleted

	_, err = GetList(listAddrA)
	if err != sql.ErrNoRows {
		t.Fatalf("list has not been deleted, expected %v, got %v", sql.ErrNoRows, err)
	}

	_, err = GetList(listAddrB)
	if err != sql.ErrNoRows {
		t.Fatalf("list has not been deleted, expected %v, got %v", sql.ErrNoRows, err)
	}

	// check GDPR log

	gotBytes, err := ioutil.ReadFile(testGDPRLogPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes[20:])

	if got != expectGDPRLog {
		t.Fatalf("got %s, expected %s", got, expectGDPRLog)
	}
}
