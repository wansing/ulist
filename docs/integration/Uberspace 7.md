# Integration on uberspace.de version 7

You need an LMTP wrapper script, for example `qmail-lmtp` from `mailman`:

```
wget -P ~/bin "https://gitlab.com/mailman/mailman/raw/master/contrib/qmail-lmtp"
chmod +x ~/bin/qmail-lmtp
```

## Installation

```
mkdir ~/ulist
cd ~/ulist
git clone https://github.com/wansing/ulist
cd ulist
go build -trimpath -buildmode=pie -mod=readonly -modcacherw -ldflags "-linkmode external -extldflags \"${LDFLAGS}\"" ./cmd/...
```

## `~/.qmail` files

Uberspace 7 does not support email namespaces any more. If you use virtual mailboxes, you need individual `.qmail` files for each list. Else you can overwrite `~/.qmail-default`.

```
|/home/example/bin/qmail-lmtp 8024 1 /home/example/ulist/lmtp.sock
```

* Parameters of `qmail-lmtp`
  * LMTP port, ignored because we connect to a socket
  * ext index ("EXT2 is the portion of EXT following the first dash")
  * LMTP host or absolute path to an unix socket

## `~/etc/services.d/ulist.ini`

```
[program:ulist]
directory=/home/example/ulist
environment=RUNTIME_DIRECTORY="/home/example/ulist",STATE_DIRECTORY="/home/example/ulist"
command=/home/example/ulist/ulist/ulist -http 0.0.0.0:8080 -starttls 587 -superadmin admin@example.com -weburl "https://lists.example.com"
autostart=yes
autorestart=yes
```

Re-read the supervisord config and start ulist:

```
supervisorctl reread
supervisorctl update
supervisorctl start ulist
```

## Add the web backend

```
uberspace web domain add lists.example.com
uberspace web backend set lists.example.com --http --port 8080
```
