# Running the tests against a real database

Everything runs on SQLite by default, in a temporary file, with no setup.

That is also how the plugin registry shipped broken on MySQL for months. The
schema is written out per backend, side by side, where a missing column reads as
formatting — and nothing ever executed the MySQL or Postgres branch, so two of
the three supported backends were verified by reading them.

Two things close that, and they are not substitutes for each other:

- `TestEveryBackendDeclaresTheSameColumns` parses `database.go` and checks that
  when one migration creates a table on several backends, every backend declares
  the same columns. It needs no server and catches the exact bug that shipped.
- Pointing the suite at a real server catches what static checking cannot: a
  statement that backend will not accept, a type it renders differently, a
  constraint it enforces sooner.

## Pointing the suite at a server

```sh
RDIO_TEST_DB_TYPE=postgresql \
RDIO_TEST_DB_HOST=localhost \
RDIO_TEST_DB_PORT=5432 \
RDIO_TEST_DB_NAME=rdio_test \
RDIO_TEST_DB_USER=rdio \
RDIO_TEST_DB_PASS=secret \
go test ./server -run 'Migration|Plugin|Transcript|Schema' -v
```

`RDIO_TEST_DB_TYPE` accepts `postgresql`, `mysql` or `mariadb`. Host defaults to
`localhost` and port to 5432 or 3306 to match the type; the rest are taken as
given.

**The named database is emptied at the start of every test.** Every table
beginning `rdioScanner` or `plugin_` is dropped. Use a database kept for testing
and nothing else — never point this at a server holding calls.

## What to run

`-run 'Migration|Plugin|Transcript|Schema'` covers the parts where the backend
matters: the migration ledger, the plugin registry and its schema evolution, and
the transcripts move. The rest of the suite does not touch the database and has
nothing to gain from a real one.

## Race detection

```sh
CGO_ENABLED=1 go test ./server -race -run 'Plugin|Clone|Dispatch|Watchdog'
```

Needs a C toolchain, which is why it does not run everywhere. Much of the plugin
work is concurrent — dispatch, the event loops, the registry — so this is worth
running somewhere it can before a release.
