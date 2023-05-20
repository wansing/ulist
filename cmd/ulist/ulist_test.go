package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"github.com/wansing/ulist"
	"github.com/wansing/ulist/filelog"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/repo/sqlite"
	"github.com/wansing/ulist/web"
)

const testDbPath = "/tmp/ulist-test.sqlite3"

var ul *ulist.Ulist

var gdprChannel = make(chan string, 100)
var messageChannel = make(chan *mailutil.MTAEnvelope, 100)

var messageIdPattern = regexp.MustCompile("[0-9a-z-_]{32}") // copied from listinfo_test.go
var mimeBoundaryPattern = regexp.MustCompile("[0-9a-f]{60}")
var timestampHMACPattern = regexp.MustCompile("[0-9]{10}/[-_0-9a-zA-Z]{43}")
var urlPattern = regexp.MustCompile("/(join|leave)/[^/\r\n]+(/" + timestampHMACPattern.String() + "/[^/\r\n]+)?") // without WebUrl

func init() {

	_ = os.Remove(testDbPath)

	listDB, err := sqlite.OpenListDB(testDbPath)
	if err != nil {
		log.Fatalf("error creating database: %v", err)
	}

	ul = &ulist.Ulist{
		DummyMode:  true,
		GDPRLogger: filelog.ChanLogger(gdprChannel),
		Lists:      listDB,
		MTA:        mailutil.ChanMTA(messageChannel),
		SpoolDir:   "/tmp",
	}

	ul.Web = web.Web{
		Ulist:  ul,
		Listen: "127.0.0.1:65535",
		URL:    "https://lists.example.com",
	}

	// web server only
	go func() {
		if err := ul.Web.ListenAndServe(); err != nil {
			log.Fatalln(err)
		}
	}()
}

func mustParse(email string) *mailutil.Addr {
	addr, err := mailutil.ParseAddress(email)
	if err != nil {
		panic(err)
	}
	return addr
}

func wantErr(t *testing.T, got error, want string) {
	if got == nil {
		t.Fatalf("got nil, want %s", want)
	}
	if got.Error() != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func wantErrs(t *testing.T, got []error, want string) {
	if len(got) == 0 {
		t.Fatalf("got nil, want %s", want)
	}
	if got[0].Error() != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func wantGDPREvent(t *testing.T, want string) {
	got := <-gdprChannel
	got = strings.TrimSuffix(got, "\n")
	if want != got {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func wantMessage(t *testing.T, envelopeFrom string, envelopeTo []string, messageLF string) (href string) {

	message := strings.ReplaceAll(messageLF, "\n", "\r\n") // this file has LF line breaks, mail header (RFC 5322 2.2) and text/plain body (RFC 2046 4.1.1) must have CRLF line breaks

	got := <-messageChannel

	// unify MIME boundaries

	var boundaries = make(map[string]string) // random boundary -> stable id

	got.Message = mimeBoundaryPattern.ReplaceAllStringFunc(got.Message, func(boundary string) string {
		id, ok := boundaries[boundary]
		if !ok {
			id = fmt.Sprintf("boundary-%d", len(boundaries))
			boundaries[boundary] = id
		}
		return id
	})

	// extract href

	href = urlPattern.FindString(got.Message)

	// replace HMACs in urls by "hmac"

	got.Message = timestampHMACPattern.ReplaceAllString(got.Message, "timestamp/hmac")

	// replace message ids by "message-id"

	got.Message = messageIdPattern.ReplaceAllString(got.Message, "message-id")

	// compare

	if got.EnvelopeFrom != envelopeFrom {
		t.Fatalf("got envelope-from %s, want %s", got.EnvelopeFrom, envelopeFrom)
	}

	if len(got.EnvelopeTo) != len(envelopeTo) {
		t.Fatalf("got %d envelope-to, want %d", len(got.EnvelopeTo), len(envelopeTo))
	}

	for i := range envelopeTo {
		if got.EnvelopeTo[i] != envelopeTo[i] {
			t.Fatalf("got envelope-to[%d] %s, want %s", i, got.EnvelopeTo[i], envelopeTo[i])
		}
	}

	if got.Message != message {
		ioutil.WriteFile("/tmp/got", []byte(got.Message), os.ModePerm)
		ioutil.WriteFile("/tmp/want", []byte(message), os.ModePerm)
		t.Fatalf("got message %s, want %s", got.Message, message)
	}

	return
}

func wantChansEmpty(t *testing.T) {
	time.Sleep(10 * time.Millisecond) // wait until all emails are sent and all events are written
	var failed = false
	for {
		select {
		case event := <-gdprChannel:
			failed = true
			t.Logf("want empty GDPR channel, got event:\n%s", event)
		case envelope := <-messageChannel:
			failed = true
			t.Logf("want empty message channel, got message:\n%s", envelope.Message)
		default:
			if failed {
				t.FailNow()
			} else {
				return
			}
		}
	}
}

func mustTransactOne(envelopeFrom string, envelopeTo []string, data string) {
	if err := transactOne(envelopeFrom, envelopeTo, data); err != nil {
		panic(err)
	}
}

func transactOne(envelopeFrom string, envelopeTo []string, data string) error {
	return transact(
		&mailutil.MTAEnvelope{
			EnvelopeFrom: envelopeFrom,
			EnvelopeTo:   envelopeTo,
			Message:      data,
		},
	)
}

func mustTransact(envelopes ...*mailutil.MTAEnvelope) {
	if err := transact(envelopes...); err != nil {
		panic(err)
	}
}

func transact(envelopes ...*mailutil.MTAEnvelope) error {

	backend := &ulist.LMTPBackend{
		Ulist: ul,
	}

	session, err := backend.AnonymousLogin(nil)
	if err != nil {
		return err
	}

	for _, envelope := range envelopes {

		err = session.Mail(envelope.EnvelopeFrom, smtp.MailOptions{})
		if err != nil {
			return err
		}

		for _, to := range envelope.EnvelopeTo {
			err := session.Rcpt(to)
			if err != nil {
				return err
			}
		}

		// this file has LF line breaks, mail header (RFC 5322 2.2) and text/plain body (RFC 2046 4.1.1) must have CRLF line breaks
		err = session.Data(strings.NewReader(strings.ReplaceAll(envelope.Message, "\n", "\r\n")))
		if err != nil {
			return err
		}
	}

	return nil
}

func TestCreateListBounceSuffix(t *testing.T) {
	_, _, errs := ul.CreateList("suffix+bounces@example.com", "name", "", "testing")
	wantErrs(t, errs, `list address can't end with "+bounces"`)
	wantChansEmpty(t)
}

func TestGetList(t *testing.T) {

	if _, _, errs := ul.CreateList("get-list@example.com", "Created List", "", "testing"); errs != nil {
		t.Fatal(errs)
	}

	got, err := ul.Lists.GetList(mustParse("get-list@example.com"))
	if err != nil {
		t.Fatal(err)
	}

	// HMACKey is random

	if len(got.HMACKey) != 32 {
		t.Fatalf("got %d bytes HMAC key, want 32", len(got.HMACKey))
	}

	var sum = 0
	for _, b := range got.HMACKey {
		sum += int(b)
	}

	if sum == 0 {
		t.Fatalf("HMACKey is all zeroes")
	}

	got.HMACKey = nil

	// compare ulist.ListInfo

	want := ulist.ListInfo{
		ID: 1,
		Addr: mailutil.Addr{
			Display: "Created List",
			Local:   "get-list",
			Domain:  "example.com",
		},
	}

	if got.ListInfo != want {
		t.Fatalf("got list %+v, want %+v", got.ListInfo, want)
	}

	wantChansEmpty(t)
}

func TestDeleteList(t *testing.T) {

	ul.CreateList("delete-list@example.com", "List", "", "testing")

	list, _ := ul.Lists.GetList(mustParse("delete-list@example.com"))

	err := ul.Lists.Delete(list)
	if err != nil {
		t.Fatal(err)
	}

	list, err = ul.Lists.GetList(mustParse("delete-list@example.com"))
	if list != nil || err != nil {
		t.Fatalf("got %v, %v, want nil, nil", list, err)
	}

	wantChansEmpty(t)
}

func TestMultipleReceivers(t *testing.T) {

	ul.CreateList("createlist@example.com", "Created List", "alice@example.com, bob@example.net, carol@example.org", "testing")

	wantGDPREvent(t, `alice@example.com joined the list createlist@example.com, reason: testing
	bob@example.net joined the list createlist@example.com, reason: testing
	carol@example.org joined the list createlist@example.com, reason: testing`)

	wantMessage(t, "createlist+bounces@example.com", []string{"alice@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "Created List" <createlist@example.com>
Message-Id: <message-id@example.com>
Subject: [Created List] Welcome
To: alice@example.com

Hello alice@example.com,

Welcome to the mailing list createlist@example.com.

----
You can leave the mailing list "Created List" here: https://lists.example.com/leave/createlist@example.com`)

	wantMessage(t, "createlist+bounces@example.com", []string{"bob@example.net"}, `Content-Type: text/plain; charset=utf-8
From: "Created List" <createlist@example.com>
Message-Id: <message-id@example.com>
Subject: [Created List] Welcome
To: bob@example.net

Hello bob@example.net,

Welcome to the mailing list createlist@example.com.

----
You can leave the mailing list "Created List" here: https://lists.example.com/leave/createlist@example.com`)

	wantMessage(t, "createlist+bounces@example.com", []string{"carol@example.org"}, `Content-Type: text/plain; charset=utf-8
From: "Created List" <createlist@example.com>
Message-Id: <message-id@example.com>
Subject: [Created List] Welcome
To: carol@example.org

Hello carol@example.org,

Welcome to the mailing list createlist@example.com.

----
You can leave the mailing list "Created List" here: https://lists.example.com/leave/createlist@example.com`)

	mustTransactOne("some_envelope@example.com", []string{"createlist@example.com"},
		`From: bob@example.net
To: createlist@example.com
Subject: Hi

Hello World`)

	wantMessage(t, "createlist+bounces@example.com", []string{"alice@example.com", "bob@example.net", "carol@example.org"}, `From: "bob via Created List" <createlist@example.com>
List-Id: "Created List" <createlist@example.com>
List-Post: <mailto:createlist@example.com>
List-Unsubscribe: <mailto:createlist@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <bob@example.net>
Subject: [Created List] Hi
To: createlist@example.com

Hello World

----
You can leave the mailing list "Created List" here: https://lists.example.com/leave/createlist@example.com`)

	wantChansEmpty(t)
}

func TestMultipleLists(t *testing.T) {

	ul.CreateList("multiple-a@example.com", "A", "alice@example.com", "testing")
	ul.CreateList("multiple-b@example.net", "B", "alice@example.com", "testing")

	<-messageChannel // welcome alice A
	<-messageChannel // welcome alice B

	wantGDPREvent(t, "alice@example.com joined the list multiple-a@example.com, reason: testing")
	wantGDPREvent(t, "alice@example.com joined the list multiple-b@example.net, reason: testing")

	// one email, two recipients (envelope-to)

	mustTransactOne("some_envelope@example.com", []string{"multiple-a@example.com", "multiple-b@example.net"},
		`From: alice@example.com
To: multiple-a@example.com, multiple-b@example.net
Subject: Hi

Hello World`)

	wantMessage(t, "multiple-a+bounces@example.com", []string{"alice@example.com"}, `From: "alice via A" <multiple-a@example.com>
List-Id: "A" <multiple-a@example.com>
List-Post: <mailto:multiple-a@example.com>
List-Unsubscribe: <mailto:multiple-a@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <alice@example.com>
Subject: [A] Hi
To: multiple-a@example.com, multiple-b@example.net

Hello World

----
You can leave the mailing list "A" here: https://lists.example.com/leave/multiple-a@example.com`)

	wantMessage(t, "multiple-b+bounces@example.net", []string{"alice@example.com"}, `From: "alice via B" <multiple-b@example.net>
List-Id: "B" <multiple-b@example.net>
List-Post: <mailto:multiple-b@example.net>
List-Unsubscribe: <mailto:multiple-b@example.net?subject=leave>
Message-Id: <message-id@example.net>
Reply-To: <alice@example.com>
Subject: [B] Hi
To: multiple-a@example.com, multiple-b@example.net

Hello World

----
You can leave the mailing list "B" here: https://lists.example.com/leave/multiple-b@example.net`)

	// one SMTP transaction, two emails

	mustTransact(
		&mailutil.MTAEnvelope{
			EnvelopeFrom: "some_envelope@example.com",
			EnvelopeTo:   []string{"multiple-a@example.com"},
			Message: `From: alice@example.com
To: multiple-a@example.com
Subject: Hi

Hello`},
		&mailutil.MTAEnvelope{
			EnvelopeFrom: "some_envelope@example.com",
			EnvelopeTo:   []string{"multiple-b@example.net"},
			Message: `From: alice@example.com
To: multiple-b@example.net
Subject: Hi

Hello`},
	)

	wantMessage(t, "multiple-a+bounces@example.com", []string{"alice@example.com"}, `From: "alice via A" <multiple-a@example.com>
List-Id: "A" <multiple-a@example.com>
List-Post: <mailto:multiple-a@example.com>
List-Unsubscribe: <mailto:multiple-a@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <alice@example.com>
Subject: [A] Hi
To: multiple-a@example.com

Hello

----
You can leave the mailing list "A" here: https://lists.example.com/leave/multiple-a@example.com`)

	wantMessage(t, "multiple-b+bounces@example.net", []string{"alice@example.com"}, `From: "alice via B" <multiple-b@example.net>
List-Id: "B" <multiple-b@example.net>
List-Post: <mailto:multiple-b@example.net>
List-Unsubscribe: <mailto:multiple-b@example.net?subject=leave>
Message-Id: <message-id@example.net>
Reply-To: <alice@example.com>
Subject: [B] Hi
To: multiple-b@example.net

Hello

----
You can leave the mailing list "B" here: https://lists.example.com/leave/multiple-b@example.net`)

	wantChansEmpty(t)
}

func TestPublicList(t *testing.T) {

	ul.CreateList("public@example.com", "Whoops", "", "testing")

	list, _ := ul.Lists.GetList(mustParse("public@example.com"))

	if err := ul.Lists.Update(list, "Public", true, false, ulist.Pass, ulist.Pass, ulist.Pass, ulist.Mod); err != nil {
		t.Fatal(err)
	}

	// ask join

	mustTransactOne("some_envelope@example.com", []string{"public@example.com"},
		`From: bob@example.com
To: public@example.com
Subject: join

`)

	confirmJoinHref := wantMessage(t, "public+bounces@example.com", []string{"bob@example.com"},
		`Content-Type: text/plain; charset=utf-8
From: "Public" <public@example.com>
Message-Id: <message-id@example.com>
Subject: [Public] Please confirm: join the mailing list public@example.com
To: bob@example.com

You receive this mail because you (or someone else) asked to join your email address bob@example.com to the mailing list public@example.com.

To confirm, please visit this address:

https://lists.example.com/join/public@example.com/timestamp/hmac/bob@example.com

If you didn't request this, please ignore this email.`)

	// confirm join

	(&http.Client{}).Post("http://127.0.0.1:65535"+confirmJoinHref, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"confirm_join": []string{"yes"}}.Encode()))
	// returns an error because it tries to follow the redirect to lists.example.com, but we can ignore that

	wantMessage(t, "public+bounces@example.com", []string{"bob@example.com"},
		`Content-Type: text/plain; charset=utf-8
From: "Public" <public@example.com>
Message-Id: <message-id@example.com>
Subject: [Public] Welcome
To: bob@example.com

Hello bob@example.com,

Welcome to the mailing list public@example.com.

----
You can leave the mailing list "Public" here: https://lists.example.com/leave/public@example.com`)

	wantGDPREvent(t, "bob@example.com joined the list public@example.com, reason: user confirmed in web ui")

	// test membership

	if got, err := ul.Lists.GetMembership(list, mustParse("bob@example.com")); err == nil {
		want := ulist.Membership{
			ListInfo: ulist.ListInfo{
				ID: 5,
				Addr: mailutil.Addr{
					Display: "Public",
					Local:   "public",
					Domain:  "example.com",
				},
			},
			Member:        true,
			MemberAddress: "bob@example.com",
			Receive:       true,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
		}
		if got != want {
			t.Fatalf("got %v, want %v", got, want)
		}
	} else {
		t.Fatal(err)
	}

	// ask leave

	mustTransactOne("some_envelope@example.com", []string{"public@example.com"},
		`From: bob@example.com
To: public@example.com
Subject: leave

`)

	confirmLeaveHref := wantMessage(t, "public+bounces@example.com", []string{"bob@example.com"},
		`Content-Type: text/plain; charset=utf-8
From: "Public" <public@example.com>
Message-Id: <message-id@example.com>
Subject: [Public] Please confirm: leave the mailing list public@example.com
To: bob@example.com

You receive this mail because you (or someone else) asked to remove your email address bob@example.com from the mailing list public@example.com.

To confirm, please visit this address:

https://lists.example.com/leave/public@example.com/timestamp/hmac/bob@example.com

If you didn't request this, please ignore this email.`)

	// confirm leave

	(&http.Client{}).Post("http://127.0.0.1:65535"+confirmLeaveHref, "application/x-www-form-urlencoded", strings.NewReader(url.Values{"confirm_leave": []string{"yes"}}.Encode()))

	wantMessage(t, "public+bounces@example.com", []string{"bob@example.com"},
		`Content-Type: text/plain; charset=utf-8
From: "Public" <public@example.com>
Message-Id: <message-id@example.com>
Subject: [Public] Goodbye
To: bob@example.com

You left the mailing list public@example.com.

Goodbye!`)

	wantGDPREvent(t, "bob@example.com left the list public@example.com, reason: user confirmed in web ui")

	// test membership

	if membership, err := ul.Lists.GetMembership(list, mustParse("bob@example.com")); membership.Member != false || err != nil {
		t.Fatalf("got %v, %v, want false, nil", membership.Member, err)
	}

	wantChansEmpty(t)
}

func TestRejectAll(t *testing.T) {

	ul.CreateList("reject-all@example.com", "List name", "", "testing")
	list, _ := ul.Lists.GetList(mustParse("reject-all@example.com"))
	ul.Lists.Update(list, "List name", false, false, ulist.Reject, ulist.Reject, ulist.Reject, ulist.Reject)

	ul.Lists.AddKnowns(list, []*ulist.Addr{mustParse("known@example.com")})
	ul.AddMembers(list, true, []*ulist.Addr{mustParse("member@example.com")}, true, false, false, false, false, "testing")
	ul.AddMembers(list, true, []*ulist.Addr{mustParse("mod@example.com")}, true, true, false, false, false, "testing")

	wantGDPREvent(t, "member@example.com joined the list reject-all@example.com, reason: testing")
	wantGDPREvent(t, "mod@example.com joined the list reject-all@example.com, reason: testing")

	for _, test := range []string{"unknown", "known", "member", "mod"} {
		err := transactOne("some_envelope@example.com", []string{"reject-all@example.com"},
			`From: `+test+`@example.com
To: reject-all@example.com

`)
		wantErr(t, err, "user not found")
	}

	wantMessage(t, "reject-all+bounces@example.com", []string{"member@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List name" <reject-all@example.com>
Message-Id: <message-id@example.com>
Subject: [List name] Welcome
To: member@example.com

Hello member@example.com,

Welcome to the mailing list reject-all@example.com.

----
You can leave the mailing list "List name" here: https://lists.example.com/leave/reject-all@example.com`)

	wantMessage(t, "reject-all+bounces@example.com", []string{"mod@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List name" <reject-all@example.com>
Message-Id: <message-id@example.com>
Subject: [List name] Welcome
To: mod@example.com

Hello mod@example.com,

Welcome to the mailing list reject-all@example.com.

----
You can leave the mailing list "List name" here: https://lists.example.com/leave/reject-all@example.com`)

	wantChansEmpty(t)
}

func TestLoop(t *testing.T) {

	ul.CreateList("loop@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // alice

	err := transactOne("some_envelope@example.com", []string{"loop@example.com"},
		`From: chris@example.com
To: loop@example.com
List-Id: "List" <loop@example.com>
Subject: Hi

Hello`)

	wantErr(t, err, "email loop detected: loop@example.com")

	wantChansEmpty(t)
}

func TestMultipleNotifieds(t *testing.T) {

	ul.CreateList("multiple-notifieds@example.com", "List", "alice@example.com, bob@example.com, carol@example.com", "testing")

	<-messageChannel // welcome alice
	<-messageChannel // welcome bob
	<-messageChannel // welcome carol

	wantGDPREvent(t, `alice@example.com joined the list multiple-notifieds@example.com, reason: testing
	bob@example.com joined the list multiple-notifieds@example.com, reason: testing
	carol@example.com joined the list multiple-notifieds@example.com, reason: testing`)

	mustTransactOne("some_envelope@example.com", []string{"multiple-notifieds@example.com"},
		`From: unknown@example.com
To: multiple-notifieds@example.com
Subject: Hi

Hello`)

	wantMessage(t, "multiple-notifieds+bounces@example.com", []string{"alice@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List" <multiple-notifieds@example.com>
Message-Id: <message-id@example.com>
Subject: [List] A message needs moderation
To: alice@example.com

A message at "List" <multiple-notifieds@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/multiple-notifieds@example.com

----
You can leave the mailing list "List" here: https://lists.example.com/leave/multiple-notifieds@example.com`)

	wantMessage(t, "multiple-notifieds+bounces@example.com", []string{"bob@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List" <multiple-notifieds@example.com>
Message-Id: <message-id@example.com>
Subject: [List] A message needs moderation
To: bob@example.com

A message at "List" <multiple-notifieds@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/multiple-notifieds@example.com

----
You can leave the mailing list "List" here: https://lists.example.com/leave/multiple-notifieds@example.com`)

	wantMessage(t, "multiple-notifieds+bounces@example.com", []string{"carol@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List" <multiple-notifieds@example.com>
Message-Id: <message-id@example.com>
Subject: [List] A message needs moderation
To: carol@example.com

A message at "List" <multiple-notifieds@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/multiple-notifieds@example.com

----
You can leave the mailing list "List" here: https://lists.example.com/leave/multiple-notifieds@example.com`)

	wantChansEmpty(t)
}

func TestXSpamStatus(t *testing.T) {

	ul.CreateList("x-spam-status@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // alice

	mustTransactOne("some_envelope@example.com", []string{"x-spam-status@example.com"},
		`From: alice@example.com
To: x-spam-status@example.com
Subject: Hi
X-Spam-Status: Yes, score=12

Hello`)

	wantMessage(t, "x-spam-status+bounces@example.com", []string{"alice@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List" <x-spam-status@example.com>
Message-Id: <message-id@example.com>
Subject: [List] A message needs moderation
To: alice@example.com

A message at "List" <x-spam-status@example.com> is waiting for moderation.

You can moderate it here: https://lists.example.com/mod/x-spam-status@example.com

----
You can leave the mailing list "List" here: https://lists.example.com/leave/x-spam-status@example.com`)

	wantChansEmpty(t)
}

func TestMailToBounce(t *testing.T) {

	ul.CreateList("mail-to-bounce@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // alice

	err := transactOne("some_envelope@example.com", []string{"mail-to-bounce+bounces@example.com"},
		`From: alice@example.com
To: mail-to-bounce+bounces@example.com
Subject: Hi

Hello`)

	wantErr(t, err, "bounce address accepts only bounce notifications (with empty envelope-from)")

	wantChansEmpty(t)
}

func TestBounceToList(t *testing.T) {

	ul.CreateList("bounce-to-list@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // alice

	err := transactOne("", []string{"bounce-to-list@example.com"},
		`From: alice@example.com
To: bounce-to-list@example.com
Subject: Hi

Hello`)

	wantErr(t, err, "got bounce notification (with empty envelope-from) to non-bounce address")

	wantChansEmpty(t)
}

func TestBounceToBounce(t *testing.T) {

	ul.CreateList("bounce-to-bounce@example.com", "List", "carol@example.com", "testing")

	<-messageChannel // welcome carol
	<-gdprChannel    // carol

	mustTransactOne("", []string{"bounce-to-bounce+bounces@example.com"},
		`From: carol@example.com
To: bounce-to-bounce+bounces@example.com
Subject: could not deliver message

Sorry`)

	wantMessage(t, "", []string{"carol@example.com"}, `Content-Type: text/plain; charset=utf-8
From: "List" <bounce-to-bounce@example.com>
Message-Id: <message-id@example.com>
Subject: [List] Bounce notification: could not deliver message
To: bounce-to-bounce+bounces@example.com

Sorry`)

	wantChansEmpty(t)
}

func TestCcBcc(t *testing.T) {

	ul.CreateList("cc-bcc@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // alice

	// BCC

	err := transactOne("some_envelope@example.com", []string{"cc-bcc@example.com"},
		`From: alice@example.com
To: foo@example.com
Cc: bar@example.com
Subject: Hi

Hello`)

	wantErr(t, err, "list address cc-bcc@example.com is not in To or Cc")

	// CC

	mustTransactOne("some_envelope@example.net", []string{"cc-bcc@example.com"},
		`From: alice@example.com
To: foo@example.com
Cc: bar@example.com, cc-bcc@example.com
Subject: Hi

Hello`)

	wantMessage(t, "cc-bcc+bounces@example.com", []string{"alice@example.com"},
		`Cc: bar@example.com, cc-bcc@example.com
From: "alice via List" <cc-bcc@example.com>
List-Id: "List" <cc-bcc@example.com>
List-Post: <mailto:cc-bcc@example.com>
List-Unsubscribe: <mailto:cc-bcc@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <alice@example.com>
Subject: [List] Hi
To: foo@example.com

Hello

----
You can leave the mailing list "List" here: https://lists.example.com/leave/cc-bcc@example.com`)

	wantChansEmpty(t)
}

func TestEncodeSpecialChars(t *testing.T) {

	ul.CreateList("list_ue@example.com", "List Ü", "user_ue@example.com", "testing")

	<-messageChannel // welcome user_ue
	<-gdprChannel    // alice

	mustTransactOne("user_ue@example.com", []string{"list_ue@example.com"},
		`From: =?utf-8?q?User_=C3=9C?= <user_ue@example.com>
To: "List Ü" <list_ue@example.com>
Subject: =?utf-8?q?Hell=C3=B6?=

Hi`) // note that the "To" header is not encoded properly

	wantMessage(t, "list_ue+bounces@example.com", []string{"user_ue@example.com"},
		`From: =?utf-8?q?User_=C3=9C_via_List_=C3=9C?= <list_ue@example.com>
List-Id: =?utf-8?q?List_=C3=9C?= <list_ue@example.com>
List-Post: <mailto:list_ue@example.com>
List-Unsubscribe: <mailto:list_ue@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: =?utf-8?q?User_=C3=9C?= <user_ue@example.com>
Subject: =?utf-8?q?[List_=C3=9C]_Hell=C3=B6?=
To: "List Ü" <list_ue@example.com>

Hi

----
You can leave the mailing list "List Ü" here: https://lists.example.com/leave/list_ue@example.com`) // the "To" header stays unencoded, as we're minimally invasive here

	wantChansEmpty(t)
}

func TestMultipartAlternativeMessageFooter(t *testing.T) {

	ul.CreateList("multipart-alternative-message@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel

	mustTransactOne("some_envelope@example.com", []string{"multipart-alternative-message@example.com"},
		`From: alice@example.com
To: multipart-alternative-message@example.com
Subject: Hi
Content-Type: multipart/alternative; boundary="original-boundary"

--original-boundary
Content-Type: text/plain; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: inline

Hello plain text world!

--original-boundary
Content-Type: text/html; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: inline

<p>Hello HTML world!</p>

--original-boundary--
`)

	wantMessage(t, "multipart-alternative-message+bounces@example.com", []string{"alice@example.com"},
		`Content-Type: multipart/mixed;
 boundary=boundary-0
From: "alice via List" <multipart-alternative-message@example.com>
List-Id: "List" <multipart-alternative-message@example.com>
List-Post: <mailto:multipart-alternative-message@example.com>
List-Unsubscribe: <mailto:multipart-alternative-message@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <alice@example.com>
Subject: [List] Hi
To: multipart-alternative-message@example.com

--boundary-0
Content-Type: multipart/alternative; boundary="original-boundary"

--original-boundary
Content-Type: text/plain; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: inline

Hello plain text world!

--original-boundary
Content-Type: text/html; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: inline

<p>Hello HTML world!</p>

--original-boundary--

--boundary-0
Content-Disposition: inline
Content-Type: multipart/alternative; boundary=boundary-1

--boundary-1
Content-Disposition: inline
Content-Type: text/plain; charset=us-ascii

You can leave the mailing list "List" here: https://lists.example.com/leave/multipart-alternative-message@example.com
--boundary-1
Content-Disposition: inline
Content-Type: text/html; charset=us-ascii

<span style="font-size: 9pt;">You can leave the mailing list "List" <a href="https://lists.example.com/leave/multipart-alternative-message@example.com">here</a>.</span>
--boundary-1--

--boundary-0--
`)

	wantChansEmpty(t)
}

func TestMultipartMixedMessageFooter(t *testing.T) {

	ul.CreateList("multipart-mixed-message@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel

	mustTransactOne("some_envelope@example.com", []string{"multipart-mixed-message@example.com"},
		`From: alice@example.com
To: multipart-mixed-message@example.com
Subject: Hi
Content-Type: multipart/mixed; boundary="original-boundary"

--original-boundary
Content-Type: text/plain; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: inline

Hello plain text world!

--original-boundary
Content-Type: text/html; charset="utf-8"
Content-Transfer-Encoding: quoted-printable
Content-Disposition: attachment; filename=hello.html

<p>This is an attachment.</p>

--original-boundary--
`)

	wantMessage(t, "multipart-mixed-message+bounces@example.com", []string{"alice@example.com"},
		`Content-Type: multipart/mixed; boundary="original-boundary"
From: "alice via List" <multipart-mixed-message@example.com>
List-Id: "List" <multipart-mixed-message@example.com>
List-Post: <mailto:multipart-mixed-message@example.com>
List-Unsubscribe: <mailto:multipart-mixed-message@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <alice@example.com>
Subject: [List] Hi
To: multipart-mixed-message@example.com

--original-boundary
Content-Disposition: inline
Content-Type: text/plain; charset="utf-8"

Hello plain text world!

--original-boundary
Content-Disposition: inline
Content-Type: multipart/alternative; boundary=boundary-0

--boundary-0
Content-Disposition: inline
Content-Type: text/plain; charset=us-ascii

You can leave the mailing list "List" here: https://lists.example.com/leave/multipart-mixed-message@example.com
--boundary-0
Content-Disposition: inline
Content-Type: text/html; charset=us-ascii

<span style="font-size: 9pt;">You can leave the mailing list "List" <a href="https://lists.example.com/leave/multipart-mixed-message@example.com">here</a>.</span>
--boundary-0--

--original-boundary
Content-Disposition: attachment; filename=hello.html
Content-Type: text/html; charset="utf-8"

<p>This is an attachment.</p>

--original-boundary--
`)

	wantChansEmpty(t)
}

func TestKnowns(t *testing.T) {

	ul.CreateList("knowns@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel

	list, _ := ul.Lists.GetList(mustParse("knowns@example.com"))
	ul.Lists.AddKnowns(list, []*mailutil.Addr{mustParse("bob@example.com"), mustParse("carol@example.com"), mustParse("known@example.com")})

	mustTransactOne("some_envelope@example.com", []string{"knowns@example.com"},
		`From: known@example.com
To: knowns@example.com
Subject: Hi

Hello`)

	wantMessage(t, "knowns+bounces@example.com", []string{"alice@example.com"},
		`From: "known via List" <knowns@example.com>
List-Id: "List" <knowns@example.com>
List-Post: <mailto:knowns@example.com>
List-Unsubscribe: <mailto:knowns@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <known@example.com>
Subject: [List] Hi
To: knowns@example.com

Hello

----
You can leave the mailing list "List" here: https://lists.example.com/leave/knowns@example.com`)

	wantKnowns := []string{
		"bob@example.com",
		"carol@example.com",
		"known@example.com",
	}

	knowns, err := ul.Lists.Knowns(list)
	if err != nil {
		t.Fatal(err)
	}

	if len(wantKnowns) != len(knowns) {
		t.Fatalf("got %d knowns, want %d", len(knowns), len(wantKnowns))
	}

	for i := range wantKnowns {
		if knowns[i] != wantKnowns[i] {
			t.Fatalf("got %s, want %s", knowns[i], wantKnowns[i])
		}
	}

	wantChansEmpty(t)
}

func TestMembers(t *testing.T) {

	ul.CreateList("members@example.com", "List", "alice@example.com", "testing")

	<-messageChannel // welcome alice
	<-gdprChannel    // welcome alice

	list, _ := ul.Lists.GetList(mustParse("members@example.com"))
	ul.Lists.Update(list, "List", false, false, ulist.Reject, ulist.Pass, ulist.Reject, ulist.Reject) // members only
	ul.AddMembers(
		list,
		false, // sendWelcome
		[]*mailutil.Addr{mustParse("bob@example.com"), mustParse("carol@example.com"), mustParse("dave@example.com")},
		false,     // receive
		false,     // moderate
		false,     // notify
		false,     // admin
		false,     // bounces
		"testing", // reason
	)

	wantGDPREvent(t, `bob@example.com joined the list members@example.com, reason: testing
	carol@example.com joined the list members@example.com, reason: testing
	dave@example.com joined the list members@example.com, reason: testing`)

	mustTransactOne("some_envelope@example.com", []string{"members@example.com"},
		`From: dave@example.com
To: members@example.com
Subject: Hi

Hello`)

	wantMessage(t, "members+bounces@example.com", []string{"alice@example.com"},
		`From: "dave via List" <members@example.com>
List-Id: "List" <members@example.com>
List-Post: <mailto:members@example.com>
List-Unsubscribe: <mailto:members@example.com?subject=leave>
Message-Id: <message-id@example.com>
Reply-To: <dave@example.com>
Subject: [List] Hi
To: members@example.com

Hello

----
You can leave the mailing list "List" here: https://lists.example.com/leave/members@example.com`)

	members, err := ul.Lists.Members(list)
	if err != nil {
		t.Fatal(err)
	}

	wantListInfo := ulist.ListInfo{
		ID: 18,
		Addr: mailutil.Addr{
			Display: "List",
			Local:   "members",
			Domain:  "example.com",
		},
	}

	wantMembers := []ulist.Membership{
		ulist.Membership{
			ListInfo:      wantListInfo,
			MemberAddress: "alice@example.com",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
			Bounces:       true,
		},
		ulist.Membership{
			ListInfo:      wantListInfo,
			MemberAddress: "bob@example.com",
			Receive:       false,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
			Bounces:       false,
		},
		ulist.Membership{
			ListInfo:      wantListInfo,
			MemberAddress: "carol@example.com",
			Receive:       false,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
			Bounces:       false,
		},
		ulist.Membership{
			ListInfo:      wantListInfo,
			MemberAddress: "dave@example.com",
			Receive:       false,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
			Bounces:       false,
		},
	}

	if len(wantMembers) != len(members) {
		t.Fatalf("got %d members, want %d", len(members), len(wantMembers))
	}

	for i := range wantMembers {
		if members[i] != wantMembers[i] {
			t.Fatalf("got %+v, want %+v", members[i], wantMembers[i])
		}
	}

	wantChansEmpty(t)
}
