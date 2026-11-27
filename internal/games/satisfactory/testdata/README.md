# Where the test data comes from

Checked on 2026-11-26. FACTS.md has the facts these files back.

## api/

- `QueryServerState-1.2.4.0.json`: a real server's answer to QueryServerState,
  game version 1.2.4.0 (CL 502094), captured on 2026-09-21 by the
  satisfactory-dash project:
  https://raw.githubusercontent.com/Sour-Dev-Home/satisfactory-dash/main/docs-vault/raw-sources/captured-responses/vanilla-QueryServerState-sample.json
  (the file's last line, copied as it is).
- `CreateNewGame-missing-1.2.4.0.json`: the same real server's answer to a
  CreateNewGame without `bSkipOnboarding`, from the same project:
  https://raw.githubusercontent.com/Sour-Dev-Home/satisfactory-dash/main/docs-vault/raw-sources/captured-responses/vanilla-CreateNewGame-discrepancy.md
  (its first answer, copied as it is). The same file shows the server then
  asking for `GameModeSettings`, and accepting the call (HTTP 202) once both
  were there.

## monitor/

- `v1.0.0-CL-365306-linux-start.log`: lines from a real 1.0.0 (CL 365306)
  dedicated server's start, on Linux, from the robtme/satisfactory-server
  Docker image's repository: https://raw.githubusercontent.com/robtme/satisfactory-server/main/server.log
  (lines 113, 354-358, 363-365 and 609-634, copied as they are). It has
  the version lines, the API's certificate fingerprint and the API's port.
  Its command line turns the LogNet category down to errors, so it has no
  join or leave lines.
- `engine-format-join-leave.log`: NOT copied from a real server. Written by
  hand from Unreal Engine's own log lines for a login, a join and a closed
  connection (its World.cpp and NetConnection.cpp), in the shapes three
  independent tools that read Satisfactory's log match (DJWoodZ's
  Satisfactory-Discord-Bot src/utils/parser.js, sharkoz's
  satisfactory-server-notifications app/index.ts, binaryn3xus/satisfactory
  internal/players/tracker.go). To be replaced by a real session's lines
  (a friend joining and leaving the owner's server) before the game is
  marked ready (spec 8.2).

## golden/

Written by the tests themselves (`go test -update`), from the adapter's
code: the command line and plan (`command-line.txt`), and the API calls
the app makes at a first start, a second start, a start of another world,
and a stop (`setup-*.txt`, `stop-calls.txt`), as a recording stand-in API
saw them, with passwords replaced by what they are.
