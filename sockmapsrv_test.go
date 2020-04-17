package main

import (
	"os/exec"
	"testing"
)

const sockmapsrvTestSocketmap = "/tmp/test-socketmap.sock"

const postmapInput = `postmap-a@example.com
postmap-c@example.com
*
postmap-a@example.com
postmap-b@example.com
postmap-c@example.com`

const postmapExpect = `postmap-a@example.com	lmtp:unix:/tmp/lmtp.sock
postmap-a@example.com	lmtp:unix:/tmp/lmtp.sock
postmap-b@example.com	lmtp:unix:/tmp/lmtp.sock
`

func TestPostmap(t *testing.T) {

	db.CreateList("postmap-a@example.com", "A", "", "testing", nil)
	db.CreateList("postmap-b@example.com", "A", "", "testing", nil)

	go sockmapsrv("/tmp/lmtp.sock", sockmapsrvTestSocketmap)

	// postmap -q - socketmap:unix:socketmap.sock:name < addresses.txt

	cmd := exec.Command("postmap", "-q", "-", "socketmap:unix:"+sockmapsrvTestSocketmap+":name")
	stdin, _ := cmd.StdinPipe()
	stdin.Write([]byte(postmapInput))
	stdin.Close()
	result, _ := cmd.Output()

	if string(result) != postmapExpect {
		t.Fatalf("got %s, want %s", result, postmapExpect)
	}
}
