package main

import (
	"fmt"

	"github.com/emersion/go-smtp"
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
// 554 Transaction failed, maybe spam/blacklisted

var SMTPErrUserNotExist = SMTPErrorf(550, "user not found")

func SMTPErrorf(code int, format string, a ...interface{}) *smtp.SMTPError {
	return &smtp.SMTPError{
		Code:         code,
		EnhancedCode: smtp.EnhancedCodeNotSet,
		Message:      fmt.Sprintf(format, a...),
	}
}
