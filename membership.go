package main

import (
	"database/sql"
	"net/url"
)

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

func Memberships(memberAddress string) ([]Membership, error) {

	rows, err := Db.getMembershipsStmt.Query(memberAddress)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	defer rows.Close()

	memberships := []Membership{}
	for rows.Next() {
		var m Membership
		rows.Scan(&m.Display, &m.Local, &m.Domain, &m.Receive, &m.Moderate, &m.Notify, &m.Admin)
		memberships = append(memberships, m)
	}

	return memberships, nil
}
