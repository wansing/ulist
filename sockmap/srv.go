package sockmap

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/sockmap/netstring"
)

// http://www.postfix.org/postconf.5.html#transport_maps
//
// transport_maps (default: empty)
//
// Optional lookup tables with mappings from recipient address to (message delivery transport, next-hop destination). See transport(5) for details.
// Specify zero or more "type:table" lookup tables, separated by whitespace or comma. Tables will be searched in the specified order until a match is found.

// Test it:
//
// postmap -q - socketmap:unix:socketmap.sock:name < /tmp/addresses
// foo@example.com	lmtp:unix:/tmp/lmtp.sock
// list@example.com	lmtp:unix:/tmp/lmtp.sock
//
// The name of the socketmap (here: "name") is ignored.

type Server struct {
	Exists        func(addr *mailutil.Addr) (bool, error)
	ExistResponse string // LMTP socket path

	done      chan struct{}
	locker    sync.Mutex
	listeners []net.Listener
	conns     map[net.Conn]struct{}
}

func NewServer(exists func(addr *mailutil.Addr) (bool, error), existResponse string) *Server {
	return &Server{
		Exists:        exists,
		ExistResponse: existResponse,
		done:          make(chan struct{}, 1),
		conns:         make(map[net.Conn]struct{}),
	}
}

// Serve accepts incoming connections on the Listener l.
// For each email address for which srv.Exists returns true, srv.ExistResponse is returned.
// The reply argument will usually be the absolute path to your LMTP socket.
func (srv *Server) Serve(l net.Listener) error {
	srv.locker.Lock()
	srv.listeners = append(srv.listeners, l)
	srv.locker.Unlock()

	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-srv.done:
				// we called Close()
				return nil
			default:
				return fmt.Errorf("accepting: %w", err)
			}
		}
		go srv.handleConn(conn)
	}
}

func (srv *Server) handleConn(conn net.Conn) {
	srv.locker.Lock()
	srv.conns[conn] = struct{}{}
	srv.locker.Unlock()

	defer func() {
		conn.Close()

		srv.locker.Lock()
		delete(srv.conns, conn)
		srv.locker.Unlock()
	}()

	// postmap transmits multiple addresses in one connection, waiting for a response after each
	for {
		conn.SetDeadline(time.Now().Add(10 * time.Second))

		input := make([]byte, 500) // RFC 5321: max email address length is 254 or 320
		n, err := conn.Read(input)
		input = input[:n]
		if err == io.EOF {
			break
		}
		if err != nil {
			if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
				break // don't log anything
			}
			log.Printf("sockmap: error reading from connection: %v", err)
			conn.Write(netstring.Encode("TEMP error reading from connection"))
			break
		}

		data, err := netstring.Decode(input)
		if err != nil {
			log.Printf("sockmap: error decoding netstring: %v", err)
			conn.Write(netstring.Encode("PERM error decoding netstring"))
			continue
		}

		var key string
		if fields := strings.SplitN(data, " ", 2); len(fields) >= 2 {
			key = fields[1] // ignore 0-th field (name of the socketmap)
		} else {
			log.Printf("sockmap: malformed request: %s", data)
			conn.Write(netstring.Encode("PERM malformed request"))
			continue
		}

		if key == "*" { // postfix tries asterisk first
			conn.Write(netstring.Encode("NOTFOUND "))
			continue
		}

		listAddr, err := mailutil.ParseAddress(key)
		if err != nil {
			log.Printf("sockmap: request %s has malformed key %s: %v", data, key, err)
			conn.Write(netstring.Encode("NOTFOUND ")) // postmap considers the whole transaction failed on both TEMP and PERM errors, so we avoid them here
			continue
		}

		if ok, err := srv.Exists(listAddr); err == nil {
			if ok {
				conn.Write(netstring.Encode("OK lmtp:unix:" + srv.ExistResponse))
			} else {
				conn.Write(netstring.Encode("NOTFOUND "))
			}
		} else {
			log.Printf("sockmap: database error: %v", err)
			conn.Write(netstring.Encode("TEMP database error"))
		}
	}
}

// Close immediately closes all active listeners and connections.
// It returns any error returned from closing the server's underlying listener(s).
func (srv *Server) Close() error {
	select {
	case <-srv.done:
		return errors.New("server already closed")
	default:
		close(srv.done)
	}

	var err error
	for _, l := range srv.listeners {
		if lerr := l.Close(); lerr != nil && err == nil {
			err = lerr
		}
	}

	srv.locker.Lock()
	for conn := range srv.conns {
		conn.Close()
	}
	srv.locker.Unlock()

	return err
}
