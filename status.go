package main

import "database/sql"

type Status int

const (
	Known Status = iota
	Member
	Moderator
)

func (s Status) String() string {
	switch s {
	case Known:
		return "known"
	case Member:
		return "member"
	case Moderator:
		return "moderator"
	default:
		return "<unknown>"
	}
}

func (l *List) GetStatus(address string) ([]Status, error) {

	var s = []Status{}

	m, err := l.GetMember(address)
	switch err {
	case nil:
		if m.Moderate {
			s = append(s, Moderator)
		} else {
			s = append(s, Member)
		}
	case sql.ErrNoRows:
		// not a member, go on
	default:
		return nil, err
	}

	isKnown, err := l.IsKnown(address)
	if err != nil {
		return nil, err
	}
	if isKnown {
		s = append(s, Known)
	}

	return s, nil
}
