package main

import (
	"os"
	"os/exec"
	"testing"
)

const sockmapsrvTestDbPath = "/tmp/test-socketmap.sqlite3"
const sockmapsrvTestSocketmap = "/tmp/test-socketmap.sock"

const postmapInput =
`list_a@example.com
list_c@example.com
list_a@example.com
list_b@example.com
list_c@example.com`

const postmapExpect =
`list_a@example.com	lmtp:unix:/tmp/lmtp.sock
list_a@example.com	lmtp:unix:/tmp/lmtp.sock
list_b@example.com	lmtp:unix:/tmp/lmtp.sock
`

func TestPostmap(t *testing.T) {

	os.Remove(sockmapsrvTestDbPath)
	Db, _ = OpenDatabase("sqlite3", sockmapsrvTestDbPath)
	CreateList("list_a@example.com", "A", "", nil)
	CreateList("list_b@example.com", "A", "", nil)

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
