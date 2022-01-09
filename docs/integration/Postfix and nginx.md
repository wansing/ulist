# Integration into nginx and postfix

## `/etc/systemd/system/ulist.service`

You can omit this step if you installed ulist from a proper package source, like the [Arch Linux User Repository (AUR)](https://aur.archlinux.org/packages/ulist/).

```
[Unit]
Description=ulist
After=network.target

[Service]
Type=simple
User=ulist
Group=ulist
SupplementaryGroups=postdrop
PrivateDevices=true
PrivateIPC=true
PrivateTmp=true
ProtectControlGroups=true
ProtectKernelTunables=true
ProtectSystem=strict
ReadWritePaths=/var/lib/ulist
ReadWritePaths=/var/spool/postfix/maildrop/
RuntimeDirectory=ulist
EnvironmentFile=/etc/ulist/ulist.conf
ExecStart=/usr/bin/ulist

[Install]
WantedBy=multi-user.target
```

Create the system user:

```
useradd --system --shell /usr/bin/nologin --create-home --home-dir /var/lib/ulist ulist
```

## `/etc/ulist/ulist.conf`

```
http=127.0.0.1:8080
smtps=
starttls=
superadmin=
weburl=http://127.0.0.1:8080
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
transport_maps = socketmap:unix:/run/ulist/socketmap.sock:name
[...]
```
