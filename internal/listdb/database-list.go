package listdb

import (
	"bytes"
	"database/sql"
	"fmt"
	"sort"
	"strings"

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

func (list *List) AddMember(sendWelcome bool, addr *mailutil.Addr, receive, moderate, notify, admin bool, reason string) error {
	var err = &util.Err{}
	list.AddMembers(sendWelcome, []*mailutil.Addr{addr}, receive, moderate, notify, admin, reason, err)
	delete(sentJoinCheckbacks, addr.RFC5322AddrSpec())
	return err.Last
}

func (list *List) AddMembers(sendWelcome bool, addrs []*mailutil.Addr, receive, moderate, notify, admin bool, reason string, alerter util.Alerter) {

	affectedRows := list.withListAndAddresses(alerter, list.db.addMemberStmt, list.Id, addrs, receive, moderate, notify, admin)
	if affectedRows == 0 {
		return
	} else {
		alerter.Successf("%d members have been added to the mailing list %s", affectedRows, list.RFC5322AddrSpec())
	}

	var gdprEvent = &strings.Builder{}
	for _, addr := range addrs {
		fmt.Fprintf(gdprEvent, "%s joined the list %s, reason: %s\n", addr, list, reason)
	}
	if err := gdprLogger.Printf("%s", gdprEvent); err != nil {
		alerter.Alertf("error writing join events to GDPR logger: ", err)
	}

	if sendWelcome {

		var data = struct {
			Footer      string
			ListAddress string
		}{
			Footer:      list.plainFooter(),
			ListAddress: list.RFC5322AddrSpec(),
		}

		var body = &bytes.Buffer{}
		if err := signoffJoinTemplate.Execute(body, data); err != nil {
			alerter.Alertf("error executing email template: %v", err)
			return
		}

		for _, addr := range addrs {
			if err := list.Notify(addr.RFC5322AddrSpec(), "Welcome", bytes.NewReader(body.Bytes())); err != nil { // NewReader is important, else the Buffer would be consumed
				alerter.Alertf("error sending welcome to %s: %v", addr, err)
			}
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

func (list *List) RemoveMember(sendGoodbye bool, addr *mailutil.Addr, reason string) error {
	var err = &util.Err{}
	list.RemoveMembers(sendGoodbye, []*mailutil.Addr{addr}, reason, err)
	delete(sentLeaveCheckbacks, addr.RFC5322AddrSpec())
	return err.Last
}

func (list *List) RemoveMembers(sendGoodbye bool, addrs []*mailutil.Addr, reason string, alerter util.Alerter) {

	affectedRows := list.withListAndAddresses(alerter, list.db.removeMemberStmt, list.Id, addrs)
	if affectedRows == 0 {
		return
	} else {
		alerter.Successf("%d members have been removed from the mailing list %s", affectedRows, list.RFC5322AddrSpec())
	}

	var gdprEvent = &strings.Builder{}
	for _, addr := range addrs {
		fmt.Fprintf(gdprEvent, "%s left the list %s, reason: %s\n", addr, list, reason)
	}
	if err := gdprLogger.Printf("%s", gdprEvent); err != nil {
		alerter.Alertf("error writing leave events to GDPR logger: ", err)
	}

	if sendGoodbye {

		var body = &bytes.Buffer{}
		if err := signoffLeaveTemplate.Execute(body, list.RFC5322AddrSpec()); err != nil {
			alerter.Alertf("error executing email template: %v", err)
			return
		}

		for _, addr := range addrs {
			if err := list.Notify(addr.RFC5322AddrSpec(), "Goodbye", bytes.NewReader(body.Bytes())); err != nil { // NewReader is important, else the Buffer would be consumed
				alerter.Alertf("error sending goodbye to %s: %v", addr, err)
			}
		}
	}
}

func (list *List) AddKnown(addr *mailutil.Addr) error {
	return list.withListAndAddress(list.db.addKnownStmt, list.Id, addr)
}

func (list *List) AddKnowns(addrs []*mailutil.Addr, alerter util.Alerter) {

	affectedRows := list.withListAndAddresses(alerter, list.db.addKnownStmt, list.Id, addrs)
	if affectedRows > 0 {
		alerter.Successf("%d known addresses have been added to the mailing list %s", affectedRows, list.RFC5322AddrSpec())
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

	affectedRows := list.withListAndAddresses(alerter, list.db.removeKnownStmt, list.Id, addrs)
	if affectedRows > 0 {
		alerter.Successf("%d known addresses have been removed from the mailing list %s", affectedRows, list.RFC5322AddrSpec())
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

// Arguments of stmt must be (listId, address, args...).
// A transaction is used because batch inserts in SQLite are very slow without.
func (list *List) withListAndAddresses(alerter util.Alerter, stmt *sql.Stmt, listId int, addrs []*mailutil.Addr, args ...interface{}) (affectedRows int64) {

	tx, err := list.db.Begin()
	if err != nil {
		alerter.Alertf("error starting database transaction: %v", err)
		return
	}

	for _, na := range addrs {

		if list.Equals(na) {
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
