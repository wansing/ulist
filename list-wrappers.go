package main

import (
	"database/sql"
	"sort"
	"strings"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
)

func CreateList(listAddress, listName, rawAdminMods string, alerter util.Alerter) error {

	listAddress, err := mailutil.Clean(listAddress)
	if err != nil {
		return err
	}

	hmacKey, err := util.RandomString32()
	if err != nil {
		return err
	}

	if err := Db.CreateList(listAddress, listName, hmacKey); err != nil {
		return err
	}

	list, err := Db.GetList(listAddress)
	if err != nil {
		return err
	}

	return list.AddMembers(false, rawAdminMods, true, true, true, true, alerter) // sendWelcome == false
}

// *List is nil if not found
// error is never sql.ErrNoRows
func GetList(listAddress string) (list *List, err error) {
	list, err = Db.GetList(listAddress)
	if err == sql.ErrNoRows {
		list = nil
		err = nil
	}
	return
}

func (l *List) Update(name string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error {
	name = strings.TrimSpace(name)
	return Db.UpdateList(l.Address, name, publicSignup, hideFrom, actionMod, actionMember, actionKnown, actionUnknown)
}

func (l *List) Admins() ([]Membership, error) {
	return Db.Admins(l.Address)
}

func (l *List) Members() ([]Membership, error) {

	m, err := Db.Members(l.Address)
	if err != nil {
		return nil, err
	}

	sort.Slice(m, func(i, j int) bool {
		return m[i].MemberAddress < m[j].MemberAddress
	})

	return m, nil
}

func (l *List) Receivers() ([]Membership, error) {
	return Db.Receivers(l.Address)
}

func (l *List) Notifieds() ([]Membership, error) {
	return Db.Notifieds(l.Address)
}

func (l *List) Knowns() ([]string, error) {

	k, err := Db.Knowns(l.Address)
	if err != nil {
		return nil, err
	}

	sort.Strings(k)
	return k, nil
}

// *Membership is never nil
func (l *List) GetMember(address string) (m *Membership, isMember bool, err error) {
	m, err = Db.GetMember(l.Address, address)
	switch err {
	case nil:
		isMember = true
	case sql.ErrNoRows:
		err = nil // isMember is still false
	}
	return
}

// Clean addresses here, not in the database code.

// sets receive true, moderate notify admin false
func (l *List) AddMember(sendWelcome bool, uncleanedAddress string, alerter util.Alerter) error {

	memberAddress, err := mailutil.Clean(uncleanedAddress)
	if err != nil {
		return err
	}

	if err := Db.AddMember(l.Address, memberAddress, true, false, false, false); err != nil {
		return err
	}

	if sendWelcome {
		l.sendUsersMailTemplate([]string{memberAddress}, "Welcome", welcomeTemplate, alerter)
	}

	return nil
}

func (l *List) AddMembers(sendWelcome bool, uncleanedAddresses string, receive, moderate, notify, admin bool, alerter util.Alerter) error {

	addresses, err := mailutil.Cleans(uncleanedAddresses, DatabaseBatchLimit, alerter)
	if err != nil {
		return err
	}

	if err = Db.AddMembers(l.Address, addresses, receive, moderate, notify, admin, alerter); err != nil {
		return err
	}

	if sendWelcome {
		l.sendUsersMailTemplate(addresses, "Welcome", welcomeTemplate, alerter)
	}

	return nil
}

func (l *List) UpdateMember(uncleanedAddress string, receive, moderate, notify, admin bool) error {

	address, err := mailutil.Clean(uncleanedAddress)
	if err != nil {
		return err
	}

	return Db.UpdateMember(l.Address, address, receive, moderate, notify, admin)
}

func (l *List) RemoveMember(sendGoodbye bool, uncleanedAddress string, alerter util.Alerter) error {

	address, err := mailutil.Clean(uncleanedAddress)
	if err != nil {
		return err
	}

	if err := Db.RemoveMember(l.Address, address); err != nil {
		return err
	}

	if sendGoodbye {
		l.sendUsersMailTemplate([]string{address}, "Goodbye", goodbyeTemplate, alerter)
	}

	return nil
}

func (l *List) RemoveMembers(sendGoodbye bool, uncleanedAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.Cleans(uncleanedAddresses, DatabaseBatchLimit, alerter)
	if err != nil {
		return err
	}

	if err = Db.RemoveMembers(l.Address, addresses, alerter); err != nil {
		return err
	}

	if sendGoodbye {
		l.sendUsersMailTemplate(addresses, "Goodbye", goodbyeTemplate, alerter)
	}

	return nil
}

func (l *List) AddKnowns(uncleanedAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.Cleans(uncleanedAddresses, DatabaseBatchLimit, alerter)
	if err != nil {
		return err
	}

	return Db.AddKnowns(l.Address, addresses, alerter)
}

func (l *List) IsKnown(uncleanedAddress string) (bool, error) {

	address, err := mailutil.Clean(uncleanedAddress)
	if err != nil {
		return false, err
	}

	return Db.IsKnown(l.Address, address)
}

func (l *List) RemoveKnowns(uncleanedAddresses string, alerter util.Alerter) error {

	addresses, err := mailutil.Cleans(uncleanedAddresses, DatabaseBatchLimit, alerter)
	if err != nil {
		return err
	}

	return Db.RemoveKnowns(l.Address, addresses, alerter)
}
