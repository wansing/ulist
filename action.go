package main

import (
	"database/sql/driver"
	"errors"
)

type Action int

var ErrUnknownActionString = errors.New("unknown action string")

const (
	// ordered, order is required for list.GetAction
	Reject Action = iota
	Mod
	Pass
)

// implement sql.Scanner
func (a *Action) Scan(value interface{}) (err error) {
	*a, err = ParseAction(value.(string))
	return
}

// implement sql/driver.Valuer
func (a Action) Value() (driver.Value, error) {
	return a.String(), nil
}

func ParseAction(s string) (Action, error) {
	switch s {
	case Reject.String():
		return Reject, nil
	case Mod.String():
		return Mod, nil
	case Pass.String():
		return Pass, nil
	default:
		return Mod, ErrUnknownActionString
	}
}

func (a Action) String() string {
	switch a {
	case Reject:
		return "reject"
	case Mod:
		return "mod"
	case Pass:
		return "pass"
	default:
		return "<unknown>"
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
