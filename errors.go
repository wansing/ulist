package main

import (
	"github.com/emersion/go-smtp" // not to be confused with golang's net/smtp
)

// Some RCPT error codes in SMTP
//
// 4xx Temporary errors
// 421 Service is unavailable due to a connection problem
// 450 Requested mail action not taken: mailbox unavailable
// 451 Requested action aborted: local error in processing
// 452 Requested action not taken: insufficient system storage
//
// 5xx Permanent errors
// 550 Requested action not taken: mailbox unavailable
// 552 Requested mail action aborted: exceeded storage allocation
// 554 Transaction failed, maybe spam/blacklisted or

var SMTPErrUserNotExist = &smtp.SMTPError{
	Code:         550,
	EnhancedCode: smtp.EnhancedCodeNotSet,
	Message:      "User not found",
}

func SMTPErr(code int, message string) error {
	return &smtp.SMTPError{
		Code:         code,
		EnhancedCode: smtp.EnhancedCodeNotSet,
		Message:      message,
	}
}

func SMTPWrapErr(code int, message string, err error) error {
	if err == nil {
		return nil
	}
	return SMTPErr(code, message+": "+err.Error())
}
