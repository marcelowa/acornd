# ADR 0029 — A run ends when its command does

Status: accepted (2026-10-28), built

## The problem

Two agents were working in `acornd-dev-01` on 2026-10-28. One of them ran a command with a
sixty-second tool timeout:

```sh
mkdir -p /dev/net && mknod /dev/net/tun c 10 200; …; timeout 20 pasta -q … -- /bin/true
```

`timeout` puts the command it runs in its own process group. The stop button was pressed, the
conversation never moved again, and the agent was left with a shell that would not answer and no
way to say why. The transcript stopped at the tool call; the task table said
`pi.generation waiting`, `pi.tool running`, `abort_requested=1` on both.

What was actually there:

| | |
|---|---|
| the run's shell | PID 22591 **zombie**, process group 22591 |
| the process it started | `pasta` PID 22598, **process group 22596** (`timeout`'s own), alive 12 minutes |
| where its output went | `/proc/22598/fd` — fd 1 and fd 2 are the **write ends** of the run's pipes |

The kill had reached the supervisor and the supervisor had killed the shell's group. It could not
kill `pasta`: that process was in another group, on purpose, because `timeout` makes one. `pasta`
ignored the `timeout` SIGTERM, `timeout` exited, and the orphan kept the pipes open.

The supervisor then did what it had always done: `streams.Wait()` before `command.Wait()`. It waits
for both stdout and stderr readers to reach EOF, which needs every writer to close, and `pasta`
never did. So `command.Wait()` was never called: the shell stayed a zombie, the run stayed in
`s.runs`, and **no `exit` event was ever sent**.

The worker's side had no second line of defence. Its abort and its timeout both write a `kill`
frame and then wait for the `exit` event; there is no bound on that wait. So the tool call outlived
its own sixty-second timeout *and* the abort, and the turn waited forever on a promise that could
not settle.

The same shape was in the capability probe, one layer down: `probePasta` runs pasta through
`exec.CommandContext` with a ten-second timeout and `CombinedOutput`, which waits for a
pipe-holding grandchild too. That is why `TestTierEmptyNameIsWhatTheMachineCanRun` hung for two
minutes in the same workspace, and why the agent went looking for `/dev/net/tun` in the first place.

Killing the one orphan `pasta` on the host closed the pipes, the supervisor reaped the shell and
sent its exit, and the conversation was `running: false` within a second.

## The decision

**1. The supervisor owns its pipe read ends.** `os.Pipe` instead of `StdoutPipe`, so the write ends
go to the child as an `*os.File` (no hidden `io.Copy`, and `Wait` returns when the process does)
and the read ends stay the supervisor's. `finish` reaps first, gives the pumps a second to drain
what the command wrote (`pipeDrainGrace`), then closes the read ends even if an escaped process
still holds the write ends. The run ends, the zombie is reaped, and the client hears `exit`.

**2. The worker bounds a killed run.** After an abort or a timeout, `exec` waits `KILL_GRACE_MS`
(three seconds) for the exit it asked for; if it does not come, the call settles as stopped with
code 143 and the connection closes. An abort is an answer, not a request that a wedged sandbox may
ignore. This also covers a sandbox still running an older supervisor.

**3. A capability probe cannot hang a start.** `probePasta` sets `Command.WaitDelay` to a second:
a pasta that leaves a writer behind is a refusal in a second, not a two-minute wait for the writer
to exit.

### Alternatives rejected

- **Kill the whole session, not the process group.** That would take the escaped `pasta` too, but
  finding every member means walking `/proc` or a subreaper, and the run still has to end if a
  process cannot be found or killed. Ending the run must not depend on the cleanup succeeding.
- **`WaitDelay` with `StdoutPipe`.** `Wait` closes the read end as soon as the process exits, so
  output still in the pipe is lost. Draining for a bounded moment keeps it.
- **Only the client grace.** The turn would settle, but the supervisor would keep a zombie and a
  run forever, and every later command would find the sandbox half-broken.

## What it does not do

The process that left the run's group is **not killed**. It is outside the group the sandbox owns,
it stays in the sandbox until it exits or the sandbox stops, and pruning an arbitrary session from
the supervisor is a separate decision with its own risks. The run ends without it, which is the
part a turn cannot do without.

## Evidence

| test | before | after |
|---|---|---|
| `TestAKilledRunEndsEvenWhenAGrandchildHoldsThePipes` (supervisor) | no `exit` after 3s: *"the run never ended after it was killed: i/o timeout"* | exit with `stopped:"killed"` in ~1s |
| *"a command the supervisor cannot end is still reported as stopped"* (worker) | the test timed out after 5s | `code 143`, `stopped:"timeout"`, in 3s |
| `TestAPastaThatLeavesAProcessBehindIsRefusedNotHung` (probe) | 6.00s — until the holder exited | 1.2s |

Found on `acornd`, in `acornd-dev-01`, from the transcript, the `tasks` table, the zombie's
process group and `/proc/<pid>/fd`.
