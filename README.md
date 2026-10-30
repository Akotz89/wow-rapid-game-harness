# Rapid in-game test loop

From the repository root:

```sh
scripts/wow-test check    # Offline contract tests; no realm connection or dependency fetch.
scripts/wow-test prepare  # Explicitly create/reuse the one ordinary dedicated fixture.
scripts/wow-test smoke    # Four native gameplay cases, restore origin, one acknowledged logout.
scripts/wow-test run      # Full five-case suite, including logout/relog persistence.
```

No argument remains `check`. Smoke runs Login, ChatEcho, NativeMovement and
LearnedSelfCast, then parent cleanup restores the starting position and requires
one native logout acknowledgment. Full `run` also tests LogoutRelogPersistence
and normally waits for two native logout acknowledgments. Smoke does not prove
relog persistence. On October 15, 2026, measured whole commands took 25.95 seconds
for smoke and 46.88 seconds for full run (Gameplay parent: 25.43 and 46.36 seconds).
These are single native runs on the current realm, not a latency guarantee.
Receipts: `research/evidence/cpi-388-fast-smoke-20261015/README.md`.

The default private config location for prepare/smoke/run is
`work/rapid-game-harness-20261012/private/fixture.json`. Set
`WOW_TEST_CONFIG` to an absolute alternative private JSON file when required.
No credential value has a default. Direct Go live tests require that environment
variable. The config contains exactly auth_addr, username, password, character;
accounts start RH and characters Rh. It must be a regular file owned by the
current user with no group/other permissions. Native passwords have a
16-character maximum. Invalid existing configs fail before locks, provisioning,
dependency fetching, or connections. New prepare destinations must be under the
task private directory.
FIFO/symlink inputs and files larger than 4096 bytes are rejected. Both live profiles freeze
the validated descriptor contents into one mode0600 snapshot under the ignored
task private directory. Go, remote verification and redaction all use that same
snapshot, even if the original config is replaced. The original stays untouched;
success, failure and TERM/INT/HUP remove the snapshot after children exit.
The snapshot fingerprint must match remote verification before LoginBot.

Check is safe with no config. If WOW_TEST_CONFIG is supplied, check validates
that file first. All operations acquire the same nonblocking serialization lock
and get a unique task work/run directory. After short local path/mode checks and
an empty snapshot reservation, a fresh timed worker is bounded to 120 seconds
with forced termination two seconds later, regardless of inherited guard values.
The public script PID supervises that separate process group: TERM/INT/HUP promptly
initiate group termination, wait for escalation if needed, remove only its owned
snapshot and return 143/130/129. Cooperative deadlines return 124; forced deadlines
may return 137. A rejected operation, failed test, or timeout exits nonzero.
The native observer stays in that worker group. Its four-second context sends
TERM to Python, whose CLI handler lets subprocess cleanup kill and reap SSH;
importing the remote module leaves signal handlers unchanged.
Signal the public script PID; signals sent directly to its internal Bash worker
can remain deferred while Bash waits for a foreground child. Snapshot cleanup is
not guaranteed after supervisor SIGKILL, host failure or escaped process groups.
An externally stopped timeout monitor cannot process its deadline until resumed;
public cancellation resumes the owned group so termination can finish.

Full `run` asserts five cases; smoke runs the first four:

1. Login reaches native in-world phase; name/GUID and initial map/position agree
   with the authenticated API. API state independently requires level 1 and
   positive health. The server-owned manifest and ordinary account identity are
   verified before every authentication attempt, including relog.
2. A random SAY nonce returns in SMSG_MESSAGECHAT from the exact fixture GUID.
3. Forward/stop movement changes the API position 0.7–1.3 meters on the same map
   and reaches the commanded endpoint within 0.15 meters, using the initial
   native position and facing. Wrong-direction movement fails.
4. The already learned Lesser Heal (2050), count 0, completes with a validated
   native SPELL_GO binding both caster GUIDs and the explicit unit target to self.
   A self-target immune/no-hit GO proves cast completion; it does not prove a
   landed healing effect or health gain. Matching native interruptions and
   CAST_FAILED responses preserve their rejection reason, including reason 0.
5. Native logout completion arrives; a fresh session has the same name/GUID,
   level and map, with position within 0.15 meters of the accepted movement.
   Parent cleanup then returns to the API-observed starting position and asserts
   native arrival before the final acknowledged logout saves that position,
   avoiding cumulative drift across repeats.

The scenario parser supports GO flags 0x100 with optional POWER_LEFT_SELF (0x800,
including its required u32 power trailer), NO_GCD (0x40000) and PENDING (1).
It checks raw-u64 hit/miss lists (including reflect's extra result byte), requires
exact target mask 2 and a packed self GUID, and exhausts the supported GO and
SPELL_FAILURE layouts. Other flags/target layouts are ignored by this narrow
scenario parser. CAST_FAILED requires its six-byte spell/count/reason base;
optional native reason arguments still mean rejection. Count 0 is reused by the
client, so an otherwise identical stale self-cast receipt is not distinguishable.
Validated native spell, self-target GUID and count are logged after native identity
also passes. The raw hook exclusively supplies these receipts.

No result is inferred from a packet write, local position mutation, or silence.
Missing observations, Warden rejection, wrong identity, or native failure fail
the suite. Subsequent cases report failed prerequisites when login/movement
fails. After an attempted movement from a verified origin, parent cleanup reads
fresh authenticated native state and returns the fixture when its identity, map,
level, health and displacement (at most two meters) are still safe. Already at
the start is a no-op. Recovery failure preserves the original test failure and
adds its own error; cleanup still attempts logout and always closes its owned
session. Logout itself requires SMSG_LOGOUT_COMPLETE. A hard timeout terminates owned
processes and sockets; native logout/save acceptance after an abnormal termination
must be checked before the next run.

Every plain, encrypted and auth packet write has a checked four-second socket
deadline, refreshed for each packet. A deadline-setting error, write error or short
write closes that stream and signals stop; later sends reject it without retrying
or reusing its cipher state. Close can therefore finish after a blocked write's
deadline. An auth-response send failure aborts before enabling encryption or
advancing session phase. Terminal read failures also close the socket, invalidate
later sends and preserve the first transport cause. A terminal disconnect wakes
login immediately; initial challenge read/parse failures retain their cause too.
`StopError()` snapshots the stopped bit and stop channel under the transport lock;
it returns nil when no stop is known (including before connecting), the first
retained cause after a failure, and a generic error after bare Close. Session phase
and character enum/create waiters reject stale or queued receipts when that
snapshot is terminal, including the interval before Close publishes its stop
channel. Setup writes are checked; failed sends and terminal waits wrap the first
cause with their stage. Known terminal streams stop character selection promptly
without another enum/create/login attempt. Healthy silent streams keep their
15/20/15-second enum/create/post-create deadlines, and an unknown create outcome
still requires re-enumerating the exact requested name on the next call. A healthy
session still requires the native login receipt and retains its existing timeout. Logout succeeds only after native
SMSG_LOGOUT_COMPLETE; a bare Close is an error, terminal transport errors retain
their cause, and a received acknowledgement takes precedence over stop. The live
helper uses this shared waiter with its existing 25-second missing-receipt limit.
These transport guarantees do not prove gameplay or a saved logout.

Each Go subtest has pass/fail and elapsed seconds in `tests.jsonl`. Both live
profiles require every selected child, the Gameplay parent, the exact-name policy
root and the package pass receipt. Coverage validates original Action/Package/Test
metadata before redaction; all retained JSON values still get redacted. Invalid
JSON, non-object events, invalid receipt fields, missing passes, failures and skips
fail even if Go exits zero. Smoke also rejects unexpected Gameplay
children, including persistence. Child passes alone cannot hide a parent cleanup
failure. The outer limit remains 120 seconds, Go's limit 110 seconds, and each
native logout acknowledgment deadline 25 seconds.

Check also runs
all Python transport, runner, synthetic dependency and movement-recovery
regressions, retaining their output in `remote-tests.txt`
and returning failure when they fail. Synthetic full/smoke cleanup matrices use
four workers with isolated disposable state per case; assertions stay on the
coordinator thread. Every case retains its 40-second Go and 45-second subprocess
limits, including real 25-second missing-logout waits, and each matrix joins its
workers before fixture cleanup. Upstream
diagnostic account/password strings are redacted while streaming, before stdout
or evidence retention. The authenticated API token stays on server-01.
Both live profiles also execute `TestPinnedExactNameCreationPolicy`, an offline AST check of
the actual pinned dependency's requested-name creation path, without fetching.

## Dependency and build

Only the guarded regular-PC WSL2 host compiles. Go 1.26.9 resides in the task work
root; the runner validates its version. Before any Go invocation, it disables
user Go defaults (`GOENV=off`), ambient flags (`GOFLAGS` empty), workspace
replacement/discovery (`GOWORK=off`) and automatic toolchain switching
(`GOTOOLCHAIN=local`). This keeps overlays and workspace replacements from
substituting source after provenance checks. These settings apply only to the
runner and its children; caller settings and user configuration files stay intact.
Bootstrap checks out upstream
AzerothGhost 34b756fce55ed82decb737bd9b808d7aec4e537b into ignored
`tools/rapid-game-harness/.deps/azerothghost`, preferably sharing the inspected
task clone's Git objects. It applies only the retained auth OS byte-order and
exact requested-character-name patches, plus the retained world transport and
client session phase patches.
Bootstrap independently materializes the whole pinned tree plus those patches
and compares actual files, executable modes
and symlink targets. Ignored untracked inputs and tracked modifications hidden
by index flags fail before compilation; unexpected content is preserved.
Do not prune the inspected clone while a shared clone references it.
Character creation tries only the requested name. A collision or missing create
receipt fails; the next run re-enumerates that same name instead of creating an
alternate actor.
go.mod also carries upstream's detour replacement; go.sum preserves upstream's
published dependency checksums. The current protocol/harness imports use only
the standard library, so offline checks use a generated dependency-free modfile.
The offline transport regressions require that exact local pinned checkout and the
local Go 1.26.9 toolchain; missing inputs fail with a prerequisite error. They run
bootstrap only against that existing checkout to verify/publish approved patches,
then test the actual client package through a temporary Go overlay with in-memory
peers. They never fetch dependencies, use credentials or connect to the realm.
Real four-second blocked-write tests overlap so they add roughly four seconds.
The same actual-client compile tests StopError, phase/receipt arbitration, initial
challenge failures, auth queues and first-cause retention. `test_session.py` extracts
the current complete patched LoginBot function byte for byte into a disposable
stdlib-only module, with fake authentication and client boundaries. Its synctest
matrix checks prompt terminal returns, no later sends, setup/send failures,
unchanged virtual 15/20/15-second deadlines, exact-name creation and case/GUID
isolation. Required individual pass receipts prevent missing cases from silently
passing. These LoginBot tests prove orchestration against synthetic boundaries;
they do not prove the real client's synchronization or native gameplay. Actual
client tests supply synchronization evidence; smoke/full runs supply native
acceptance. All tests reuse the existing Go cache and remove disposable inputs.

For a missing Go install, recover the pinned task-local toolchain below from the
repository root. This holds the harness lock and validates the replacement before
moving an existing install. A failed download/check leaves the existing install
intact; retain a failed recovery directory for inspection.

```sh
scripts/wow-build-host-guard || exit 1
task="$PWD/work/rapid-game-harness-20261012"
mkdir -p "$task" || exit 1
exec 9>"$task/harness.lock" || exit 1
flock -n 9 || exit 1
recovery="$(mktemp -d "$task/go-recovery.XXXXXXXX")" || exit 1
curl --fail --location --output "$recovery/go.tar.gz" https://go.dev/dl/go1.26.9.linux-amd64.tar.gz || exit 1
printf '%s  %s\n' 42d158b4d8f7b61ac0a830567c940a86098fb7aac52e467a5ebec03ef5cc2f8d "$recovery/go.tar.gz" | sha256sum --check - || exit 1
tar -xzf "$recovery/go.tar.gz" -C "$recovery" || exit 1
[[ "$(GOTOOLCHAIN=local "$recovery/go/bin/go" version)" == 'go version go1.26.9 linux/amd64' ]] || exit 1
if [[ -e "$task/go" ]]; then mv "$task/go" "$recovery/previous-go" || exit 1; fi
if ! mv "$recovery/go" "$task/go"; then
    if [[ -e "$recovery/previous-go" ]]; then mv "$recovery/previous-go" "$task/go"; fi
    exit 1
fi
rm -rf -- "$recovery"
flock -u 9
exec 9>&-
```

## Extend one scenario

Add another serial subtest inside TestLiveGameplay after its prerequisites,
register a race-safe hook **before** sending, and require a native receipt or
authenticated observation within a deadline. For example:

```go
t.Run("NativeIdentityAgain", func(t *testing.T) {
    requireSession(t)
    _, err := waitNative(c.Character, func(o observation) error {
        return identity(o, c.Character, session.GUID)
    })
    if err != nil { t.Fatal(err) }
})
```

Add an offline negative test for mismatches and absent results. Do not use
NewSolo, NewScenario, EnsureAccount, GM commands, DB cleanup, synthetic
items/XP, account deletion, or existing-player resets.

## Limits and recovery

This is real 3.3.5a protocol gameplay coverage on the retained server-01 realm.
It does not render a Windows client, validate addons, exercise graphical UI,
prove long-session Warden compatibility, or cover combat beyond one self-cast.
The host Windows client and retired nested/WSL realms are outside this tool.

The dedicated ordinary human priest persists across runs. Successful runs and
recoverable failed runs return to their starting position through native movement,
verified against fresh authenticated state. Recovery is limited to the same
name/GUID/map, level 1, positive health and a two-meter local area. Unexpected
identity/state is reported without speculative return movement. Failed outbound
start/stop writes immediately attempt a safety stop before later cases. Unresolved
movement requires another successful stop in cleanup even when near the origin.
Failed return start/stop writes also attempt a safety stop. Every safety stop
refreshes authenticated identity, map and bounded position first; unavailable or
invalid observations fail recovery without sending stale coordinates.
Packet writes and local coordinates never prove restoration.

SIGKILL/process death, exhausted operation deadlines, native/API/network outages,
or absence of a usable session after logout/relog can prevent recovery or a saved
return. Those runs can retain movement; never delete/reset the fixture during
cleanup. A failed test remains nonzero even when recovery succeeds. On failure, retain
sanitized JSON evidence, inspect native readiness with remote.py status, then
retry run using the same private config. Missing/malformed credentials require
explicit prepare/recovery, with no implicit account password reset.

`tests.jsonl` retains the original subtest failure and parent cleanup's native
`return_to_start_delta_m` or recovery error. `remote-tests.txt` records synthetic
tests of the actual live/shared Go orchestration with fake protocol/API boundaries,
including displaced failure, return errors and bounded-state refusals. Synthetic
tests use no real credentials or realm access. Sanitized live acceptance evidence
belongs under `research/evidence/` for CPI-388; generated checks and run outputs
stay under `work/rapid-game-harness-20261012/`.

The server serializes fixture operations with a five-second lock deadline and
publishes the complete manifest with file and directory fsync before account
creation. Busy operations fail safely and may be retried. Unknown account or
initial character creation outcomes reuse that exact durable identity. A recorded
account that disappears, changes id or gains privileges is rejected before SOAP.
The manifest binds the exact account, character name and native character GUID;
legacy fixtures bind their existing actor from read-only SQL. Missing legacy or
bound actors fail before LoginBot, and observation checks both SQL and API GUIDs.
Only a newly prepared fixture has pending first-character creation permission;
binding its first native GUID consumes that permission permanently. Prepare never
grants it to an existing legacy account or replaces a bound actor. External actor
changes racing between SQL verification and protocol login are outside an atomic
transaction; subsequent native observation still rejects an identity mismatch.

Task-owned temporary sources/caches and run evidence stay in the task or ignored
dependency directory. Coordinator owns retention and sanitized final evidence.
Remove only superseded run material after confirming no harness process holds
the serialization lock; retain the private fixture and rollback/evidence needed
by the governing CPI-388 issue. No realm/config/security changes or restarts are
part of this loop.
