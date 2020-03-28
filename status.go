package main

import (
	"github.com/wansing/ulist/mailutil"
)

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

func (l *List) GetStatus(address *mailutil.Addr) ([]Status, error) {

	var s = []Status{}

	membership, err := l.GetMember(address)
	if err != nil {
		return nil, err
	}
	if membership != nil {
		if membership.Moderate {
			s = append(s, Moderator)
		} else {
			s = append(s, Member)
		}
	}

	isKnown, err := l.IsKnown(address.RFC5322AddrSpec())
	if err != nil {
		return nil, err
	}
	if isKnown {
		s = append(s, Known)
	}

	return s, nil
}
