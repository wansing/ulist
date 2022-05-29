package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/wansing/ulist"
	"github.com/wansing/ulist/auth"
	"github.com/wansing/ulist/filelog"
	"github.com/wansing/ulist/mailutil"
	"github.com/wansing/ulist/repos/sqlite"
	"github.com/wansing/ulist/web"
)

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
		const warnFormat = "\033[1;31m%s\033[0m"
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

	// dbs

	gdprLogger, err := filelog.NewFileLogger(filepath.Join(stateDir, "gdpr.log"))
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

	ul := &ulist.Ulist{
		DummyMode:     dummyMode,
		GDPRLogger:    gdprLogger,
		Lists:         listDB,
		LMTPSock:      lmtpSock,
		SocketmapSock: socketmapSock,
		Superadmin:    superadmin,
		SpoolDir:      spoolDir,
	}
	defer ul.Waiting.Wait()

	ul.Web = web.Web{
		Ulist:  ul,
		Listen: webListen,
		URL:    webURL,
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

	if dummyMode {
		ul.MTA = mailutil.DummyMTA{}
	}

	if err := ul.ListenAndServe(); err != nil {
		log.Printf("error %v", err)
	}
}
