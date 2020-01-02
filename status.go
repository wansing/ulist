package main

import "database/sql"

type Status int

const (
	Unknown Status = iota // default
	Known
	Member
	Moderator // max
)

// TODO argument address mailutil.Addr?
func (l *List) GetStatus(address string) (Status, error) {

	m, err := l.GetMember(address)
	switch err {
	case nil:
		if m.Moderate {
			return Moderator, nil
		} else {
			return Member, nil
		}
	case sql.ErrNoRows:
		// no member, go on
	default:
		return Unknown, err
	}

	isKnown, err := l.IsKnown(address)
	if err != nil {
		return Unknown, err
	}

	if isKnown {
		return Known, nil
	} else {
		return Unknown, nil
	}
}
