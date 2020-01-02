package main

import "database/sql"

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
