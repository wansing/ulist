package main

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/util"
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

// db should be initialized at this point
func sockmapsrv(lmtpSock, socketmapSock string) {

	var absLMTPSock string
	if filepath.IsAbs(lmtpSock) {
		absLMTPSock = lmtpSock
	} else {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("error getting working directory: %v", err)
		}
		absLMTPSock = filepath.Join(wd, lmtpSock)
	}

	// listen to socket

	_ = util.RemoveSocket(socketmapSock) // remove old socket

	listener, err := net.Listen("unix", socketmapSock)
	if err != nil {
		log.Printf("error creating socket: %v", err)
		return
	}
	defer listener.Close() // removes the socket file

	_ = os.Chmod(socketmapSock, os.ModePerm) // chmod 777, so people can connect to the listener

	log.Printf("socketmap listener: %s", socketmapSock)

	// accept connections

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("sockmap: error accepting connection: %v", err)
		}
		go func(conn net.Conn) {
			defer conn.Close()
			for { // postmap transmits multiple addresses in one connection, waiting for a response after each
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
					conn.Write(util.EncodeNetstring("TEMP error reading from connection"))
					break
				}

				data, err := util.DecodeNetstring(input)
				if err != nil {
					log.Printf("sockmap: error decoding netstring: %v", err)
					conn.Write(util.EncodeNetstring("PERM error decoding netstring"))
					continue
				}

				var key string
				if fields := strings.SplitN(data, " ", 2); len(fields) >= 2 {
					key = fields[1] // ignore 0-th field (name of the socketmap)
				} else {
					log.Printf("sockmap: malformed request: %s", data)
					conn.Write(util.EncodeNetstring("PERM malformed request"))
					continue
				}

				if key == "*" { // postfix tries asterisk first
					conn.Write(util.EncodeNetstring("NOTFOUND "))
					continue
				}

				listAddr, err := mailutil.ParseAddress(key)
				if err != nil {
					log.Printf("sockmap: request %s has malformed key %s: %v", data, key, err)
					conn.Write(util.EncodeNetstring("NOTFOUND ")) // postmap considers the whole transaction failed on both TEMP and PERM errors, so we avoid them here
					continue
				}

				if exists, err := db.IsList(listAddr); err == nil {
					if exists {
						conn.Write(util.EncodeNetstring("OK lmtp:unix:" + absLMTPSock))
					} else {
						conn.Write(util.EncodeNetstring("NOTFOUND "))
					}
				} else {
					log.Printf("sockmap: database error: %v", err)
					conn.Write(util.EncodeNetstring("TEMP database error"))
				}
			}

		}(conn)
	}
}
