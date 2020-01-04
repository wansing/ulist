package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"sort"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

// *List is never nil, error can be sql.ErrNoRows
func GetList(listAddress *mailutil.Addr) (*List, error) {
	l := &List{}
	l.Local = listAddress.Local
	l.Domain = listAddress.Domain
	return l, Db.getListStmt.QueryRow(listAddress.Local, listAddress.Domain).Scan(&l.Id, &l.Display, &l.HMACKey, &l.PublicSignup, &l.HideFrom, &l.ActionMod, &l.ActionMember, &l.ActionUnknown, &l.ActionKnown)
}

func (l *List) Update(display string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error {
	_, err := Db.updateListStmt.Exec(display, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown, l.Id)
	return err
}

func (l *List) Admins() ([]Membership, error) {
	return l.membersWhere(Db.getAdminsStmt)
}

// *Membership is never nil, error can be sql.ErrNoRows
func (l *List) GetMember(memberAddress string) (*Membership, error) {
	m := &Membership{
		MemberAddress: memberAddress,
		ListInfo:      l.ListInfo,
	}
	return m, Db.getMemberStmt.QueryRow(l.Id, memberAddress).Scan(&m.Receive, &m.Moderate, &m.Notify, &m.Admin)
}

func (l *List) Members() ([]Membership, error) {
	return l.membersWhere(Db.getMembersStmt)
}

func (l *List) Notifieds() ([]Membership, error) {
	return l.membersWhere(Db.getNotifiedsStmt)
}

func (l *List) Receivers() ([]Membership, error) {
	return l.membersWhere(Db.getReceiversStmt)
}

func (l *List) Knowns() ([]string, error) {

	rows, err := Db.getKnownsStmt.Query(l.Id)
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

// always sends welcome email
func (l *List) AddMember(addr *mailutil.Addr, receive, moderate, notify, admin bool) error {

	body := &bytes.Buffer{}
	if err := welcomeTemplate.Execute(body, l.RFC5322AddrSpec()); err != nil {
		return err
	}

	if err := l.withListAndAddress(Db.addMemberStmt, l.Id, addr, receive, moderate, notify, admin); err != nil {
		return err
	}

	_ = l.sendUserMail(addr.RFC5322AddrSpec(), "Welcome", body) // ignore errors here
	return nil
}

func (l *List) AddMembers(sendWelcome bool, addrs []*mailutil.Addr, receive, moderate, notify, admin bool, alerter util.Alerter) {

	affectedRows := l.withListAndAddresses(alerter, Db.addMemberStmt, l.Id, addrs, receive, moderate, notify, admin)
	if affectedRows > 0 {
		alerter.Successf("%d members have been added to the mailing list %s", affectedRows, l.RFC5322AddrSpec())
	}

	if sendWelcome {
		l.sendUsersMailTemplate(addrs, "Welcome", welcomeTemplate, alerter)
	}
}

func (l *List) UpdateMember(rawAddress string, receive, moderate, notify, admin bool) error {

	addr, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return err
	}

	_, err = Db.updateMemberStmt.Exec(receive, moderate, notify, admin, l.Id, addr.RFC5322AddrSpec())
	return err
}

func (l *List) RemoveMember(addr *mailutil.Addr) error {

	body := &bytes.Buffer{}
	if err := goodbyeTemplate.Execute(body, l.RFC5322AddrSpec()); err != nil {
		return err
	}

	if err := l.withListAndAddress(Db.removeMemberStmt, l.Id, addr); err != nil {
		return err
	}

	_ = l.sendUserMail(addr.RFC5322AddrSpec(), "Goodbye", body) // ignore errors here
	return nil
}

func (l *List) RemoveMembers(sendGoodbye bool, addrs []*mailutil.Addr, alerter util.Alerter) {

	affectedRows := l.withListAndAddresses(alerter, Db.removeMemberStmt, l.Id, addrs)
	if affectedRows > 0 {
		alerter.Successf("%d members have been removed from the mailing list %s", affectedRows, l.RFC5322AddrSpec())
	}

	if sendGoodbye {
		l.sendUsersMailTemplate(addrs, "Goodbye", goodbyeTemplate, alerter)
	}
}

func (l *List) AddKnown(addr *mailutil.Addr) error {
	return l.withListAndAddress(Db.addKnownStmt, l.Id, addr)
}

func (l *List) AddKnowns(addrs []*mailutil.Addr, alerter util.Alerter) {

	affectedRows := l.withListAndAddresses(alerter, Db.addKnownStmt, l.Id, addrs)
	if affectedRows > 0 {
		alerter.Successf("%d known addresses have been added to the mailing list %s", affectedRows, l.RFC5322AddrSpec())
	}
}

func (l *List) IsKnown(rawAddress string) (bool, error) {

	address, err := mailutil.ParseAddress(rawAddress)
	if err != nil {
		return false, err
	}

	var known bool
	return known, Db.isKnownStmt.QueryRow(l.Id, address.RFC5322AddrSpec()).Scan(&known)
}

func (l *List) RemoveKnowns(addrs []*mailutil.Addr, alerter util.Alerter) {

	affectedRows := l.withListAndAddresses(alerter, Db.removeKnownStmt, l.Id, addrs)
	if affectedRows > 0 {
		alerter.Successf("%d known addresses have been removed from the mailing list %s", affectedRows, l.RFC5322AddrSpec())
	}
}

func (l *List) Delete() error {

	tx, err := Db.Begin()
	if err != nil {
		return err
	}

	_, err = tx.Stmt(Db.removeListStmt).Exec(l.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	_, err = tx.Stmt(Db.removeListKnownsStmt).Exec(l.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	_, err = tx.Stmt(Db.removeListMembersStmt).Exec(l.Id)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (l *List) membersWhere(stmt *sql.Stmt) ([]Membership, error) {

	rows, err := stmt.Query(l.Id)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []Membership{}
	for rows.Next() {
		m := Membership{}
		m.ListInfo = l.ListInfo
		rows.Scan(&m.MemberAddress, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		members = append(members, m)
	}

	sort.Slice(members, func(i, j int) bool {
		return members[i].MemberAddress < members[j].MemberAddress
	})

	return members, nil
}

// Arguments of stmt must be (listId, address, args...).
func (l *List) withListAndAddress(stmt *sql.Stmt, listId int, addr *mailutil.Addr, args ...interface{}) error {

	if l.Equals(addr) {
		return fmt.Errorf("%s is the list address", addr.RFC5322AddrSpec())
	}

	_, err := stmt.Exec(append([]interface{}{listId, addr.RFC5322AddrSpec()}, args...)...)
	return err
}

// Arguments of stmt must be (listId, address, args...).
// A transaction is used because batch inserts in SQLite are very slow without.
func (l *List) withListAndAddresses(alerter util.Alerter, stmt *sql.Stmt, listId int, addrs []*mailutil.Addr, args ...interface{}) (affectedRows int64) {

	tx, err := Db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	for _, na := range addrs {

		if l.Equals(na) {
			alerter.Alertf("skipped %s because it's the list address", na.RFC5322AddrSpec())
			continue
		}

		result, err := stmt.Exec(append([]interface{}{listId, na.RFC5322AddrSpec()}, args...)...)
		if err != nil {
			alerter.Alertf("error executing database statement: %v", err)
			continue
		}

		if ra, err := result.RowsAffected(); err == nil {
			affectedRows += ra
		}
	}

	if err := tx.Commit(); err != nil {
		alerter.Alertf("error committing database transaction: %v", err)
		return
	}

	return
}
