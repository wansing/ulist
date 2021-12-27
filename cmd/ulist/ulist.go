package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wansing/ulist"
	"github.com/wansing/ulist/auth"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/repos/sqlite"
	"github.com/wansing/ulist/sockmap"
	"github.com/wansing/ulist/util"
	"github.com/wansing/ulist/web"
	"golang.org/x/sys/unix"
)

const WarnFormat = "\033[1;31m%s\033[0m"

func main() {

	log.SetFlags(0) // no log prefixes required, systemd-journald adds them

	// mail flow
	lmtpSock := flag.String("lmtp", "lmtp.sock", "create an LMTP server socket at this `path` and listen for incoming mail")
	socketmapSock := flag.String("socketmap", "", "create a socketmap server socket at this `path`")

	// web interface
	webListen := flag.String("http", "127.0.0.1:8080", "make the web interface available at this ip:port or socket path")
	webUrl := flag.String("weburl", "http://127.0.0.1:8080", "use this `url` in links to the web interface")

	// authentication
	superadmin := flag.String("superadmin", "", "allow the user with this `email` address to create, delete and modify every list through the web interface")
	smtpsAuthPort := flag.Uint("smtps", 0, "connect to localhost:`port` for SMTPS user authentication (first choice)")
	starttlsAuthPort := flag.Uint("starttls", 0, "connect to localhost:`port` for SMTP STARTTLS user authentication")

	// debug
	dummyMode := flag.Bool("dummymode", false, "accept any user credentials and don't send any emails")

	flag.Parse()

	// MTA

	var mta mailutil.MTA = mailutil.Sendmail{}

	// dummymode changes

	if *dummyMode {
		mta = mailutil.DummyMTA{}
		*superadmin = "test@example.com"
		log.Printf(WarnFormat, "ulist runs in dummy mode. Everyone can login as superadmin and no emails are sent.")
	}

	// sockets

	runtimeDir := os.Getenv("RUNTIME_DIRECTORY")
	if runtimeDir == "" {
		runtimeDir = "/run/ulist"
	}
	runtimeDir = filepath.Join("/", runtimeDir) // make absolute

	if !filepath.IsAbs(*lmtpSock) {
		*lmtpSock = filepath.Join(runtimeDir, *lmtpSock)
	}

	if *socketmapSock != "" && !filepath.IsAbs(*socketmapSock) {
		*socketmapSock = filepath.Join(runtimeDir, *socketmapSock)
	}

	// spool dir

	stateDir := os.Getenv("STATE_DIRECTORY")
	if stateDir == "" {
		stateDir = "/var/lib/ulist"
	}
	if *dummyMode {
		stateDir = "/tmp/ulist"
	}

	spoolDir := filepath.Join(stateDir, "spool")
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		log.Fatalf("error creating spool directory: %v", err)
	}

	if unix.Access(spoolDir, unix.W_OK) != nil {
		log.Fatalf("spool directory %s is not writeable", spoolDir)
	}

	log.Printf("spool directory: %s", spoolDir)

	// dbs

	gdprLogger, err := util.NewFileLogger(filepath.Join(stateDir, "gdpr.log"))
	if err != nil {
		log.Fatalf("error creating GDPR logfile: %v", err)
	}

	listDB, err := sqlite.OpenListDB(filepath.Join(stateDir, "lists.sqlite3"))
	if err != nil {
		log.Fatalf("error opening list db: %v", err)
	}
	defer listDB.Close()

	userDB, err := sqlite.OpenUserDB(filepath.Join(stateDir, "users.sqlite3"))
	if err != nil {
		log.Fatalf("error opening user db: %v", err)
	}
	defer userDB.Close()

	// create Ulist

	ul := &ulist.Ulist{
		DummyMode:  *dummyMode,
		GDPRLogger: gdprLogger,
		Lists:      listDB,
		MTA:        mta,
		Superadmin: *superadmin,
		SpoolDir:   spoolDir,
		WebURL:     *webUrl,
	}

	// servers

	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, syscall.SIGINT, syscall.SIGTERM)

	// socketmap server

	sockmapSrv := sockmap.NewServer(ul.Lists.IsList, *lmtpSock)

	if *socketmapSock != "" {
		l, err := net.Listen("unix", *socketmapSock)
		if err != nil {
			log.Fatalf("error creating socketmap socket %s: %v", *socketmapSock, err)
		}
		go func() {
			if err := sockmapSrv.Serve(l); err != nil {
				log.Printf("socketmap server error: %v", err)
				shutdownChan <- syscall.SIGINT
			}
		}()

		log.Printf("socketmap listener: %s", *socketmapSock)
	}

	// web server

	w := web.Web{
		Ulist: ul,
		UserRepos: []web.UserRepo{ // SQL database first. Note that if both smtps and starttls ports are given and refer to the same email server, the email server might be queried twice.
			userDB,
			auth.SMTPS{Port: *smtpsAuthPort},
			auth.STARTTLS{Port: *starttlsAuthPort},
		},
	}

	if !w.AuthenticationAvailable() && !ul.DummyMode {
		log.Printf(WarnFormat, "There are no authenticators available. Users won't be able to log into the web interface.")
	}

	webNetwork := "unix"
	if strings.Contains(*webListen, ":") {
		webNetwork = "tcp"
	}

	webListener, err := net.Listen(webNetwork, *webListen)
	if err != nil {
		log.Fatalln(err)
	}

	webSrv := w.NewServer()

	go func() {
		if err := webSrv.Serve(webListener); err != nil && err != http.ErrServerClosed {
			log.Printf("web server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("web listener: %s://%s ", webNetwork, *webListen)

	// LMTP server

	lmtpSrv := ulist.NewLMTPServer(*lmtpSock, ul)

	go func() {
		if err := lmtpSrv.ListenAndServe(); err != nil {
			log.Printf("lmtp server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("LMTP listener: %s", *lmtpSock)

	// graceful shutdown

	log.Printf("running")

	<-shutdownChan
	log.Println("received shutdown signal")
	webSrv.Close()
	lmtpSrv.Close()
	sockmapSrv.Close()
	ul.Waiting.Wait()
	log.Printf("exiting")
}
