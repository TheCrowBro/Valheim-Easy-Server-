# Goldens: what bed004b does

These files were recorded from the code at bed004b (the release installed
copies run before the multi-game work) and are checked by tests. They are
the safety net for the rebuild: the new code has to give the same answers.

- `serverargs.json`: the Valheim server's command line and the `argsKey`
  that says when a restart is needed, for a table of settings.
- `discord.json`: the online, stopped and daily Discord posts, and the
  Settings page's test post.
- `reminders.json`: what players are told to do before they join.
- `exithints.json`: the hint shown when the server stops unexpectedly.
- `tracker.json`, with the log excerpts in `tracker/`: what the app makes of
  a server log (the Log page, the activity feed, who's online, the join
  code, and the records kept afterwards).
- `status.json`: every key `/api/status` has, with its JSON type.
- `routes.json`: how every route of the control panel answers.

`../legacy/` holds each file the app keeps in its data folder, as bed004b
writes it.

The tests are in `internal/manager/*_golden_test.go` and
`internal/web/legacy_golden_test.go`. Times in these files are this
computer's local time, so the tests pass in any time zone.

Never re-record a golden to make a test pass: change the code until it
gives the recorded answer. `VES_RECORD_GOLDEN=1` only writes a golden file
that doesn't exist yet, for new cases in a new file.
