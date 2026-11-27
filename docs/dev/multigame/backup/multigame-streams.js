export const meta = {
  name: 'multigame-streams',
  description: 'Build multi-game work streams in parallel worktrees, each reviewed independently and followed up',
  phases: [
    { title: 'Build', detail: 'one coding agent per stream, in its own worktree' },
    { title: 'Review', detail: 'an independent reviewer per stream' },
    { title: 'Follow-up', detail: 'address what the reviewers found' },
  ],
}

const BASE = args.base
const SPEC = '/home/user/valheim-easy-server-/docs/dev/multigame/spec.md'
const CONTEXT = (branch, release) => `You are working on "Valheim Easy Server" (becoming "Easy Game Server"): a Go single-exe app with an embedded vanilla-JS control panel that hosts game servers for friends on Windows 10/11 and macOS 12+ (Linux for tests). Installed copies auto-update from every release of the default branch and roll back automatically if a new build fails, so nothing an installed copy depends on may change unless the spec says so.

The design is ${SPEC} (read sections 1, 2 and 9 fully, plus every section your stream's brief names) with the owner's decisions in docs/dev/multigame/decisions.md (they win). Identity constants and golden tests from release R1 are in the tree: they must stay green. You are building one stream of release ${release}.

Rules:
- Work in your own git worktree on branch ${branch}, based on commit ${BASE} (the multigame branch). Edit only the files your stream owns (spec 9.3); anything else, report it instead.
- Plain English in comments and any user-facing text; explain why.
- Checks before committing: "go version" (use GOTOOLCHAIN=go1.26.10 if older); gofmt -l . (prints nothing); go vet ./... and with GOOS=windows and GOOS=darwin; go test -race -count=1 ./... (internal/manager takes several minutes); node --check on any changed .js. You can only run Linux here; make Windows/macOS-only code obviously correct and say what only CI can confirm.
- Commit on your branch: a short title, a blank line, a plain summary, then exactly these two lines:
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Ekx5qMqLB1h6gHQcZYRXnE
- Never push, never tag, never touch other branches or git history.`

const RESULT = {
  type: 'object',
  properties: {
    worktree: { type: 'string' }, branch: { type: 'string' }, commit: { type: 'string' },
    summary: { type: 'string' }, ciOnly: { type: 'string', description: 'what only Windows/macOS CI can confirm' }, tests: { type: 'string' },
    forLead: { type: 'string', description: 'anything the lead must do or decide (files you needed but do not own, spec gaps, contract questions)' },
  },
  required: ['worktree', 'branch', 'commit', 'summary', 'ciOnly', 'tests', 'forLead'],
}
const REVIEW = {
  type: 'object',
  properties: { issues: { type: 'array', items: { type: 'object', properties: {
    title: { type: 'string' }, file: { type: 'string' }, severity: { type: 'string', enum: ['must-fix', 'should-fix'] }, detail: { type: 'string' },
  }, required: ['title', 'file', 'severity', 'detail'] } } },
  required: ['issues'],
}

const results = await pipeline(args.streams,
  (s) => agent(`${CONTEXT(s.branch, s.release)}

First run: git checkout -b ${s.branch} ${BASE}   (check with git log -1 that you're exactly on ${BASE}).

${s.ask}

Report the worktree path, branch, commit, what you built, what only CI can confirm, what you ran, and anything for the lead.`, { label: `build:${s.key}`, phase: 'Build', schema: RESULT, isolation: 'worktree' }),
  (built, s) => built && agent(`${CONTEXT(s.branch, s.release)}

Review stream ${s.key} without changing anything. Worktree ${built.worktree}, diff ${BASE}..${built.commit} (git -C ${built.worktree} diff ${BASE} ${built.commit}). The stream's brief: ${s.ask}
The builder reported: ${JSON.stringify(built)}

Check against the spec, the owner's decisions and the real code: does it meet every acceptance point in its brief and spec 9.3? Does it change behaviour that must not change (R1's identity and golden tests, Valheim's behaviour, installed copies' update/rollback)? Would it break CI on Windows or macOS? Do its tests really test what they claim? Is anything over-built for what the stream needs? You may copy the worktree elsewhere to run things. Report only real problems: must-fix for wrong/incomplete work and regressions, should-fix for clear smaller gaps. An empty list is the expected outcome for good work.`, { label: `review:${s.key}`, phase: 'Review', schema: REVIEW }).then(r => ({ built, reviewed: !!r, issues: r ? r.issues : [] })),
  (rv, s) => {
    if (!rv || !rv.built) return null
    if (!rv.issues.length) return { stream: s.key, built: rv.built, reviewed: rv.reviewed, review: [], follow: null }
    return agent(`${CONTEXT(s.branch, s.release)}

Continue stream ${s.key} in the existing worktree ${rv.built.worktree} on branch ${s.branch} (cd there; don't create another worktree). An independent reviewer found:
${JSON.stringify(rv.issues, null, 2)}

Address every must-fix and the should-fix items that are right (explain any you disagree with). Run the checks again and add a new commit. Report as before.`, { label: `follow:${s.key}`, phase: 'Follow-up', schema: RESULT }).then(f => ({ stream: s.key, built: rv.built, reviewed: rv.reviewed, review: rv.issues, follow: f }))
  },
)
return results.filter(Boolean)
