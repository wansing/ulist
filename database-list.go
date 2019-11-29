package main

import (
	"database/sql"
	"sort"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func (l *List) membersWhere(condition string) ([]Membership, error) {

	rows, err := Db.Query("SELECT l.name, m.address, m.receive, m.moderate, m.notify, m.admin FROM list l, member m WHERE l.address = ? AND l.id = m.list "+condition+" ORDER BY m.address", l.Address)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	members := []Membership{}
	for rows.Next() {
		m := Membership{}
		m.ListInfo.Address = l.Address
		rows.Scan(&m.ListInfo.Name, &m.MemberAddress, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		members = append(members, m)
	}

	sort.Slice(members, func(i, j int) bool {
		return members[i].MemberAddress < members[j].MemberAddress
	})

	return members, nil
}

// *List is never nil, error can be sql.ErrNoRows
func GetList(listAddress string) (*List, error) {
	l := &List{}
	return l, Db.getListStmt.QueryRow(listAddress).Scan(&l.Address, &l.Id, &l.Name, &l.HMACKey, &l.PublicSignup, &l.HideFrom, &l.ActionMod, &l.ActionMember, &l.ActionUnknown, &l.ActionKnown)
}

func (l *List) Update(name string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error {
	_, err := Db.updateListStmt.Exec(name, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown, l.Address)
	return err
}

func (l *List) Admins() ([]Membership, error) {
	return l.membersWhere("m.admin = 1")
}

// *Membership is never nil, error can be sql.ErrNoRows
func (l *List) GetMember(memberAddress string) (*Membership, error) {
	m := &Membership{MemberAddress: memberAddress}
	m.ListInfo.Address = l.Address
	return m, Db.getMemberStmt.QueryRow(l.Address, memberAddress).Scan(&m.Receive, &m.Moderate, &m.Notify, &m.Admin)
}

func (l *List) Members() ([]Membership, error) {
	return l.membersWhere("")
}

func (l *List) Receivers() ([]Membership, error) {
	return l.membersWhere("AND m.receive = 1")
}

func (l *List) Notifieds() ([]Membership, error) {
	return l.membersWhere("AND m.notify = 1")
}

func (l *List) Knowns() ([]string, error) {

	rows, err := Db.getKnownsStmt.Query(l.Address)
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

func (l *List) AddMembers(sendWelcome bool, rawAddresses string, receive, moderate, notify, admin bool, alerter util.Alerter) error {

	addresses, err := mailutil.ExtractAddresses(rawAddresses, BatchLimit, alerter)
	if err != nil {
		return err
	}

	Db.withAddresses(Db.addMemberStmt, alerter, "%d members have been added to the mailing list "+l.Address+".", addresses, l.Id, receive, moderate, notify, admin)

	if sendWelcome {
		l.sendUsersMailTemplate(addresses, "Welcome", welcomeTemplate, alerter)
	}

	return nil
}

func (l *List) UpdateMember(rawAddress string, receive, moderate, notify, admin bool) error {

	address, err := mailutil.ExtractAddress(rawAddress)
	if err != nil {
		return err
	}

	_, err = Db.updateMemberStmt.Exec(receive, moderate, notify, admin, l.Id, address)
	return err
}

func (l *List) RemoveMembers(sendGoodbye bool, rawAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.ExtractAddresses(rawAddresses, BatchLimit, alerter)
	if err != nil {
		return err
	}

	Db.withAddresses(Db.removeMemberStmt, alerter, "%d members have been removed from the mailing list "+l.Address+".", addresses, l.Id)

	if sendGoodbye {
		l.sendUsersMailTemplate(addresses, "Goodbye", goodbyeTemplate, alerter)
	}

	return nil
}

func (l *List) AddKnowns(rawAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.ExtractAddresses(rawAddresses, BatchLimit, alerter)
	if err != nil {
		return err
	}

	Db.withAddresses(Db.addKnownStmt, alerter, "%d known addresses have been added to the mailing list "+l.Address+".", addresses, l.Id)
	return nil
}

func (l *List) IsKnown(rawAddress string) (bool, error) {

	address, err := mailutil.ExtractAddress(rawAddress)
	if err != nil {
		return false, err
	}

	var known bool
	return known, Db.isKnownStmt.QueryRow(l.Address, address).Scan(&known)
}

func (l *List) RemoveKnowns(rawAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.ExtractAddresses(rawAddresses, BatchLimit, alerter)
	if err != nil {
		return err
	}

	Db.withAddresses(Db.removeKnownStmt, alerter, "%d known addresses have been removed from the mailing list "+l.Address+".", addresses, l.Id)
	return nil
}
