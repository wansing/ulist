package listdb

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

func (list *List) GetStatus(address *mailutil.Addr) ([]Status, error) {

	var s = []Status{}

	membership, err := list.GetMembership(address)
	if err != nil {
		return nil, err
	}
	if membership.Member {
		if membership.Moderate {
			s = append(s, Moderator)
		} else {
			s = append(s, Member)
		}
	}

	isKnown, err := list.IsKnown(address.RFC5322AddrSpec())
	if err != nil {
		return nil, err
	}
	if isKnown {
		s = append(s, Known)
	}

	return s, nil
}
