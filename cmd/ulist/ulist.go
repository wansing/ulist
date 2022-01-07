package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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

const warnFormat = "\033[1;31m%s\033[0m"

func main() {

	log.SetFlags(0) // no log prefixes required, systemd-journald adds them
	defer func() {
		log.Println("exiting")
	}()

	// configuration

	dummyMode := os.Getenv("dummymode") == "true"
	smtpsAuthPort, _ := strconv.Atoi(os.Getenv("smtps"))
	starttlsAuthPort, _ := strconv.Atoi(os.Getenv("starttls"))
	superadmin := os.Getenv("superadmin")
	webListen := os.Getenv("http")
	webURL := os.Getenv("weburl")

	if webListen == "" {
		webListen = "127.0.0.1:8080"
	}
	if webURL == "" {
		webURL = "http://127.0.0.1:8080"
	}

	flag.BoolVar(&dummyMode, "dummymode", dummyMode, "accept any user credentials and don't send any emails")
	flag.IntVar(&smtpsAuthPort, "smtps", smtpsAuthPort, "connect to localhost:`port` for SMTPS user authentication (first choice)")
	flag.IntVar(&starttlsAuthPort, "starttls", starttlsAuthPort, "connect to localhost:`port` for SMTP STARTTLS user authentication")
	flag.StringVar(&superadmin, "superadmin", superadmin, "allow the user with this `email` address to create, delete and modify every list through the web interface")
	flag.StringVar(&webListen, "http", webListen, "make the web interface available at this ip:port or socket path")
	flag.StringVar(&webURL, "weburl", webURL, "use this `url` in links to the web interface")
	flag.Parse()

	if dummyMode {
		superadmin = "test@example.com"
		log.Printf(warnFormat, "ulist runs in dummy mode. Everyone can login as superadmin and no emails are sent.")
	}

	// sockets

	runtimeDir := os.Getenv("RUNTIME_DIRECTORY")
	if runtimeDir == "" {
		runtimeDir = "/run/ulist"
	}
	runtimeDir = filepath.Join("/", runtimeDir) // make absolute

	lmtpSock := filepath.Join(runtimeDir, "lmtp.sock")
	socketmapSock := filepath.Join(runtimeDir, "socketmap.sock")

	// spool dir

	stateDir := os.Getenv("STATE_DIRECTORY")
	if stateDir == "" {
		stateDir = "/var/lib/ulist"
	}
	if dummyMode {
		stateDir = "/tmp/ulist"
	}

	spoolDir := filepath.Join(stateDir, "spool")
	if err := os.MkdirAll(spoolDir, 0700); err != nil {
		log.Printf("error creating spool directory: %v", err)
		return
	}

	if unix.Access(spoolDir, unix.W_OK) != nil {
		log.Printf("spool directory %s is not writeable", spoolDir)
		return
	}

	log.Printf("spool directory: %s", spoolDir)

	// dbs

	gdprLogger, err := util.NewFileLogger(filepath.Join(stateDir, "gdpr.log"))
	if err != nil {
		log.Printf("error creating GDPR logfile: %v", err)
		return
	}
	defer gdprLogger.Close()

	listDB, err := sqlite.OpenListDB(filepath.Join(stateDir, "lists.sqlite3?_busy_timeout=10000&_journal=WAL&_sync=NORMAL&cache=shared"))
	if err != nil {
		log.Printf("error opening list db: %v", err)
		return
	}
	defer listDB.Close()

	userDB, err := sqlite.OpenUserDB(filepath.Join(stateDir, "users.sqlite3?_busy_timeout=10000&_journal=WAL&_sync=NORMAL&cache=shared"))
	if err != nil {
		log.Printf("error opening user db: %v", err)
		return
	}
	defer userDB.Close()

	// create Ulist

	var mta mailutil.MTA = mailutil.Sendmail{}
	if dummyMode {
		mta = mailutil.DummyMTA{}
	}

	ul := &ulist.Ulist{
		DummyMode:  dummyMode,
		GDPRLogger: gdprLogger,
		Lists:      listDB,
		MTA:        mta,
		Superadmin: superadmin,
		SpoolDir:   spoolDir,
		WebURL:     webURL,
	}
	defer ul.Waiting.Wait()

	// servers

	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, syscall.SIGINT, syscall.SIGTERM)

	// socketmap server

	sockmapSrv := sockmap.NewServer(ul.Lists.IsList, lmtpSock)
	defer sockmapSrv.Close()

	sockmapListener, err := net.Listen("unix", socketmapSock)
	if err != nil {
		log.Printf("error creating socketmap socket: %v", err)
		return
	}
	if err := os.Chmod(socketmapSock, 0777); err != nil {
		log.Printf("error setting socketmap socket permissions: %v", err)
		return
	}

	go func() {
		if err := sockmapSrv.Serve(sockmapListener); err != nil {
			log.Printf("socketmap server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("socketmap listener: %s", socketmapSock)

	// web server

	w := web.Web{
		Ulist: ul,
		UserRepos: []web.UserRepo{ // SQL database first. Note that if both smtps and starttls ports are given and refer to the same email server, the email server might be queried twice.
			userDB,
			auth.SMTPS{
				Port: uint(smtpsAuthPort),
			},
			auth.STARTTLS{
				Port: uint(starttlsAuthPort),
			},
		},
	}

	if names := w.AuthenticatorNames(); names != "" {
		log.Printf("authenticators: %s", names)
	}

	if !w.AuthenticationAvailable() && !ul.DummyMode {
		log.Printf(warnFormat, "There are no authenticators available. Users won't be able to log into the web interface.")
	}

	webNetwork := "unix"
	if strings.Contains(webListen, ":") {
		webNetwork = "tcp"
	}

	webListener, err := net.Listen(webNetwork, webListen)
	if err != nil {
		log.Printf("error creating web listener: %v", err)
		return
	}
	if webNetwork == "unix" {
		if err := os.Chmod(webListen, 0777); err != nil {
			log.Printf("error setting web socket permissions: %v", err)
			return
		}
	}

	webSrv := w.NewServer()
	defer webSrv.Close()

	go func() {
		if err := webSrv.Serve(webListener); err != nil && err != http.ErrServerClosed {
			log.Printf("web server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	log.Printf("web listener: %s://%s ", webNetwork, webListen)

	// LMTP server

	lmtpSrv := ulist.NewLMTPServer(lmtpSock, ul)
	defer lmtpSrv.Close()

	go func() {
		if err := lmtpSrv.ListenAndServe(); err != nil {
			log.Printf("lmtp server error: %v", err)
			shutdownChan <- syscall.SIGINT
		}
	}()

	if err := os.Chmod(lmtpSock, 0777); err != nil {
		log.Printf("error setting LMTP socket permissions: %v", err)
		return
	}

	log.Printf("LMTP listener: %s", lmtpSock)

	// wait for shutdown signal

	log.Printf("running")
	<-shutdownChan
	log.Println("received shutdown signal")
}
