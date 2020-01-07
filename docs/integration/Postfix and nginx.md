# Integration into nginx and postfix

## `/etc/systemd/system/ulist.service`

```
[Unit]
Description=ulist
After=network.target

[Service]
User=postfix
Group=postfix
Type=simple
WorkingDirectory=/srv/ulist/data
ExecStart=/usr/bin/ulist -lmtp /var/spool/postfix/private/ulist-lmtp -smtps 465 -superadmin admin@example.com -weburl "https://lists.example.com"

[Install]
WantedBy=multi-user.target
```

## `/etc/nginx/nginx.conf`

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

## `/etc/postfix/main.cf`

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

## `/etc/postfix/transport-maps.cf`

```
dbpath = /srv/ulist/data/ulist.sqlite
query = SELECT CASE
	WHEN EXISTS(SELECT 1 FROM list WHERE address = '%s') THEN "lmtp:unix:/var/spool/postfix/private/ulist-lmtp"
END;
```
