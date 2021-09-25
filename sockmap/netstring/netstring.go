package netstring

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func Decode(netstr []byte) (string, error) {

	fields := strings.SplitN(string(netstr), ":", 2)
	if len(fields) != 2 {
		return "", errors.New("missing colon")
	}

	length, err := strconv.Atoi(fields[0])
	if err != nil {
		return "", err
	}

	result := fields[1]

	if len(result) == 0 {
		return "", errors.New("zero bytes after colon")
	}

	result = result[:len(result)-1]

	if length != len(result) {
		return "", fmt.Errorf("expected %d bytes, got %d", length, len(result))
	}

	return result, nil
}

func Encode(str string) []byte {
	return []byte(fmt.Sprintf("%d:%s,", len(str), str))
}
