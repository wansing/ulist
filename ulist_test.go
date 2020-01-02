package main

import (
	"database/sql"
	//"io"
	"log"
	"os"
	"testing"

	//"github.com/emersion/go-smtp"
	"github.com/wansing/ulist/mailutil"
)

const testDbPath = "/tmp/ulist-test.sqlite3"

func init() {
	Testmode = true
}

/*
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
}*/

type testAlerter struct{}

func (testAlerter) Alertf(format string, a ...interface{}) {
	log.Printf(format, a...)
}

func (testAlerter) Successf(format string, a ...interface{}) {
	log.Printf(format, a...)
}

func TestCRUD(t *testing.T) {

	var err error

	_ = os.Remove(testDbPath)

	Db, _ = OpenDatabase("sqlite3", testDbPath)
	if err != nil {
		t.Fatal(err)
	}

	// create list

	_, err = CreateList("a@example.com", "A", "admin1@example.com, admin2@example.com", testAlerter{})
	if err != nil {
		t.Fatal(err)
	}

	// load list

	listAddr, _ := mailutil.ParseAddress("a@example.com")

	list, err := GetList(listAddr)
	if err != nil {
		t.Fatal(err)
	}

	// add member

	member1, err := mailutil.ParseAddress("member1@example.net")
	if err != nil {
		t.Fatal(err)
	}

	err = list.AddMember(member1, true, false, false, false)
	if err != nil {
		t.Fatal(err)
	}

	// add members

	member2, err := mailutil.ParseAddress("member2@example.org")
	if err != nil {
		t.Fatal(err)
	}

	member3, err := mailutil.ParseAddress("member3@example.com")
	if err != nil {
		t.Fatal(err)
	}

	list.AddMembers(false, []*mailutil.Addr{member2, member3}, false, true, false, false, testAlerter{})

	// add known

	known1, err := mailutil.ParseAddress("known1@example.org")
	if err != nil {
		t.Fatal(err)
	}

	err = list.AddKnown(known1)
	if err != nil {
		t.Fatal(err)
	}

	// add members

	known2, err := mailutil.ParseAddress("known2@example.com")
	if err != nil {
		t.Fatal(err)
	}

	known3, err := mailutil.ParseAddress("known3@example.net")
	if err != nil {
		t.Fatal(err)
	}

	list.AddKnowns([]*mailutil.Addr{known3, known2}, testAlerter{})

	// get members

	members, err := list.Members()
	if err != nil {
		t.Fatal(err)
	}

	expectedListInfo := ListInfo{
		mailutil.Addr{
			Display: "A",
			Local:   "a",
			Domain:  "example.com",
		},
	}

	expectedMembers := []Membership{
		Membership{
			ListInfo:      expectedListInfo,
			MemberAddress: "admin1@example.com",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectedListInfo,
			MemberAddress: "admin2@example.com",
			Receive:       true,
			Moderate:      true,
			Notify:        true,
			Admin:         true,
		},
		Membership{
			ListInfo:      expectedListInfo,
			MemberAddress: "member1@example.net",
			Receive:       true,
			Moderate:      false,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectedListInfo,
			MemberAddress: "member2@example.org",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
		Membership{
			ListInfo:      expectedListInfo,
			MemberAddress: "member3@example.com",
			Receive:       false,
			Moderate:      true,
			Notify:        false,
			Admin:         false,
		},
	}

	for i := range expectedMembers {
		if members[i] != expectedMembers[i] {
			t.Fatal()
		}
	}

	// get knowns

	expectedKnowns := []string{
		"known1@example.org",
		"known2@example.com",
		"known3@example.net",
	}

	knowns, err := list.Knowns()
	if err != nil {
		t.Fatal(err)
	}

	for i := range expectedKnowns {
		if knowns[i] != expectedKnowns[i] {
			t.Fatal()
		}
	}

	// delete list

	err = list.Delete()
	if err != nil {
		t.Fatal(err)
	}

	// check that list is deleted

	list, err = GetList(listAddr)
	if err != sql.ErrNoRows {
		t.Fatalf("List has not been deleted, expected %v, got %v", sql.ErrNoRows, err)
	}
}
