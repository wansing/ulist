# ulist

A mailing list service that keeps it simple. An alternative to mailman in some use cases.

<p align="center">
	<img src="/docs/screencast-1.apng?raw=true" width="720">
</p>

## Build

```
go build ./cmd/...
```

Arch Linux users can install ulist from the [AUR](https://aur.archlinux.org/packages/ulist/).

## Integration

* Email submission: ulist listens to an LMTP socket
* Email delivery to system's MTA: ulist executes `/usr/sbin/sendmail`
* Web UI: ulist listens to a port or a unix socket
* Web UI authentication: against a local SQLite database or an SMTP server

See `docs/integration` for examples.

## Features

* single binary
* nice web interface
* works with SPF, DKIM etc. out of the box
* SMTP authentication
* probably GDPR compliant
* appends a footer with an unsubscribe link
* [socketmap](http://www.postfix.org/socketmap_table.5.html) server for postfix

## Design Choices

* Email delivery via the sendmail interface
  * no recipient limit
  * when running in a jail, you need access to `/etc/postfix/main.cf` and `/var/spool/postfix/maildrop`
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

* fail2ban pattern
* LDAP authenticator
* more unit tests
* GDPR: require opt-in after n days or member won't get mails any more
* more sophisticated bounce processing
* web UI: list creation permissions per domain
* remove IP address of sender (or check that removal works)
* ensure that the sender is not leaked if `HideFrom` is true, e.g. by removing `Delivered-To` headers?
* ability to block people (maybe keep membership and set `optInExpiry` timestamp or so to -1)
* maybe issue with Apple Mail: two line breaks after header

## Omitted features

* Archive

## Known issues

* Email addresses like `alice@example.com <alice@example.com>` are not RFC 5322 compliant, use `alice <alice@example.com>` or `"alice@example.com" <alice@example.com>`
