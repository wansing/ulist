package listdb

import (
	"bytes"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/wansing/ulist/internal/listdb/txt"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func (db *Database) IsList(address *mailutil.Addr) (bool, error) {
	var exists bool
	return exists, db.isListStmt.QueryRow(address.Local, address.Domain).Scan(&exists)
}

// *List can be nil, error is never sql.ErrNoRows
func (db *Database) GetList(listAddress *mailutil.Addr) (*List, error) {
	var list = &List{}
	list.db = db
	list.Local = listAddress.Local
	list.Domain = listAddress.Domain
	var err = db.getListStmt.QueryRow(listAddress.Local, listAddress.Domain).Scan(&list.Id, &list.Display, &list.HMACKey, &list.PublicSignup, &list.HideFrom, &list.ActionMod, &list.ActionMember, &list.ActionUnknown, &list.ActionKnown)
	switch err {
	case nil:
		return list, nil
	case sql.ErrNoRows:
		return nil, nil
	default:
		return nil, err
	}
}

func (list *List) Update(display string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error {

	_, err := list.db.updateListStmt.Exec(display, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown, list.Id)
	if err != nil {
		return err
	}

	list.Display = display
	list.PublicSignup = publicSignup
	list.HideFrom = hideFrom
	list.ActionMod = actionMod
	list.ActionMember = actionMember
	list.ActionKnown = actionKnown
	list.ActionUnknown = actionUnknown
	return nil
}

func (list *List) Admins() ([]string, error) {
	return list.membersWhere(list.db.getAdminsStmt)
}

// *Membership can be nil, error is never sql.ErrNoRows
func (list *List) GetMember(addr *mailutil.Addr) (*Membership, error) {
	var receive, moderate, notify, admin bool
	var err = list.db.getMemberStmt.QueryRow(list.Id, addr.RFC5322AddrSpec()).Scan(&receive, &moderate, &notify, &admin)
	switch err {
	case nil:
		return &Membership{
			MemberAddress: addr.RFC5322AddrSpec(),
			ListInfo:      list.ListInfo,
			Receive:       receive,
			Moderate:      moderate,
			Notify:        notify,
			Admin:         admin,
		}, nil
	case sql.ErrNoRows:
		return nil, nil
	default:
		return nil, err
	}
}

// list and addr can be nil
func (list *List) IsMember(addr *mailutil.Addr) (bool, error) {
	if list == nil || addr == nil {
		return false, nil
	}
	membership, err := list.GetMember(addr)
	return membership != nil, err
}

func (list *List) Members() ([]Membership, error) {

	rows, err := list.db.getMembersStmt.Query(list.Id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []Membership{}
	for rows.Next() {
		m := Membership{}
		m.ListInfo = list.ListInfo
		rows.Scan(&m.MemberAddress, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		members = append(members, m)
	}

	return members, nil
}

func (list *List) Notifieds() ([]string, error) {
	return list.membersWhere(list.db.getNotifiedsStmt)
}

func (list *List) Receivers() ([]string, error) {
	return list.membersWhere(list.db.getReceiversStmt)
}

func (list *List) Knowns() ([]string, error) {

	rows, err := list.db.getKnownsStmt.Query(list.Id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	knowns := []string{}
	for rows.Next() {
		var known string
		rows.Scan(&known)
		knowns = append(knowns, known)
	}

	sort.Strings(knowns)

	return knowns, nil
}

func (list *List) AddMember(addr *mailutil.Addr, receive, moderate, notify, admin bool, reason string) error {
	var err = &util.Err{}
	list.AddMembers(true, []*mailutil.Addr{addr}, receive, moderate, notify, admin, reason, err)
	delete(sentJoinCheckbacks, addr.RFC5322AddrSpec())
	return err.Last
}

func (list *List) signoffJoinMessage(member *mailutil.Addr) (*bytes.Buffer, error) {
	var buf = &bytes.Buffer{}
	var err = txt.SignoffJoin.Execute(buf, struct {
		Footer      string
		ListAddress string
		MailAddress string
	}{
		Footer:      list.plainFooter(),
		ListAddress: list.RFC5322AddrSpec(),
		MailAddress: member.RFC5322AddrSpec(),
	})
	return buf, err
}

func (list *List) AddMembers(sendWelcome bool, addrs []*mailutil.Addr, receive, moderate, notify, admin bool, reason string, alerter util.Alerter) {

	tx, err := list.db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	var addSuccess int
	var addFailure int
	var addLastErr error
	var tmplFailure int
	var tmplLastErr error
	var sendSuccess int
	var sendFailure int
	var sendLastErr error

	var gdprEvent = &strings.Builder{}

	for _, addr := range addrs {

		if list.Equals(addr) {
			alerter.Alertf("skipping %s because it's the list address", addr.RFC5322AddrSpec())
			continue
		}

		_, err := list.db.addMemberStmt.Exec(list.Id, addr.RFC5322AddrSpec(), receive, moderate, notify, admin)
		if err == nil {
			addSuccess++
		} else {
			addFailure++
			addLastErr = err
			continue
		}

		if gdprEvent.Len() > 0 {
			gdprEvent.WriteString("\t")
		}
		fmt.Fprintf(gdprEvent, "%s joined the list %s, reason: %s\n", addr, list, reason)

		if sendWelcome {

			welcomeBody, err := list.signoffJoinMessage(addr)
			if err != nil {
				tmplFailure++
				tmplLastErr = err
				continue
			}

			err = list.Notify(addr.RFC5322AddrSpec(), "Welcome", welcomeBody) // reading welcomeBody consumes the buffer
			if err == nil {
				sendSuccess++
			} else {
				sendFailure++
				sendLastErr = err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		alerter.Alertf("error committing database transaction: %v", err)
		return
	}

	if addSuccess > 0 {
		alerter.Successf("%d members have been added to the mailing list %s", addSuccess, list.RFC5322AddrSpec())
	}

	if addFailure > 0 {
		alerter.Alertf("could not add %d members to the mailing list %s, last error: %s", addFailure, list.RFC5322AddrSpec(), addLastErr)
	}

	if tmplFailure > 0 {
		alerter.Alertf("could not parse %d templates, last error: %s", tmplFailure, tmplLastErr)
	}

	if sendSuccess > 0 {
		alerter.Successf("%d members have been notified", sendSuccess)
	}

	if sendFailure > 0 {
		alerter.Alertf("could not notify %d members, last error: %s", sendFailure, sendLastErr)
	}

	if gdprEvent.Len() > 0 {
		if err := gdprLogger.Printf("%s", gdprEvent); err != nil {
			alerter.Alertf("error writing join events to GDPR logger: ", err)
		}
	}
}

func (list *List) UpdateMember(rawAddress string, receive, moderate, notify, admin bool) error {

	addr, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return err
	}

	_, err = list.db.updateMemberStmt.Exec(receive, moderate, notify, admin, list.Id, addr.RFC5322AddrSpec())
	return err
}

func (list *List) RemoveMember(addr *mailutil.Addr, reason string) error {
	var err = &util.Err{}
	list.RemoveMembers(true, []*mailutil.Addr{addr}, reason, err)
	delete(sentLeaveCheckbacks, addr.RFC5322AddrSpec())
	return err.Last
}

func (list *List) signoffLeaveMessage() ([]byte, error) {
	var buf = &bytes.Buffer{}
	var err = txt.SignoffLeave.Execute(buf, list.RFC5322AddrSpec())
	return buf.Bytes(), err
}

func (list *List) RemoveMembers(sendGoodbye bool, addrs []*mailutil.Addr, reason string, alerter util.Alerter) {

	var goodbyeBody []byte
	var err error

	if sendGoodbye {
		goodbyeBody, err = list.signoffLeaveMessage()
		if err != nil {
			alerter.Alertf("error executing email template: %v", err)
			return
		}
	}

	tx, err := list.db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	var removeSuccess int
	var removeFailure int
	var removeLastErr error
	var sendSuccess int
	var sendFailure int
	var sendLastErr error

	var gdprEvent = &strings.Builder{}

	for _, addr := range addrs {

		_, err := list.db.removeMemberStmt.Exec(list.Id, addr.RFC5322AddrSpec())
		if err == nil {
			removeSuccess++
		} else {
			removeFailure++
			removeLastErr = err
			continue
		}

		if gdprEvent.Len() > 0 {
			gdprEvent.WriteString("\t")
		}
		fmt.Fprintf(gdprEvent, "%s left the list %s, reason: %s\n", addr, list, reason)

		if sendGoodbye {
			err = list.Notify(addr.RFC5322AddrSpec(), "Goodbye", bytes.NewReader(goodbyeBody))
			if err == nil {
				sendSuccess++
			} else {
				sendFailure++
				sendLastErr = err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		alerter.Alertf("error committing database transaction: %v", err)
		return
	}

	if removeSuccess > 0 {
		alerter.Successf("%d members have been removed from the mailing list %s", removeSuccess, list.RFC5322AddrSpec())
	}

	if removeFailure > 0 {
		alerter.Alertf("could not remove %d members from the mailing list %s, last error: %s", removeFailure, list.RFC5322AddrSpec(), removeLastErr)
	}

	if sendSuccess > 0 {
		alerter.Successf("%d members have been notified", sendSuccess)
	}

	if sendFailure > 0 {
		alerter.Alertf("could not notify %d members, last error: %s", sendFailure, sendLastErr)
	}

	if err := gdprLogger.Printf("%s", gdprEvent); err != nil {
		alerter.Alertf("error writing leave events to GDPR logger: ", err)
	}
}

func (list *List) AddKnown(addr *mailutil.Addr) error {
	var err = &util.Err{}
	list.AddKnowns([]*mailutil.Addr{addr}, err)
	return err.Last
}

func (list *List) AddKnowns(addrs []*mailutil.Addr, alerter util.Alerter) {

	tx, err := list.db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	var success int
	var failure int
	var lastErr error

	for _, addr := range addrs {

		if list.Equals(addr) {
			alerter.Alertf("skipping %s because it's the list address", addr.RFC5322AddrSpec())
			continue
		}

		_, err := list.db.addKnownStmt.Exec(list.Id, addr.RFC5322AddrSpec())
		if err == nil {
			success++
		} else {
			failure++
			lastErr = err
		}
	}

	if err := tx.Commit(); err != nil {
		alerter.Alertf("error committing database transaction: %v", err)
		return
	}

	if success > 0 {
		alerter.Successf("%d known addresses have been added to the mailing list %s", success, list.RFC5322AddrSpec())
	}

	if failure > 0 {
		alerter.Alertf("could not add %d known addresses to the mailing list %s, last error: %s", failure, list.RFC5322AddrSpec(), lastErr)
	}
}

func (list *List) IsKnown(rawAddress string) (bool, error) {

	address, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return false, err
	}

	var known bool
	return known, list.db.isKnownStmt.QueryRow(list.Id, address.RFC5322AddrSpec()).Scan(&known)
}

func (list *List) RemoveKnowns(addrs []*mailutil.Addr, alerter util.Alerter) {

	tx, err := list.db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	var success int
	var failure int
	var lastErr error

	for _, addr := range addrs {

		if list.Equals(addr) {
			alerter.Alertf("skipping %s because it's the list address", addr.RFC5322AddrSpec())
			continue
		}

		_, err := list.db.removeKnownStmt.Exec(list.Id, addr.RFC5322AddrSpec())
		if err == nil {
			success++
		} else {
			failure++
			lastErr = err
		}
	}

	if err := tx.Commit(); err != nil {
		alerter.Alertf("error committing database transaction: %v", err)
		return
	}

	if success > 0 {
		alerter.Successf("%d known addresses have been removed from the mailing list %s", success, list.RFC5322AddrSpec())
	}

	if failure > 0 {
		alerter.Alertf("could not remove %d known addresses from the mailing list %s, last error: %s", failure, list.RFC5322AddrSpec(), lastErr)
	}
}

func (list *List) Delete() error {

	tx, err := list.db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Stmt(list.db.removeListStmt).Exec(list.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	_, err = tx.Stmt(list.db.removeListKnownsStmt).Exec(list.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	_, err = tx.Stmt(list.db.removeListMembersStmt).Exec(list.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (list *List) membersWhere(stmt *sql.Stmt) ([]string, error) {

	rows, err := stmt.Query(list.Id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []string{}
	for rows.Next() {
		var m string
		rows.Scan(&m)
		members = append(members, m)
	}

	return members, nil
}

// Arguments of stmt must be (listId, address, args...).
func (list *List) withListAndAddress(stmt *sql.Stmt, listId int, addr *mailutil.Addr, args ...interface{}) error {

	if list.Equals(addr) {
		return fmt.Errorf("%s is the list address", addr.RFC5322AddrSpec())
	}

	_, err := stmt.Exec(append([]interface{}{listId, addr.RFC5322AddrSpec()}, args...)...)
	return err
}
