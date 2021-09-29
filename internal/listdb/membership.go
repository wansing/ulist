package listdb

import (
	"database/sql"

	"github.com/wansing/ulist/mailutil"
)

type Membership struct {
	ListInfo      // not List because we had to fetch all of them from the database in Memberships()
	Member        bool
	MemberAddress string
	Receive       bool
	Moderate      bool
	Notify        bool
	Admin         bool
}

func (db *Database) Memberships(member *mailutil.Addr) ([]Membership, error) {

	rows, err := db.getMembershipsStmt.Query(member.RFC5322AddrSpec())
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
