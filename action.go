package main

type Action string

const (
	Mod    Action = "mod"
	Pass   Action = "pass"
	Reject Action = "reject" // mails should be rejected if an error occurred
)

func ParseAction(s string) Action {
	switch s {
	case string(Pass):
		return Pass
	case string(Reject):
		return Reject
	default:
		return Mod
	}
}

// helpers for templates

func (a Action) EqualsMod() bool {
	return a == Mod
}

func (a Action) EqualsPass() bool {
	return a == Pass
}

func (a Action) EqualsReject() bool {
	return a == Reject
}
