package main

import "net/url"

type Membership struct {
	ListInfo
	MemberAddress string
	Receive       bool
	Moderate      bool
	Notify        bool
	Admin         bool
}

func (m *Membership) EscapeMemberAddress() string {
	return url.QueryEscape(m.MemberAddress)
}
