# RegisterControlCallback never reaches a modular input controller

**Component:** `components/input` (client and server), module hosting
**Version observed:** `go.viam.com/rdk v1.7.0`, viam-server on darwin/arm64
**Severity:** a modular `input_controller` cannot be consumed by anything.
Unary calls work; every streaming subscription silently receives nothing.

## Summary

`RegisterControlCallback` against an input controller **provided by a module**
returns `nil` but never reaches the module's component. The consumer receives
no events, no error is logged anywhere, and the only events it does see are the
`Connect`/`Disconnect` the RDK client synthesises locally.

Unary methods on the same component over the same connection work correctly,
so this is specific to the `StreamEvents` subscription path crossing a module
boundary.

The same consumer code works against a **builtin** input controller
(`rdk:builtin:fake`), because a builtin resource is handed to an in-process
consumer directly with no client, server or stream involved.

## Reproduction

1. Configure any modular `input_controller`. Ours is a keyboard controller
   whose events are injected with `TriggerEvent`; any module-provided
   controller that emits events should do.
2. Restart viam-server so no stale client state is in play.
3. Run a plain external client that subscribes to every control
   (`examples/streamtest` in this repo):

   ```sh
   go run ./examples/streamtest --host <machine> --key-id <id> --key <key>
   ```

4. Generate events on the component.

No consumer module is needed. This reproduces with one external client against
a freshly restarted server.

## Observed

`streamtest` output, trimmed:

```
resolved "keyboard-input" as *input.client
Controls() -> [AbsoluteHat0X AbsoluteHat0Y ButtonLT ButtonRT ButtonWest ButtonEast ButtonEStop]
Events() -> 7 entries
registered AbsoluteHat0X
registered AbsoluteHat0Y
... (7 total, every call returned nil)
subscribed. press keys in the web page; ctrl-c to stop.
1 events received so far
1 events received so far          <- for 30+ seconds while events were generated
^C stopping after 1 events
```

The single event is `AbsoluteHat0X Connect`, synthesised client-side by
`sendConnectionStatus` when the first stream opens. The seven `Disconnect`
events printed at shutdown are likewise synthesised locally. **Nothing arrived
over the wire.**

Meanwhile the module logged, at the same timestamps, that it was emitting
events normally, and logged **zero** incoming `RegisterControlCallback` calls.
The component's own `RegisterControlCallback` is instrumented and is never
entered.

## Expected

The subscription reaches the module's component, and events generated on that
component are delivered to the consumer's callback.

## Analysis

For a modular resource there are two nested client/server pairs:

```
consumer's input.client
  -> viam-server's input serviceServer
     -> viam-server's input.client over the module's SharedConn
        -> module's input serviceServer
           -> the component
```

viam-server builds the middle client with the registered `RPCClient`
(`module/modmanager/manager.go:586`), and the module serves the full API on its
own gRPC server (`module/resources.go:228,247`). `SharedConn.NewStream`
delegates to the socket connection, and the module server installs stream
interceptors with no timeout among them, so nothing structurally forbids
streaming into a module.

The failure is a lock-ordering livelock in `components/input/client.go`. It is
not a hard deadlock: the parked call does eventually return `ctx.Err()`. It
only presents as permanent because, across a module boundary, the caller is
viam-server's `StreamEvents` handler and the context is the consumer's stream,
which outlives everything.

`RegisterControlCallback` takes `c.streamMu` and holds it, via `defer`, across
`checkReady` (`client.go:182-183`, `:209`):

```go
c.streamMu.Lock()
defer c.streamMu.Unlock()
...
c.streamRunning = true
utils.PanicCapturingGo(func() { c.connectStream(closeContext) })
if err := c.checkReady(ctx); err != nil {   // polls streamReady until ctx dies
    return err
}
```

`connectStream`'s exit path also needs `c.streamMu` (`client.go:217-228`):

```go
defer func() {
    c.streamMu.Lock()
    defer c.streamMu.Unlock()
    ...
    c.streamRunning = false
    c.streamReady = false
}()
```

If `connectStream` returns while a `RegisterControlCallback` is parked in
`checkReady`, its deferred cleanup blocks on `streamMu`, `streamReady` is never
set, and `checkReady` spins until its context is cancelled. `checkReady`'s
context here is `server.Context()`, the consumer's stream, so it effectively
never returns.

The window is wide open in practice because the consumer's client **restarts
its stream on every registration** (`client.go:191-200`: set `streamHUP`,
cancel the stream, return immediately). Subscribing to seven controls tears
down and rebuilds the stream seven times in a few hundred milliseconds. Each
restart starts a new server-side `StreamEvents` handler, and each handler calls
`RegisterControlCallback` on the one shared module client before it begins
forwarding (`server.go:153-160`). So there are repeated opportunities for a
`connectStream` teardown to coincide with a `checkReady` wait.

Once that happens, viam-server's `StreamEvents` handler is blocked inside
registration and never reaches its forwarding loop. From the consumer's side
everything looks healthy: its own client-side stream opened, so `checkReady`
succeeded and `RegisterControlCallback` returned `nil`. Nothing errors. Events
simply never arrive.

This also explains the builtin `base_remote_control` service hanging during
startup against the same component: as an in-process consumer it calls the
blocking path directly, with no stream of its own to mask it.

### Runtime evidence

Captured from a test that reproduces the stall in-package, at its timeout dump:

```
goroutine 133 [sync.Mutex.Lock, 3 minutes]:
go.viam.com/rdk/components/input.(*client).connectStream.func1()
	components/input/client.go:219
go.viam.com/rdk/components/input.(*client).connectStream(...)
	components/input/client.go:266

goroutine 132 [select]:
go.viam.com/rdk/components/input.(*client).checkReady(...)
	components/input/client.go:148
go.viam.com/rdk/components/input.(*client).RegisterControlCallback(...)
	components/input/client.go:209
```

`connectStream`'s teardown is blocked acquiring `streamMu`; the registration is
spinning in `checkReady` holding it.

### A second, independent deadlock

Fixing only the above surfaces a genuine hard deadlock that the old
serialisation kept narrow. `sendConnectionStatus` (`client.go:354-355`) takes
`c.mu.RLock()` and holds it via `defer` while calling `execCallback`, which
takes `c.mu.RLock()` again. `sync.RWMutex` is not reentrant: once any goroutine
is blocked in `mu.Lock()`, new readers queue behind that writer, so the inner
`RLock` waits on a writer that waits on the outer `RLock`.

```
goroutine 8 [sync.RWMutex.RLock, 9 minutes]:
  input.(*client).execCallback          client.go:375
  input.(*client).sendConnectionStatus  client.go:370
  input.(*client).connectStream         client.go:332
goroutine 341 [sync.RWMutex.Lock, 9 minutes]:
  input.(*client).RegisterControlCallback  client.go:226
```

This is latent on unpatched main as well. Any fix for the first issue must
address this too, or the hang moves rather than disappears.

## Suggested fixes

**1. Do not hold `streamMu` while waiting in `checkReady`.** This is the
deadlock proper. Release the mutex before waiting, or narrow it to the state
mutation it is protecting. `connectStream`'s teardown should be able to publish
`streamRunning = false` without contending with a waiter.

**2. Snapshot before dispatching in `sendConnectionStatus`.** Collect the
control names under the read lock, release it, then invoke the callbacks. This
is required for fix 1 to hold under `-race`.

**3. No server change appears necessary.** `server.go:153-160` registers
synchronously before forwarding, which is what makes the stall invisible. But
with the client fixed, a registration that cannot proceed returns promptly and
`server.go:161` already propagates it as a stream error, so bounding the
handler's wait separately would be redundant.

**4. Deregister when a stream ends.** `StreamEvents` registers a `ctrlFunc` and
never removes it when the handler returns, so a dead subscriber keeps its
registration indefinitely. Combined with fix 5 this is what makes state
accumulate across reconnects.

**5. Allow more than one subscriber per control.** Both the client
(`client.go:171-178`) and typical server-side implementations keep a single
callback per control and event type, so a second consumer silently displaces
the first, which then receives nothing. Since viam-server holds exactly one
client per modular resource shared by all consumers, this displacement happens
inside viam-server and cannot be worked around by the module. Appending
subscribers keyed by the registering context, as `components/input/fake` does
with its slice, fixes this.

## Workaround

`Events()` is unary and works across the module boundary. A consumer that only
needs current state can poll it on whatever interval it already runs, instead
of subscribing. That is what we did in `arm-remote-control`: its movement loop
already ticked at 10Hz and only ever read the latest value per control.

## Status

A proof-of-concept patch exists on branch `fix/input-client-streammu-deadlock`
in a local RDK clone, as two commits so the two bugs can be evaluated
separately. The files it touches are byte-identical to both `v1.7.0` and
`upstream/main`, so it applies cleanly upstream.

Verified at package level: two new tests hang before the patch and pass after,
`go test ./components/input/... -race -count=5` is clean, and
`./services/baseremotecontrol/...` passes.

Not yet verified end to end. Nobody has run the reproduction above against a
viam-server built with the patch. Fix 5 lives inside viam-server's single
shared client per modular resource, so it remains possible that the hang is
fixed and a consumer still sees nothing for that separate reason.
