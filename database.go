package main

import "github.com/wansing/ulist/util"

// add/remove up to 10000 rows at once
const DatabaseBatchLimit = 10000

type Database interface {
	Close() error

	// multiple lists

	Memberships(memberAddress string) ([]Membership, error) // for web ui

	AllLists() ([]ListInfo, error)    // for web ui
	PublicLists() ([]ListInfo, error) // for web ui

	// single list

	CreateList(address, name, hmacKey string) error
	GetList(listAddress string) (*List, error)
	UpdateList(listAddress, name string, publicSignup, hideFrom bool, actionMod, actionMember, actionKnown, actionUnknown Action) error

	// members

	Admins(listAddress string) ([]Membership, error)    // for bounce forwarding
	Members(listAddress string) ([]Membership, error)   // for web ui
	Receivers(listAddress string) ([]Membership, error) // for forwarding emails
	Notifieds(listAddress string) ([]Membership, error) // for sending notifications

	// knowns

	Knowns(listAddress string) ([]string, error) // for web ui

	// CRUD membership

	AddMember(listAddress, address string, receive, moderate, notify, admin bool) error
	AddMembers(listAddress string, addresses []string, receive, moderate, notify, admin bool, alerter util.Alerter) error
	GetMember(listAddress, address string) (*Membership, error)
	UpdateMember(listAddress, address string, receive, moderate, notify, admin bool) error
	RemoveMember(listAddress, address string) error
	RemoveMembers(listAddress string, addresses []string, alerter util.Alerter) error

	// CRUD known

	AddKnowns(listAddress string, addresses []string, alerter util.Alerter) error
	IsKnown(listAddress, address string) (known bool, err error)
	RemoveKnowns(listAddress string, addresses []string, alerter util.Alerter) error
}
