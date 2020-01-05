# ulist

A mailing list service that keeps it simple.

## Build

See `build.sh`.

## Integration

* mail submission: ulist listens to an LMTP socket
* mail delivery to system's MTA: ulist executes `/usr/sbin/sendmail`
  * advantage: no recipient limit
  * disadvantage: when running in a jail, you need access to `/etc/postfix`, `/var/log/postfix` and `/var/spool/postfix/maildrop`
  * disadvantages of SMTP delivery: `localhost:25` usually accepts mail for localhost only and might drop emails for other recipients. `localhost:587` usually requires authentication and SSL/TLS.
* Web UI: ulist listens to `tcp://127.0.0.1:port` or a unix socket.
* Web UI authentication: against a local database or SMTP server, see [auth](https://github.com/wansing/auth)
* Supported databases: SQLite, PostgreSQL (untested), MySQL/MariaDB (untested)

## Example using nginx and postfix

### `/etc/systemd/system/ulist.service`

```
[Unit]
Description=ulist
After=network.target

[Service]
User=postfix
Group=postfix
Type=simple
WorkingDirectory=/srv/ulist/data
ExecStart=/usr/bin/ulist -httptcp 8080 -lmtp /var/spool/postfix/private/ulist-lmtp -smtps 50465 -superadmin admin@example.com -weburl "https://lists.example.com"

[Install]
WantedBy=multi-user.target
```

### `/etc/nginx/nginx.conf`

```
[...]
server {
    listen      443 ssl;
    server_name lists.example.com;
    location / {
        proxy_pass http://127.0.0.1:8080/;
    }
    [...]
}
[...]
```

### `/etc/postfix/main.cf`

If lists and regular email accounts share a domain, you can declare a `virtual_transport` to the LDA and a `transport_maps` to override it. If you have separate domains, there might be an easier way.

The `recipient_delimiter = +` is required in order to receive bounces at `listname+bounces@example.com`.

```
[...]
recipient_delimiter = +
virtual_mailbox_domains = example.com [...]
virtual_transport = lmtp:unix:/var/spool/postfix/private/dovecot-lmtp
transport_maps = sqlite:/etc/postfix/transport-maps.cf
[...]
```

### `/etc/postfix/transport-maps.cf`

```
dbpath = /srv/ulist/data/ulist.sqlite
query = SELECT CASE
	WHEN EXISTS(SELECT 1 FROM list WHERE address = '%s') THEN "lmtp:unix:/var/spool/postfix/private/ulist-lmtp"
END;
```

## Example using supervisord (on Uberspace 7)

* Uberspace 7 has no namespaces any more, so the second argument of `qmail-lmtp` is ´1´

### `~/etc/services.d/ulist.ini`

[program:ulist]
directory=/home/example/ulist
command=/home/example/ulist/ulist/ulist -http 0.0.0.0:8080 -starttls 587 -superadmin you@example.com -weburl "https://lists.example.com"
autostart=yes
autorestart=yes

## Example using apache and qmail (on Uberspace 6)

You need an LMTP wrapper script, for example `qmail-lmtp` from `mailman`:

```
wget -P ~/bin "https://gitlab.com/mailman/mailman/raw/master/contrib/qmail-lmtp"
chmod +x ~/bin/qmail-lmtp
```

### `~/.qmail-lists-default`

Assuming `lists` is the namespace of your lists domain.

```
|qmail-lmtp 8024 2 /home/example/ulist/lmtp.sock
```

* Parameters of `qmail-lmtp`
  * LMTP port, ignored because we connect to a socket
  * ext index ("EXT2 is the portion of EXT following the first dash"), used to crop the namespace (`lists-`) from the recipient address
  * LMTP host or absolute path to an unix socket

### `~/service/ulist/run`

```
#!/bin/sh
exec 2>&1
(cd ~/ulist && exec ulist -http 61234 -starttls 587 -superadmin you@example.com -weburl "https://lists.example.com")
```

### `.htaccess`

```
RewriteEngine On

RewriteCond %{HTTPS} !=on
RewriteCond %{ENV:HTTPS} !=on
RewriteRule ^(.*)$ https://%{SERVER_NAME}%{REQUEST_URI} [R=301,L]

RewriteBase /
RewriteRule ^(.*)$ http://127.0.0.1:61234/$1 [P]
RequestHeader set X-Forwarded-Proto https env=HTTPS
```

## Decisions on From-Munging

* If a forwarded email is not modified, DKIM will pass but SPF checks might fail. We could predict the consequences by checking the sender's DMARC policy. But for the sake of consistence, let's rewrite all `From` headers to the mailing list address.
* As `From` is rewritten, it's feasible to modify the email. We can prepend the list name to the subject and add an unsubscribe footer to the content.

## Security Considerations

* We can't hide the existence of a list. Maybe in the web interface, but not via SMTP.

## TODO

* more unit tests
* opt-in after adding members manually
* GDPR: log opt-in clicks separately
* GDPR: require opt-in after n days or member won't get mails any more
* more sophisticated bounce processing
* append an unsubscribe link to the content
* remove unsubscribing via email (that's prone to spoofing and can leak memberships)
* web UI: list creation permissions per domain
* prevent email loops (check `Received` or `List-...` headers)
* reject or always moderate emails which have been flagged as spam (`X-Spam` header or so)
* remove IP address of sender (or check that removal works)
* ensure that the sender is not leaked if `HideFrom` is true, e.g. by removing `Delivered-To` headers?
* ability to block people (maybe keep membership and set `optInExpiry` timestamp to -1)

## Omitted features

* Archive
