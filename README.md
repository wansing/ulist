# ulist

A mailing list service that keeps it simple.

## Build

See `build.sh`.

## Integration

* mail submission: ulist listens to an LMTP socket
* mail delivery to system's MTA: ulist executes `/usr/sbin/sendmail`
  * advantage: no recipient limit
  * disadvantage: when running in a jail, you need access to `/etc/postfix`, `/var/log/postfix` and `/var/spool/postfix/maildrop`
  * disadvantages of SMTP delivery: `localhost:25` usually accepts mail for localhost only and might drop emails for other recipients. `localhost:587` usually requires authentication and SSL/TLS
* Web UI: ulist listens to a port or a unix socket
* Web UI authentication: against a local database or SMTP server, see [auth](https://github.com/wansing/auth)
* Supported databases: SQLite, PostgreSQL (untested), MySQL/MariaDB (untested)

See `docs/integration` for examples.

## Decisions on From-Munging

* If a forwarded email is not modified, DKIM will pass but SPF checks might fail. We could predict the consequences by checking the sender's DMARC policy. But for the sake of consistence, let's rewrite all `From` headers to the mailing list address.
* As `From` is rewritten, it's feasible to modify the email. We can prepend the list name to the subject and add an unsubscribe footer to the content.

## Security Considerations

* We can't hide the existence of a list. Maybe in the web interface, but not via SMTP.

## TODO

* more unit tests
* opt-in after adding members manually
* GDPR: require opt-in after n days or member won't get mails any more
* more sophisticated bounce processing
* append an unsubscribe link to the content
* remove unsubscribing via email (it's prone to spoofing and can leak memberships)
* web UI: list creation permissions per domain
* remove IP address of sender (or check that removal works)
* ensure that the sender is not leaked if `HideFrom` is true, e.g. by removing `Delivered-To` headers?
* ability to block people (maybe keep membership and set `optInExpiry` timestamp to -1)

## Omitted features

* Archive
