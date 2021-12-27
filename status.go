package ulist

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
