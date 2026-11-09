# ADR 0009 — Liveness, keepalive, and worker recovery

Status: accepted, implemented
Date: 2026-10-04

## Problem

Two reported symptoms — "the interface disconnects too easily and does not come back",
and a rail marker nobody could interpret. The first turned out not to be a UI problem at
all.

## A dead worker looked exactly like an idle one

Reproduced by killing the harness process: every read the gateway serves comes from its
own cache, so `/api/state` and the event stream kept answering normally. The interface
showed a healthy, idle application with no backend. Only a call that had to reach the
worker failed, and nothing in the UI was making one.

So the gateway could not tell "quiet" from "dead", and neither could anything watching it.
Fixed at the source:

- **`Worker.OnExit`** — the process is reaped once, and its end is an event rather than
  something the next call discovers by failing.
- **`State.workerUp` and `State.workerError`** — the interface is told. The status pill
  reads "harness down" with the reason, instead of "idle".
- **A supervisor restarts it.** Work is committed before it is shown, so a fresh process
  resumes every run from its last commit; recovery re-reads the catalog, re-attaches the
  conversation a client is watching, and re-applies its agent. Measured at two seconds.

### The supervisor is one loop, not one per death

The first version started a restart loop from the death handler, so every failed attempt
was another death and another loop: attempts compounded instead of backing off. The
supervisor now owns the lifecycle and is the only reader of a single `workerExited`
notification; five consecutive failures back off to a sixty-second retry rather than
burning the machine on a crash loop.

`exec.Cmd.Wait` is called from a reaper goroutine, which is documented as unsafe while a
pipe is still being read, so the stdout reader remains the thing that ends; the reaper
only announces it.

## A resumed run looked idle again

Recovery resumed the work, but the rail did not hear it. On 2026-10-28 a worker was restarted
under a generation, `harness.resume()` picked the task up, and the resumed run wrote its first
entry: a `pi.system` prompt, which carries no message timestamp. Control's busy rule wants a live
run record **and** activity fresher than thirty minutes; with no timestamp the conversation read
as "never moved", so `/api/state` said `running: false` while the durable task was `running` and
the model was working. The update drain check reads the same list, so an update could walk over a
run that had just been recovered — and `--stop-runs` did stop it, as a run it never saw.

The rule was right and the record was wrong. The worker now dates a running conversation at the
listing when its newest entry cannot date it (`activityOf`, `worker/src/activity.ts`). The control
is unchanged: a record with no activity is still not busy once the run is over. The case is
`worker/test/activity.test.ts`; the reproduction is the resume itself.

## The stream had no heartbeat, and then had one nobody could see

An idle `text/event-stream` is indistinguishable from a dead one until something times
out, and a hidden tab or a sleeping laptop is exactly where a connection dies without an
error. The handler now sends `retry: 3000` and a **named `ping` event** every three
seconds, and the client:

- treats a ping as proof of life, which is the only event that arrives while nothing is
  happening;
- reconnects when the stream has been silent for eight seconds, because an `EventSource`
  that is dead without having fired an error otherwise leaves the display showing state
  that stopped being true minutes ago;
- reconnects and re-reads state on `visibilitychange`, `online`, `pageshow`, and `focus`,
  which is what a slept laptop or a dropped tailnet path looks like from inside the page;
- waits for the document to finish loading before opening the stream, since a stream torn
  down by the load itself is what the browser reports as an interrupted connection.

**The first version sent an SSE comment as its keepalive.** Comments keep the socket open
and fire no event, so the client's "time since last event" never advanced and the watchdog
*mould not fire at all*: a stream could be dead for hours while the interface showed
`idle`. It took freezing the server with `SIGSTOP` — the only way to make a connection
open and silent at once — to see it. A liveness signal has to be observable by the thing
that is watching for it, which a protocol comment is not.

Measured with the server frozen: detected after ~9 seconds, recovered 300 ms after it
resumed. With the server *gone*, the browser's own retry recovers within three seconds of
it returning, in both Chromium and Firefox.

Reconnecting is safe because every frame is a complete view and the server sends the
current state and view on subscribe: there is no delta to have missed.

## Two smaller consequences

- **Polling no longer competes with the stream.** Browsers allow a handful of connections
  per host, and a usage poll that overlaps a stalled request can hold one of them while
  the stream's retry waits. The poll now skips while the tab is hidden and never overlaps
  itself.
- **A cancelled request is not a failure.** The client replaces its stream on reconnect and
  the checks cancel one to probe liveness; the browser reports both as a failed request,
  so the checks ignore deliberate cancellations rather than treating them as errors.

## The rail marker was a third meaning for one shape

Conversations an agent created were marked with an amber dot. Dots already meant *running*
and *unread* in that rail, so a third colour with no label was three meanings for one
shape — the reported confusion was the design telling the truth.

Identity is text, and already was: the id renders as `1290 ·root`. A peer now renders as
`1290 ·peer`, in the same place and style. **Dots are for live state; text is for
identity.** The sender of a message is likewise named in words rather than encoded as a
colour.

## Verified

```
kill -9 the worker →
  gateway log:  worker died: the harness process ended: signal: killed
                worker restarted and resumed          (2s later)
  /api/state:   workerUp true
  browser:      idle

with restarts made to fail:
  /api/state:   workerUp false, "the harness process ended: exit status 1"
  browser:      "harness down", tooltip carries the reason
```

## What this does not do

- **No supervision of the gateway itself.** If the gateway dies, nothing restarts it; that
  is the supervisor of the supervisor and belongs to systemd or a container, not to us.
- **A restart loses in-flight streaming**, though not work: the last ~100ms of a partial
  answer is gone, and the transcript resumes from the last commit.
- **Recovery does not re-attach every conversation**, only the one a client is watching.
  Subagents running in the background are unaffected, because their tasks are durable and
  the worker resumes them on open.

## Amendment (2026-11-09): a quiet run is still a run

The rule above — a live record **and** activity fresher than thirty minutes — no longer
holds. It was there to stop a dead run's residue from keeping a lit dot, but the record is
committed state and a resume clears it; the residue it guarded against is a worker that never
came back, which the harness-down state already says. What the window did instead was hide a
**live** run as soon as it was quiet.

The case: a `bash` tool call started at 00:35 and hung inside the sandbox (a `youki create`
blocked in `__skb_wait_for_more_packets`, still there eight hours later). At 01:05 the window
expired; the conversation read `running: false`, the still-running tool card settled as **done**
(`ui/src/app/map.ts` settles a slot when the flag says the run is over), and Stop disappeared
from the composer. A message sent then was accepted by the harness — which still had the run —
as a steer, and sat in `pi.inbox` labelled "steering this turn" behind a boundary the hung tool
would never reach. There was no control left that could end the turn.

The interface now reads the record as it is and dims the dot on its own clock
(`ui/src/lib/clock.ts`, `STALE_AFTER_MINUTES`); the record's age is not a reason to take away
the only control that can end it. The prompt and builtin graces remain: they cover the click,
not the record. The update gate reads the same rule, so a quiet run is not swapped out from
under an update it could still finish during.
