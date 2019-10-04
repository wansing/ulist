package main

type Status int

const (
	Unknown Status = iota // default
	Known
	Member
	Moderator // max
)

func (l *List) GetStatus(address string) (Status, error) {

	m, isMember, err := l.GetMember(address)
	if err != nil {
		return Unknown, err
	}
	if m.Moderate {
		return Moderator, nil
	}
	if isMember {
		return Member, nil
	}

	isKnown, err := l.IsKnown(address)
	if err != nil {
		return Unknown, err
	}
	if isKnown {
		return Known, nil
	}

	return Unknown, nil
}
