package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"errors"
)

type ListStub struct {
	ListInfo
	Id      int
	HMACKey []byte // [32]byte would require check when reading from database
}

func (l *ListStub) HMAC(address string) ([]byte, error) {

	if len(l.HMACKey) == 0 {
		return nil, errors.New("[ListStub] HMACKey is empty")
	}

	if bytes.Compare(l.HMACKey, make([]byte, 32)) == 0 {
		return nil, errors.New("[ListStub] HMACKey is all zeroes")
	}

	mac := hmac.New(sha512.New, l.HMACKey)
	mac.Write([]byte(l.Address))
	mac.Write([]byte{0}) // separator
	mac.Write([]byte(address))

	return mac.Sum(nil), nil
}
