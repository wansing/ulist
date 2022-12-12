package sockmap

import (
	"net"
	"os/exec"
	"testing"

	"github.com/wansing/ulist/mailutil"
)

func TestPostmap(t *testing.T) {

	const sock = "/tmp/test-socketmap.sock"

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

	srv := NewServer(
		func(addr mailutil.Addr) (bool, error) {
			switch addr.RFC5322AddrSpec() {
			case "postmap-a@example.com":
				return true, nil
			case "postmap-b@example.com":
				return true, nil
			default:
				return false, nil
			}
		},
		"/tmp/lmtp.sock",
	)
	defer srv.Close()

	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(l)

	// postmap -q - socketmap:unix:socketmap.sock:name < addresses.txt

	cmd := exec.Command("postmap", "-q", "-", "socketmap:unix:"+sock+":name")
	stdin, _ := cmd.StdinPipe()
	stdin.Write([]byte(postmapInput))
	stdin.Close()
	result, _ := cmd.Output()

	if string(result) != postmapExpect {
		t.Fatalf("got %s, want %s", result, postmapExpect)
	}
}
