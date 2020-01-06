# Integration into qmail and apache (as on uberspace.de version 6)

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
```

## `~/.qmail-lists-default`

Assuming `lists` is the namespace of your lists domain.

```
|qmail-lmtp 8024 2 /home/example/ulist/lmtp.sock
```

* Parameters of `qmail-lmtp`
  * LMTP port, ignored because we connect to a socket
  * ext index ("EXT2 is the portion of EXT following the first dash"), use `2` in order to crop a namespace (`lists-`) from the recipient address
  * LMTP host or absolute path to an unix socket

## `~/service/ulist/run`

```
#!/bin/sh
exec 2>&1
(cd ~/ulist && exec ulist -http 61234 -starttls 587 -superadmin admin@example.com -weburl "https://lists.example.com")
```

## `.htaccess`

```
RewriteEngine On

RewriteCond %{HTTPS} !=on
RewriteCond %{ENV:HTTPS} !=on
RewriteRule ^(.*)$ https://%{SERVER_NAME}%{REQUEST_URI} [R=301,L]

RewriteBase /
RewriteRule ^(.*)$ http://127.0.0.1:61234/$1 [P]
RequestHeader set X-Forwarded-Proto https env=HTTPS
```
