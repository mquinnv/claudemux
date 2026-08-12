# SDD ledger — plan: docs/superpowers/plans/2026-08-11-background-work-state.md

Branch: worktree-align-context-meters
Merge base for final review: 40b0915 (branch head before Task 1)

Pre-flight scan: Task 2's code block imports `time` but does not use it until
Task 3. Go rejects unused imports, so the Task 2 dispatch instructs the
implementer to omit it and Task 3 to add it. No human ruling needed.

Task 1: complete (commits 40b0915..5bdca12, review clean)
Task 2: complete (commits 5bdca12..7d0c341, review clean)
  note: brief's test-file import block listed an unused `strings`; implementer
  dropped it (Go rejects unused imports). Same category as the directed `time`
  correction. Not a defect.
Task 3: complete (commits 7d0c341..7d828b8, review clean)
Task 3: minor (deferred): outstanding()'s zero-value `oldest` for an empty or
  fully-expired tracker is correct by inspection but not directly asserted;
  the only consumer (bgOverride, Task 4) guards on count <= 0 first.
Task 3: minor (deferred): task-3-report.md's test arithmetic says "4 new"
  while listing 5; all 5 tracker tests verified present in the file.
Task 3: ⚠️ resolved by controller — "observe fed only new events" is Task 5's
  wiring requirement, and "caller guards oldest when count == 0" is Task 4's
  bgOverride. Both carried into those dispatches and reviews.
Task 4: complete (commits 7d828b8..9fcf6cc, review clean)
  Task 3's ⚠️ "caller guards oldest when count == 0" is now RESOLVED: both
  bgOverride guards verified present (state.go:91-100).
Task 5: implemented (commit dc81509), report DONE_WITH_CONCERNS — implementer
  could not run the brief's Step 7 live-tmux check.
Task 5: Step 7 verified by controller on the installed binary, head %179,
  session `claudemux`, with one real outstanding background task:
    t+25  @claudemux_state = Background:1   head: "● Working 1 0:24 · opus-5"
    t+50  @claudemux_state = Background:1   head: "● Working 1 0:49 · opus-5"
  Duration counts from the launch, not the turn's end. Concern resolved.
  Task 3's ⚠️ "observe fed only new events" is covered by this task's review.
Task 5: review — SPEC ✅; 1 Important (test bypasses Update(dataMsg), so the
  wiring itself is unverified), 1 Minor. Important entered the fix loop.
Task 5: minor (NOT a defect): reviewer flagged that switchSession resets the
  tracker without observing the seeded events, so a session rotated into with
  work already outstanding reads Idle. That is the documented "Seeding" limit
  in the spec (2026-08-11-background-work-state-design.md), which calls it
  graceful degradation to today's behavior. No action; recorded for the final
  review's triage.
Task 5: fix round 1/5 (1 addressed, 0 open — Update(dataMsg) wiring now
  covered by TestUpdateDataMsgFeedsBackgroundTracker; commits dc81509..b9ee17e)
Task 5: complete (commits 9fcf6cc..b9ee17e, review clean)

Final whole-branch review (6df36d0..b9ee17e, opus): NOT READY TO MERGE.
  Blocker — Important 1: bgLaunches matches its markers anywhere in a
  tool_result, so merely READING or grepping text containing them registers a
  phantom launch. Verified: a Grep hit with `agentId: x` publishes
  Background:1 and hides a genuinely idle session from the conductor for up to
  30 min (the prompt-clear rule is unreachable — the user can't be sent
  there). This repo's own docs/tests trigger it. Strictly worse than
  pre-branch behavior; NOT covered by the accepted wording-drift risk, which
  is a false negative.
  Also: Important 2 (lobby line 2 clipped by rune count, no clipLine — wraps
  and destroys the grid), and Minors 3-9.
  Ledger triage by the final reviewer: nothing previously deferred is
  must-fix. The blocker was never in the ledger.
Final fix wave dispatched (one agent, findings 1-8; age column and seeding
  explicitly out of scope).
INSTALL HELD until the fix lands — installing b9ee17e would make Michael's
  own claudemux sessions invisible to his switchboard. Branch IS pushed.

Final fix wave: commits 6d1ca2f (findings 1,5,6), d319866 (2,3,4),
  7281b88 (7,8). Scoped re-review dispatched (one, per the skill).

Flake note — RESOLVED, and my first explanation was wrong. It is not reviewer
  interference. `TestEnvFileValueRetriesTransientlyEmptyFIFO` (env_test.go:229)
  fails ~20-25% of runs even in ISOLATION, taking 30.05s when it does — which
  is the 33s whole-suite failure. env.go/env_test.go are untouched by this
  branch, so this is a pre-existing flake on main, not a regression. Out of
  scope here; worth its own fix.

Fix-wave re-review (opus, b9ee17e..7281b88): findings 2-8 ADDRESSED, each
  mutation-verified. Finding 1 NOT ADDRESSED — the blocker survives, narrowed.
  bgwork.go:52-56 gates an UNANCHORED `agentId: (...)` on the substring
  "Async agent launched", but any document quoting the launch payload carries
  both strings, and the spec's line 59 has the sentence and the id on ONE
  physical line. So a Read of this repo's own spec, plan, bgwork_test.go, or
  events_test.go still publishes Background:1 and hides an idle session.
  Real payloads still register correctly (both spec shapes verified).
  Why it slipped: the negative fixtures are synthetic one-liners, so the fix
  and its tests were written against each other rather than against the case
  named in the finding. A fixture built from the spec file's own bytes would
  have caught it.
  Verified minimal closure (re-reviewer scratch-tested it): anchor the agent
  id the same way as the shell marker — `(?m)^agentId: ([A-Za-z0-9]+)` — while
  keeping the launch-sentence gate. Still extracts from the real payload;
  matches neither the spec nor the plan.
BLOCKED — load-bearing residual after the one permitted fix wave and its one
  re-review. Per the skill: no second fix wave; surfaced to Michael.
UNBLOCKED — Michael chose option 1: one targeted fix (anchor the agent id,
  keep the gate) plus a fixture built from the real doc's bytes, then
  re-review. Second fix round dispatched to the original fixer, with the
  tests-fail-first evidence order required, and the spec's Detection rules
  section to be corrected in the same round.

Second fix round (0f72b6d) re-review (opus, adversarial): Finding 1 STILL NOT
  ADDRESSED — narrowed a second time, not closed. 9 of 9 quoting shapes tried
  defeat it, and one fires on this repo TODAY: `grep "Command running in
  background with ID" <the spec>` with a single file argument emits NO path
  prefix, so the quoted line lands at byte 0 and the absolute anchor matches.
  bgLaunches -> ["boigiwsir"]. Pretty-printed quotes of the agent payload
  (real newlines instead of escaped \n) defeat the agent gate too — including
  the spec's own example if anyone ever reflows it for readability.
  Fleet sweep over 255 real transcripts: 0 genuine launches missed by the
  anchors (802/844 shell results start at byte 0; 1646/1682 agent results have
  agentId at a line start). So the risk is entirely false-positive.
  Test quality: fail-first evidence genuine and reproduced; fixtures ARE built
  from real file bytes. But the vacuity guard checks bare substrings that now
  also appear in the prose the same commit added, so reflowing the two example
  lines leaves the guard passing and the test vacuous.

ROOT CAUSE (mine, at spec time): text markers in tool_result CONTENT cannot
  distinguish "a launch happened" from "text about a launch." Anchoring is
  whack-a-mole against document formatting. During brainstorming I dismissed
  the tool-input option because async agents carry no run_in_background flag —
  true, but I never considered the tool NAME.
STRUCTURAL FIX (reviewer's, and correct): correlate ToolResult.ToolUseID back
  to the ToolUse that produced it. Background shell = Bash with
  Input["run_in_background"]==true; async agent = the Agent tool. No amount of
  text in a tool_result can fabricate a matching tool_use block. Events already
  carry ToolUses{ID,Name,Input} and ToolResults{ToolUseID}; observe() walks
  events in order, so the tracker can carry a bounded toolUseID->tool map.
BLOCKED again — surfacing to Michael. This is a design change, not a fix
  round, so I am not dispatching it on my own authority.

Michael chose the structural fix. Brief at structural-fix-brief.md; landed as
  5350953 (opus), re-review dispatched (opus, adversarial).
  Verified empirically by the implementer, not guessed: all 1648
  "Async agent launched" results across every transcript on this machine come
  from a tool_use named exactly `Agent`. The only OTHER producers of that text
  are Read (23), Bash (18), AskUserQuestion (1) — precisely the false-positive
  class the gate now excludes. No `Task`-style dispatch name exists here.
  NEW ACCEPTED FALSE NEGATIVE: 40 of 803 genuine shell launches carry no
  run_in_background flag — Claude Code auto-backgrounds a Bash that exceeds
  its timeout. Those are structurally identical to 21 foreground greps that
  merely quote the sentence, so they cannot be told apart without going back
  to text-only detection. Trade-off is the right way round (a missed launch
  degrades to the pre-branch bug; a false positive hides a session), and it is
  documented in the spec. Re-reviewer asked to challenge the claim.
  Testdata: two real transcript excerpts now committed under
  cmd/claudemux-head/testdata/. Content checked — claudemux's own traffic, no
  business content; does embed /Users/michael paths and a session UUID in a
  public repo. Flagged to Michael; sanitize on request.

Structural-fix re-review (opus, adversarial): BLOCKER CLOSED.
  Corpus replay over all 324 main transcripts, 2076 structurally-confirmed
  launches: 0 false positives, recall 2048/2076. 112 quoting-shape x carrier
  combinations inert, including shapes the reviewer invented (a session
  cat-ing a transcript so the payload contains a COMPLETE tool_use block —
  inert because extractContent is non-recursive). Test quality verified by
  four mutations, each caught by a specifically-named test.
  Out-of-scope files confirmed untouched; no existing assertion altered.

Two open items, both surfaced to Michael:
  1. Important residual — the AGENT leg still gates on a text substring, so a
     foreground Agent result quoting the payload registers a phantom launch;
     10 of 16 shapes fire under that carrier, including the whole spec and
     plan docs. Never observed in 324 real transcripts (async agent reports
     arrive as task-notifications, not Agent tool_results), but it is the same
     class of bug this branch exists to kill.
  2. My relayed claim was WRONG. The implementer said the auto-backgrounded
     Bash false negative was structurally unfixable; the reviewer disproved it.
     `toolUseResult.backgroundTaskId` is a harness-written sibling field on the
     tool_result entry: 821/821 genuine background shells carry it, ZERO of the
     26 foreground greps quoting the sentence do. Same on the agent side:
     `toolUseResult.isAsync/agentId` on 1555/1555. Using those would remove
     BOTH regexes, close residual 1, and recover the misses — the reported
     "40 of 803" also understates it, since 66 timeout-/user-backgrounded
     launches match no pattern at all.
  The spec's limitation paragraph currently justifies itself with the
  disproved claim and must be corrected whichever way Michael goes.

Michael chose the harness-signal round. Brief at harness-signal-brief.md;
  landed as 1290b74 (opus). Re-review dispatched (opus, adversarial).
  Field names verified over 1915 transcripts: backgroundTaskId string x821
  (all Bash, 0 FPs); isAsync bool x1596 (always true, always with a non-empty
  agentId, all Agent, 0 FPs). No stop-and-report condition.
  Deletion is real in production code: bgwork.go 233 -> 136 lines. Both id
  regexes, the launch-sentence marker, the tool-identity gate and the pending
  tool_use map are all GONE, not layered over.
  Two facts nobody had: `toolUseResult` is sometimes an ARRAY (2728 entries),
  and a foreground Agent never writes an object toolUseResult at all — which
  is precisely why the agent leg now needs no text check.
  Fixtures: 3 new ones derived from employer transcripts with commands, cwd,
  branches, slugs and all session/message/request ids replaced; only the
  opaque backgroundTaskId values are real. I independently grepped testdata/
  for employer terms — clean.
  Open question sent to the reviewer: two new fixtures are isSidechain:true,
  but the head tails only MAIN transcripts, so the "recovered" launches may be
  largely unreachable in its real input. Asked for a quantified answer, since
  it changes what this round was worth.

Harness-signal re-review (opus, adversarial): READY TO MERGE.
  Main-session corpus (the head's actual input): 325 files, 351,795 lines,
  2079 truth -> precision 2079/2079, recall 2079/2079, zero FP, zero FN.
  225 carrier x shape combinations inert, including a forged transcript line
  carrying a REAL backgroundTaskId as text, an MCP result with the fields
  nested one level down, and the real Read toolUseResult shape.
  Ground truth shown non-circular: only 6 main-corpus entries have launch-
  looking TEXT without the field, and all 6 are quoted payloads from this
  design session — true negatives that text detection would have fired on.
  All 16 foreground Agent results write a plain-string toolUseResult, which
  is why the agent leg needs no text check.
  Deletion confirmed: zero non-test hits for any deleted symbol; bgwork.go
  137 lines, one regex left (the completion <task-id> extractor).
  Fixtures: reviewer located the ORIGINALS and diffed them. Two genuine leaks
  (an employer 1Password account, a beejax k8s deployment) are gone; key sets
  byte-for-byte faithful. Zero employer-term hits.
  REACHABILITY ANSWERED: 0 of 351,795 main-transcript lines are sidechain, so
  the two isSidechain fixtures are lines the head cannot receive — but their
  SHAPES are reachable (3 auto + 23 timeout + 2 user in main transcripts).
  Recovery is real but small: +28 launches, 1.35%. Asking was worth it.

Cleanup round dispatched (sonnet) for 4 items the reviewer flagged:
  1. The spec's "wording drift" limit is now FALSE — detection reads no launch
     string. This is exactly the sentence that would send the next reader back
     to the text path that failed three times.
  2. The spec's "Parser changes" section still says two fields; it is three.
  3. Corpus-wide counts quoted as if they were the head's input, plus a
     double-count in bgwork_test.go (102 is the whole no-flag bucket; auto
     alone is 36 corpus-wide, 3 in main).
  4. The `if res.IsAsync` guard survived mutation — untested. One table case.
Cleanup landed as 9936a5b; the IsAsync guard is now mutation-covered.

FINAL LIVE VERIFICATION on the installed binary, head %179, session claudemux.
  Fed this session all three phantom shapes at once, then ONE real launch:
    - a Bash result starting at byte 0 with the quoted shell payload (the
      shape that fired on round 2)
    - a Read result carrying both payloads verbatim (rounds 1 and 3)
    - one genuine background task
  Result at t+30 and t+55: @claudemux_state = Background:1, head reads
  "● Working 1". Exactly the real launch; zero phantoms.
  Round 2 would have counted 2 here, rounds 1/3 would have counted 2-3, and
  pre-branch code would have said Idle while work was running.

BRANCH COMPLETE. 18 commits, pushed. Suite green (4 consecutive full runs;
  the only failure seen this phase was the known env_test.go flake).

Also worth doing, not blocking:
  - Spec drift: the design doc (lines 89-94) still documents the ORIGINAL
    unanchored regexes, so it now misdescribes the shipped code.
  - bgShellRe now requires the literal "Command " lead-in, narrower than
    "anchor to line start"; a harness rephrasing silently reverts to the
    old false-negative behavior.
