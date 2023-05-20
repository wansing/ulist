# Changelog

## v0.14.0 (2023-05-20)

A database schema upgrade is required:

```
BEGIN TRANSACTION;
ALTER TABLE member ADD COLUMN bounces BOOLEAN NOT NULL default 0;
UPDATE member SET bounces = admin;
COMMIT;
```
