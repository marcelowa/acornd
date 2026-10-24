# ADR 0028 — A sandbox stays while its worker is attached

Status: accepted (2026-10-28), built

## The problem

A bwrap workspace is one long-lived sandbox per workspace ([ADR 0023](0023-bwrap-engine.md)), and
the supervisor inside it stops itself after `--idle-timeout` with nothing running — thirty minutes
by default, from the policy. It exists as the net under a gateway that died, and
[ADR 0024](0024-one-lifecycle.md) says both engines answer "stop when idle" the same way: the
microVM engine stops an idle sandbox and its worker wakes it with `connectOrStart`; bwrap was
supposed to be the same case.

It was not. The timer counts **commands**, and a worker can sit attached for hours with nothing to
run — a conversation that is open and quiet. When the window passed, the supervisor shut itself
down and removed its socket. Nothing woke it: only control can start a bwrap sandbox
(`bootArgv`), control provisions once at worker attach, and there is no path from a worker to
control.

The result, seen on `acornd` on 2026-10-28:

- `supervisor.log` said `19:30:39 idle for 30m0s: stopping the sandbox` — and `19:53:23` for the
  other workspace — while both workers had been attached since 19:23;
- `/run/user/1000/acornfall/<id>/s.sock` was gone, and `ps` showed no sandbox;
- `read`, `write`, and `edit` kept working (file operations are the host's, by design), the state
  frame kept saying `status: running` (it is computed at attach), and every `bash` call failed with
  `connect ENOENT`, followed by a sentence that asserted a lifecycle fact nobody had checked:
  *"the workspace's sandbox stops when the workspace is stopped"*.

A workspace nobody had stopped had lost its shell, permanently, with nothing in the interface
saying so.

## The decision

**1. The worker says it is still attached.** A `keepalive` request on the supervisor protocol, sent
when the client opens and every five minutes after. The supervisor counts it as activity, exactly
as it counts a command in flight. The sandbox's own timer then only fires when both the worker and
the gateway are gone, which is the case it was ever for.

The window is thirty minutes and the heartbeat is five, so four beats are missed before a stop —
and a stop is not permanent: control starts a stopped sandbox at the next worker attach.

**2. A `ping` is not a keepalive.** `ping` asks whether the sandbox is up; the driver and the
status probes use it. If it counted as activity, control merely looking at a sandbox would keep it
alive forever and the net under a dead gateway would never fire. The two requests mean different
things, so they are different messages.

**3. The error says what to do.** The worker cannot start a bwrap sandbox, so the message is no
longer a guess about the workspace's lifecycle: *"the sandbox is stopped, and restarting acornfall
starts it again"* — which is true, because a restart re-attaches every worker and control starts
what is stopped.

### Alternatives rejected

- **Control starts the sandbox on demand.** There is no channel from worker to gateway; adding a
  request/response over the worker protocol is a bigger change than the heartbeat, and control
  cannot see a tool call before the worker already needs the sandbox.
- **Drop `--idle-timeout` entirely.** One line, but it throws away the net: a gateway that crashed
  would leave a sandbox running until the next start. The timer's purpose survives; only its
  trigger was wrong.
- **The worker boots the sandbox itself.** The boot argv — image copy, mounts, pasta, the
  supervisor path — is control's, and re-deriving it in the worker is a second implementation of
  the sandbox, which is how the two sides drift.
- **Control restarts the sandbox when the state is read.** A state read is a poll, not a tool call;
  making it boot sandboxes would start them for a tab nobody is using, and the state is not where
  a lifecycle belongs.

## Consequences

- A bwrap sandbox stays warm while its workspace is open, even with no command in it, and stops
  when the worker is evicted (control's idle rule) or when both worker and gateway are gone for the
  window.
- The cost is one Unix-socket connect per five minutes (under a millisecond) and a sandbox held
  while a workspace is open — which is what "one long-lived sandbox per workspace" already meant.
- The microVM path is unchanged: its engine stops idle sandboxes and the client wakes them. Both
  engines now keep tools working across an idle window, by different mechanisms.
- **A sandbox created by an earlier binary does not know `keepalive`** and answers "unknown
  request". The client ignores that, so such a sandbox still idles out; it is recreated with the
  new supervisor the next time it is started (a restart of acornfall, a stop, a secret change).
  An install that updates while a sandbox is alive keeps the old supervisor until then.
- **Not fixed here:** the state frame's `sandbox.status` is still computed at attach, so a sandbox
  that dies for another reason (an OOM kill, a foreign signal) can read `running` while it is gone.
  A reconcile that refreshes it belongs with the state work, not with this timer.

## Where it is checked

- `TestAKeepaliveKeepsTheSandboxAwake` — a keepalive every 100 ms holds a 300 ms window open.
- `TestAPingDoesNotKeepTheSandboxAwake` — pings do not.
- `worker/test/bwrap.test.ts` — the client sends its first keepalive when it opens, and the
  refusal names the remedy instead of guessing.
