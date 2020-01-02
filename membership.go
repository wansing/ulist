package main

import "net/url"

type Membership struct {
	ListInfo      // not List because we had to fetch all of them from the database in Memberships()
	MemberAddress string
	Receive       bool
	Moderate      bool
	Notify        bool
	Admin         bool
}

func (m *Membership) EscapeMemberAddress() string {
	return url.QueryEscape(m.MemberAddress)
}
