# ulist

A mailing list service that keeps it simple.

## Build

See `build.sh`.

## Integration

* mail submission: ulist listens to an LMTP socket
* mail delivery to system's MTA: ulist executes `/usr/sbin/sendmail`
* Web UI: ulist listens to a port or a unix socket
* Web UI authentication: against a local database or SMTP server, see [auth](https://github.com/wansing/auth)
* Supported databases: SQLite, PostgreSQL (untested), MySQL/MariaDB (untested)

See `docs/integration` for examples.

## Features

* single binary
* nice web interface
* works with SPF, DKIM etc. out of the box
* pluggable authentication
* probably GDPR compliant
* appends a footer with an unsubscribe link
* socketmap server for postfix

## Design Choices

* Email delivery via the sendmail interface
  * no recipient limit
  * when running in a jail, you need access to `/etc/postfix`, `/var/log/postfix` and `/var/spool/postfix/maildrop`
  * easier than SMTP delivery (`localhost:25` usually accepts mail for localhost only and might drop emails for other recipients, `localhost:587` usually requires authentication and SSL/TLS)
* From-Munging
  * If a forwarded email is not modified, DKIM will pass but SPF checks might fail. We could predict the consequences by checking the sender's DMARC policy. But for the sake of consistence, let's rewrite all `From` headers to the mailing list address and remove existing DKIM signatures.
* Modifying emails
  * As original DKIM signatures are removed, we can modify parts of the email. We can prepend the list name to the subject header and add an unsubscribe footer to the message body.
* Subscribe and unsubscribe
  * Issue: emails (like "subscribe" or "unsubscribe" instructions) can be spoofed
  * Issue: opt-in backscatter spam is an issue rather with web forms (trade a http request for an email) than email (trade an email for an email)
  * Issue: individual unsubscribe links in the footer will fall into others hands, as people will forward or full-quote emails
  * Decision: signup web form must be protected against spam bots and use rate limiting
  * Decision: subscribe/unsubscribe requesters get an email with a confirmation link
  * Terms
    1. user: ask (via web or email with special subject)
    2. server: checkback (send email with link)
    3. user: confirm (click link)
    4. server: sign off (send welcome or goodbye email)
* Memory consumption
  * Issue: some people use email aliases and don't remember which address they subscribed
  * Issue: individual list emails consume much memory, e.g. 1000 recipients × 10 MB message = 10 GB
  * Decision: notification emails (checkback, sign-off, moderation) are individual
  * Decision: list emails are not individual, MTA gets one email with many recipients (envelope-to)
  * List receivers must maintain an overview over their email aliases or check the Delivered-To header line.

## Security Considerations

* We can't hide the existence of a list. Maybe in the web interface, but not via SMTP.

## TODO

* docs: mention the postfix transport_maps [interface](http://www.postfix.org/DATABASE_README.html#types) interface instead of letting postfix access the ulist database
* LDAP authenticator
* more unit tests
* GDPR: require opt-in after n days or member won't get mails any more
* more sophisticated bounce processing
* web UI: list creation permissions per domain
* remove IP address of sender (or check that removal works)
* ensure that the sender is not leaked if `HideFrom` is true, e.g. by removing `Delivered-To` headers?
* ability to block people (maybe keep membership and set `optInExpiry` timestamp or so to -1)
* maybe issue with Apple Mail: two line breaks after header
* one mails sent through multiple lists has the same Message-Id, mixing up replies if a user is in both lists

## Omitted features

* Archive
