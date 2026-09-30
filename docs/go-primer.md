# Go Primer

A complete, first-principles guide to Go, taught through this repository.

This is not a generic Go tutorial. Every concept is explained using a real file in this
codebase, so you learn the language and the system at the same time. When the Go team
introduces a feature, this is the reasoning behind it and where you already depend on it.

## Contents

- [How to use this document](#how-to-use-this-document)
- [Phase 0: Setup](#phase-0-setup)
- [Phase 1: The mental model](#phase-1-the-mental-model)
- [Phase 2: Types and data](#phase-2-types-and-data)
- [Phase 3: Abstraction](#phase-3-abstraction)
- [Phase 4: Errors](#phase-4-errors)
- [Phase 5: Concurrency](#phase-5-concurrency)
- [Phase 6: The HTTP layer](#phase-6-the-http-layer)
- [Phase 7: Idiom and testing](#phase-7-idiom-and-testing)
- [Phase 8: The architecture](#phase-8-the-architecture)
- [Reference index](#reference-index)
- [Not used yet](#not-used-yet)

---

## How to use this document

Read it in order. The phases are ordered by **dependency**, not by difficulty: you cannot
understand the concurrency chapter without errors, and you cannot understand the
architecture chapter without interfaces.

Each phase has three parts:

| Part | What it is |
| --- | --- |
| Concept | The idea, explained from first principles rather than from syntax |
| In this repo | The same idea in your code, with file references |
| Exercise | Something to change, so the concept sticks |

Line references drift as the code changes. Treat them as a search hint, not a contract.

| Phase | Topic | Time |
| --- | --- | --- |
| 0 | Get it running | 15 min |
| 1 | The mental model | 20 min |
| 2 | Types and data | 45 min |
| 3 | Abstraction | 45 min |
| 4 | Errors | 30 min |
| 5 | Concurrency | 90 min |
| 6 | HTTP | 60 min |
| 7 | Idiom and testing | 45 min |
| 8 | Architecture | 30 min |

---

# Phase 0: Setup

You need the Go toolchain. It is not on this machine yet.

```bash
# Linux
curl -fsSL https://go.dev/dl/go1.24.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xz
export PATH=$PATH:/usr/local/go/bin   # add to ~/.bashrc to persist

# verify
go version   # expect: go version go1.24.0 linux/amd64
```

Then, from the repo root:

```bash
go build ./...          # compile everything
go vet ./...            # catch suspicious constructs
gofmt -l .              # list files needing formatting (expect no output)
go test ./...           # run tests
go test -race ./...     # run tests with the race detector
```

### Run it

```bash
# terminal 1
DEPLOY_PLATFORM_ADDR=:8080 go run ./cmd/server

# terminal 2
curl -s localhost:8080/api/health
curl -s -X POST localhost:8080/api/projects \
  -d '{"name":"PayApp","repoUrl":"https://github.com/Jayesh-2404/PayApp"}'
```

Then open `http://localhost:8080`, create a deployment, and watch the simulated steps
stream in. That is the whole system: an HTTP request, a row in a JSON file, a background
goroutine, and a log fan-out to your browser.

### Tools to know

| Command | Purpose |
| --- | --- |
| `go build ./...` | compile, discard binaries |
| `go run ./cmd/server` | compile and run |
| `go vet ./...` | suspicious patterns (copied locks, printf bugs) |
| `go test -race ./...` | data race detection |
| `gofmt -w .` | format |
| `go doc net/http` | read stdlib docs locally |
| `go mod tidy` | prune and verify dependencies |

`go doc` is more useful than web search. `go doc encoding/json`, `go doc context`,
`go doc sort.Slice`. The stdlib is documented properly and it ships on your machine.

---

# Phase 1: The mental model

## Three ideas

Most of Go follows from three decisions.

**Everything is a value or a pointer to a value.** No hidden object graph, no
instantiation step, no `new` ceremony. `domain.Project{}` allocates a record. `&project`
is an address. That is the whole memory model.

**Errors are return values, not exceptions.** No `try`, no `catch`, no stack unwinding.
Every operation that can fail reports it in its return values, and nothing is hidden.

**Concurrency is a language primitive.** `go` and `chan` are keywords, not library calls.
Your entire worker and your SSE streaming are built from the standard library.

## Packages and the call stack

```go
package main

func main() {
```

`main` is not special in the language. `main` is a package name and `func main()` is a
function. The **toolchain** has one rule: for `package main`, run `main`. Nothing executes
before it.

This is more useful than it sounds. The call stack starts empty at `main()`. Every
goroutine, every request handler, every callback is reachable by tracing back from that
one line. In a language with event loops and dynamic dispatch, the call stack at the top
of main is frequently empty.

## Modules

```go
module deploy-platform

go 1.24

require github.com/lib/pq v1.12.3
```

`module deploy-platform` declares the import path root. That is why internal imports
start with `deploy-platform/`:

```go
import "deploy-platform/internal/domain"
```

`go 1.24` is a **language version lock**, not a suggestion. Feature availability is gated
on it. `internal/app/server.go` uses `"GET /api/projects/{id}"` routing and
`r.PathValue("id")`, which is Go 1.22+. Setting the version lower breaks the build at
compile time, with a message telling you which feature needs which version. This is Go
giving you forward compatibility guarantees for free.

`go.sum` holds cryptographic hashes so the toolchain can verify that the module you
downloaded is the one that was published. Supply-chain integrity is in the default
workflow.

## The internal rule

`internal/` is a compiler-enforced access boundary. Code under `internal/` is importable
only from within the same tree.

```txt
main.go imports deploy-platform/internal/store     OK   (same tree)
another repo imports .../internal/domain            FAIL (compiler error)
```

This is Go's answer to Java's `public`/`private`/`protected`. Instead of annotating every
symbol, **visibility is a filesystem property**. Your `domain` package has no visibility
ceremony in it, and it is still unreachable from outside the module.

## Package layout

```txt
cmd/server/main.go        the binary; composition root
internal/domain/          types only; no behaviour, no dependencies
internal/store/           interface + implementations
internal/executor/        interface + implementations
internal/worker/          the background state machine
internal/app/             HTTP handlers
```

`cmd/` is the community convention, used by Kubernetes, Docker, and Caddy. It means the
directory produces a binary named `server`. The `cmd/` parent lets one repo hold several
binaries later (`cmd/api`, `cmd/migrate`) that share `internal/`.

Note the dependency direction: `domain` imports nothing from this repo. Everything else
imports `domain`. `app` and `worker` both import `store` and `domain`, but never each
other. You will see why this matters in Phase 8.

**Exercise.** Delete `internal/app/server.go` temporarily. `go build ./cmd/server` fails
with an undefined symbol, and every other package still builds. That failure is the
compiler enforcing the boundary.

---

# Phase 2: Types and data

## Structs

```go
// internal/domain/domain.go
type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	RepoURL         string    `json:"repoUrl"`
	Branch          string    `json:"branch"`
	HealthCheckPath string    `json:"healthCheckPath"`
	LiveURL         string    `json:"liveUrl"`
	ActiveDeployID  string    `json:"activeDeploymentId"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
```

A struct is a fixed-layout record. Field offsets are known at compile time, which is why
Go structs are cache-efficient and why marshalling is fast.

## The zero value

This is the defining constraint of Go, and it is the thing that makes Go code short.

**Every type has a useful zero value.** `0`, `""`, `false`, `nil`, and a zero struct.
`var p domain.Project` is a valid, usable, empty Project. No constructor, no null check.

Look at what `CreateDeployment` omits:

```go
// internal/store/json_store.go
deployment := domain.Deployment{
	ID:        newID("dep"),
	ProjectID: projectID,
	CommitSHA: shortID(),
	Status:    domain.StatusQueued,
	StartedAt: now,
	// Error is "" and FinishedAt is nil, which is correct
}
```

The zero value is not a trap you must defend against. It is usually the right answer.
This is why you will rarely see a Go constructor that does nothing but call another
function.

## Pointers model "optional"

`time.Time` is a struct, so its zero value is a real timestamp: January 1, year 1. It
cannot represent "unset". This is a real problem, and Go solves it by making the field a
pointer.

```go
FinishedAt *time.Time `json:"finishedAt,omitempty"`
```

```txt
FinishedAt == nil     -> the deployment has not finished
FinishedAt != nil     -> *FinishedAt is when it finished
```

Compare the two fields in the same struct:

```go
Error      string     // "" is a perfectly good "no error"
FinishedAt *time.Time // nil is a perfectly good "not finished"
```

`string` needed no pointer because `""` is a valid "unset". `time.Time` needed one because
its zero value is not.

Setting it:

```go
// internal/worker/worker.go
now := time.Now().UTC()
deployment.Status = domain.StatusSuccess
deployment.FinishedAt = &now
```

`&` is the **address-of operator**. `now` is a local variable; `&now` is a pointer to it.
The value escapes its stack frame and lives on the heap. The garbage collector reclaims
it. You never free anything, and there is no `delete()`.

**`pointer = optional value` is the first Go design pattern worth memorising.** It recurs:
optional struct fields, `SubscribeLogs` returning a `func()` for cleanup,
`http.Flusher` type assertions.

## Defined types close a set of values

```go
type DeploymentStatus string
```

Compare with the alias form, which is different:

```go
type DeploymentStatus string    // defined type: a new, distinct type
type DeploymentStatus = string  // alias: literally the same type
```

You wrote the defined version. The difference:

```go
var s domain.DeploymentStatus = "queued"   // OK: untyped constant converts
var t string = "queued"
var u domain.DeploymentStatus = t         // COMPILE ERROR: mismatched types
var v domain.DeploymentStatus = "typo"    // COMPILE ERROR: not a valid status
```

This is how Go gets closed-set safety without generics. Mint a type for the enum, and the
compiler rejects anything outside it.

```go
const (
	StatusQueued      DeploymentStatus = "queued"
	StatusCloning     DeploymentStatus = "cloning"
	StatusBuilding    DeploymentStatus = "building"
	StatusDeploying   DeploymentStatus = "deploying"
	StatusHealthCheck DeploymentStatus = "running_health_check"
	StatusSuccess     DeploymentStatus = "success"
	StatusFailed      DeploymentStatus = "failed"
	StatusRolledBack  DeploymentStatus = "rolled_back"
	StatusRollback    DeploymentStatus = "rollback"
)
```

Typed constants are computed at compile time and inlined. Every comparison in `worker.go`
is type-checked.

Compare this to TypeScript, where `type Status = "queued" | "cloning"` gives the same
guarantees at the type level but has no runtime representation. Go's version is a real
value in memory, so it serialises and compares without helpers.

## Struct tags

```go
RepoURL    string     `json:"repoUrl"`
FinishedAt *time.Time `json:"finishedAt,omitempty"`
```

A struct tag is a raw string attached to a field. The compiler ignores it entirely. It is
read at runtime by `reflect`, by convention, by packages that know what to look for.

- `json:"repoUrl"` renames on the wire. Go would default to `RepoURL`; the browser wants
  camelCase. This is how Go code speaks the frontend's language without corrupting its
  own.
- `omitempty` omits the field when it holds the zero value. Combined with
  `*time.Time`, a running deployment simply has no `finishedAt` key rather than
  `"finishedAt": null`. The browser does `if (d.finishedAt)`, which is clean.

## Encoding and decoding

```go
// encode: Go -> JSON
func writeJSON(w http.ResponseWriter, status int, value any) {
	_ = json.NewEncoder(w).Encode(value)
}
```

```go
// decode: JSON -> Go
var input domain.CreateProjectInput
if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
	...
}
```

`any` is an alias for `interface{}`, added in Go 1.18 to make "a value of any type"
readable. The encoder walks the concrete type at runtime using reflection, so
`writeJSON` does not care what it writes. One function serves every endpoint.

`&input` is mandatory. **All Go arguments are passed by value.** Without the `&`, the
decoder mutates a temporary copy and your struct stays zero-valued. This is the same
address-of operator as `&now`. Once you internalise "Go passes copies", the `&` stops
being mysterious.

## Why `CreateProjectInput` exists

```go
type CreateProjectInput struct {
	Name            string `json:"name"`
	RepoURL         string `json:"repoUrl"`
	Branch          string `json:"branch"`
	HealthCheckPath string `json:"healthCheckPath"`
}
```

Decoding into a narrow allow-list means a client cannot POST
`{"activeDeploymentId": "dep_of_another_project"}` and have it bind to a `Project`. No IDs,
no timestamps, no status, no `Error` field.

This is not a Go feature. It is a security decision that struct tags made cheap: a narrow
input type is four lines and costs nothing at runtime. Note that the store applies its own
defaults on top:

```go
if input.Branch == "" {
	input.Branch = "main"
}
if input.HealthCheckPath == "" {
	input.HealthCheckPath = "/"
}
```

Validation and defaulting live in the store, not the handler. That is why the same rules
apply whether a project is created over HTTP or from a future GitHub webhook.

**Exercise.** Add `LiveURL string` to `CreateProjectInput` and POST a project with
`{"liveUrl":"https://evil.example.com"}`. It is ignored, because `CreateProject`
overwrites `LiveURL` with a computed value. Then add a field to `CreateProjectInput` and
confirm it never reaches the stored `Project`.

---

# Phase 3: Abstraction

## Methods and receivers

```go
func (s *JSONStore) GetProject(id string) (domain.Project, error) {
```

The part in parentheses before the name is the **receiver**. Go has no free functions with
a hidden receiver; a method is just a function that takes an extra argument.

## Value versus pointer receiver

| Receiver | Receives | Use when |
| --- | --- | --- |
| `(s JSONStore)` | a **copy** of the struct | small, immutable type |
| `(s *JSONStore)` | an **address** to the original | mutates, or the type is large |

Your entire store package uses pointer receivers, for two hard reasons:

```go
type JSONStore struct {
	mu          sync.RWMutex                              // 1
	path        string
	state       state                                     // 2
	subscribers map[string]map[chan domain.DeploymentLog]struct{}
}
```

1. **`sync.Mutex` and `sync.RWMutex` must never be copied by value.** Copying the mutex
   copies its internal lock state, producing two owners of one lock: instant deadlock or
   silent corruption. `go vet` catches this.
2. **Copying a struct copies slice and map headers**, which alias the same backing
   storage. A value receiver on `JSONStore` would silently share state.

The rule of thumb: **if the struct contains a `sync.` field, the receiver is a pointer.**
You followed it everywhere.

## The _Locked convention

```go
func (s *JSONStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()      // assumes caller holds the lock
}

func (s *JSONStore) saveLocked() error {
	// no locking in here
}

func (s *JSONStore) logSubscribersLocked(deploymentID string) []chan domain.DeploymentLog {
	// also assumes the caller holds the lock
}
```

Go has no truly private methods, so `saveLocked` is callable from anywhere in package
`store`. The `Locked` suffix is a **promise to readers**, and the code keeps it:

| Caller | Holds lock? | Correct? |
| --- | --- | --- |
| `save()` | yes, it locks | yes |
| `CreateProject` | yes, L71 | yes |
| `UpdateProject` | yes, L99 | yes |
| `AddLog` | yes, L206 | yes |
| `NewJSONStore` -> `load` -> `save` | yes, `save` locks | yes |

Why bother? Because **`sync.RWMutex` is not reentrant**. If `save` held the lock and then
called something that locked again, the program would deadlock instantly and forever, with
no error message. The split is how you acquire once and do many safely.

This is one of the most valuable habits in concurrent Go. The naming makes an invariant
visible that would otherwise exist only in your head.

## Methods, closures, and function values

Go function values are first-class. That includes methods and closures.

```go
// json_store.go
unsubscribe := func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers[deploymentID], channel)
	close(channel)
}
return channel, unsubscribe
```

Which produces this interface signature:

```go
SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func())
```

The caller receives a channel **and the means to release it**, so they cannot forget the
cleanup:

```go
// server.go
events, unsubscribe := s.store.SubscribeLogs(deploymentID)
defer unsubscribe()
```

`defer` runs on **every** exit path, including the `return` taken when the client
disconnects. Compare with the unclosed response bodies you would have to remember to close
by hand in raw `net/http`. Go pushes the pairing into the type signature, so it is
impossible to forget.

This is resource acquisition paired with release, enforced structurally rather than by
convention.

## Directional channels

```go
(<-chan domain.DeploymentLog, func())
```

The `<-` makes the returned channel **receive-only**. The consumer in `server.go` may read:

```go
case logEntry := <-events:
```

It is *type-system-impossible* for the HTTP handler to send into the store's internal
channel, or to `close()` it. Go expresses "you may look, not touch" in the type system
instead of in a code review checklist.

This is the Go rule: **accept interfaces, return concrete types.** Actually, the accurate
form here is narrower: *return the least capable type that suffices.* Narrow the
capability as a value flows outward.

## Interfaces

```go
// internal/store/store.go
type Store interface {
	CreateProject(input domain.CreateProjectInput) (domain.Project, error)
	ListProjects() ([]domain.Project, error)
	GetProject(id string) (domain.Project, error)
	UpdateProject(project domain.Project) error

	CreateDeployment(projectID string, triggeredBy string, rollbackTo string) (domain.Deployment, error)
	ListDeployments(projectID string) ([]domain.Deployment, error)
	GetDeployment(id string) (domain.Deployment, error)
	UpdateDeployment(deployment domain.Deployment) error
	FindPreviousSuccessfulDeployment(projectID string, currentDeploymentID string) (domain.Deployment, error)

	AddLog(deploymentID string, stream string, message string) (domain.DeploymentLog, error)
	ListLogs(deploymentID string) ([]domain.DeploymentLog, error)
	SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func())
}
```

An interface is a **set of method signatures**. No body, no fields, no inheritance. That
is the entire type definition.

## Implicit satisfaction

There is no `implements` keyword anywhere in this repo. The rule is:

> A type satisfies an interface if it has all the methods. Nothing needs to declare it.

```go
var repository store.Store
repository = jsonStore      // *JSONStore has all 12 methods, OK
repository = postgresStore  // *PostgresStore has all 12, OK
repository = exec           // *SimulationExecutor has neither, compile error
```

The compiler checks the full method set at assignment time. This is **duck typing,
statically checked**: the flexibility of a dynamic language with the safety of a static
one.

The consequence worth knowing: interfaces are satisfied by types from *other* packages,
and even by types you did not write. Any type anywhere with the right methods works. There
is no registration step.

## Interfaces are defined by the consumer

This is the most important design principle in Go, and your repo demonstrates it exactly.

```txt
internal/store/store.go       -> interface Store     (defined in store)
internal/executor/executor.go -> interface Executor  (defined in executor)

internal/worker/worker.go  consumes store.Store + executor.Executor
internal/app/server.go    consumes store.Store
```

Neither `store` nor `executor` knows that `worker` or `app` exist. The interfaces are as
small as the **caller** needs, and the concrete types are free to have far more methods.

Contrast with Java or C#, where the interface typically lives next to the implementation
and the implementor imports it. In Go the interface is usually defined **in the consumer's
package**, and the implementation never imports the interface at all.

The proverb: **accept interfaces, return structs.** The Go team applies it internally.
`http.Handler` is a one-method interface precisely so you never depend on `net/http`
internals.

## The compile-time assertion

```go
// internal/store/postgres.go:20
var _ Store = (*PostgresStore)(nil)
```

Read it character by character:

- `_` — the blank identifier, a value declared and never used.
- `Store` — a value of interface type `Store`.
- `= (*PostgresStore)(nil)` — assigned the **typed nil pointer**.

This declares a package-level variable whose only purpose is to be type-checked at
compile time. Add a method to `Store`, forget it on `PostgresStore`, and this line fails
to compile **even if that method is never called at runtime**.

That matters here because every CRUD method in `PostgresStore` is currently a stub
returning `postgresUnimplemented`. The real type-safety risk is front and centre, and this
line means the file will not silently drift out of compliance.

This is the interface assertion idiom. Zero cost, and it is how serious Go code keeps
refactors safe.

## Function types as interfaces

```go
// internal/executor/executor.go:11
type LogFunc func(stream string, message string)
```

A named function type. Now look at how the worker injects behaviour without a struct, a
mock library, or an interface:

```go
// internal/worker/worker.go
imageTag, err := w.executor.Deploy(w.ctx, project, deployment, func(stream string, message string) {
	w.log(deployment.ID, stream, message)
})
```

The worker passes a **closure** capturing `deployment.ID` and `w`. The executor calls it
during the deploy, and every call lands in the store as a log row, which fans out to SSE
subscribers, which render in the browser.

Dependency injection with zero ceremony. No mock struct, no generated code, no interface
with one method. If `LogFunc` were the only dependency, a one-method interface would
also work; Go lets you pick the smaller abstraction.

**This is why the simulation works at all.** The `executor` package has no idea that logs
are persisted, broadcast, or displayed.

**Exercise.** Add `type LogSink interface { Log(stream, msg string) }` to the executor
package and change `Deploy` to take a `LogSink`. Then replace the `LogFunc` with it. Both
work. Then pass `*Worker` directly instead of a closure and notice you have removed a
layer. This is a judgement call, and the point of the exercise is feeling the difference.

---

# Phase 4: Errors

## The error type

```go
type error interface {
	Error() string
}
```

That is the entire definition. A one-method interface, satisfied by `*errors.errorString`,
`*net.OpError`, `*fs.PathError`, and everything else in the standard library.

## The (Result, error) convention

```go
func (s *JSONStore) GetProject(id string) (domain.Project, error) {
	for _, project := range s.state.Projects {
		if project.ID == id {
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}
```

The last return value is `error`. **Nil means success.** Failure returns the **zero
value** of the result, not garbage, so a caller can never observe uninitialised data.

The convention adapts to the arity:

```go
GetProject(id string) (domain.Project, error)              // returns a value
UpdateProject(project domain.Project) error                 // returns nothing
SubscribeLogs(id string) (<-chan Log, func())               // returns a channel + cleanup
```

## Sentinel errors

```go
// internal/store/json_store.go:19
var ErrNotFound = errors.New("not found")
```

A package-level `error` **value**. Because it is a value, it is comparable, so callers can
identify it. This is how `PostgresStore` will return the same `ErrNotFound` as
`JSONStore`, and both will satisfy the same caller.

## Wrapping, and why `==` does not work

```go
// internal/store/postgres.go
if err := db.PingContext(ctx); err != nil {
	return nil, fmt.Errorf("ping postgres: %w", err)
}
```

`%w` creates a new error that **remembers its cause**. If errors could only be compared
with `==`, this function would break the caller's check, because the original
`ErrNotFound` is now buried inside a wrapper.

`errors.Is` walks the chain:

```go
errors.Is(err, store.ErrNotFound)     // domain
errors.Is(err, os.ErrNotExist)        // file loading
errors.Is(err, context.Canceled)      // shutdown
errors.Is(err, http.ErrServerClosed)  // graceful shutdown
```

All four are in your code. This is the single most important detail in Go error handling:
**errors wrap, and `errors.Is` unwraps them.**

## Errors do not unwind

| | Mechanism | Effect on the happy path |
| --- | --- | --- |
| Java, Python, C# | exception unwinds the stack | the return value is lost, code after the throw point is skipped |
| Go | error is a return value | nothing unwinds, every path is visible at the call site |

The payoff is that error handling is just control flow, so a helper can be called from
anywhere without stack bookkeeping:

```go
func (w *Worker) fail(deployment *domain.Deployment, err error) {
	if errors.Is(err, context.Canceled) {
		return    // we were shut down, not broken
	}
	now := time.Now().UTC()
	deployment.Status = domain.StatusFailed
	deployment.Error = err.Error()
	deployment.FinishedAt = &now
	if updateErr := w.store.UpdateDeployment(*deployment); updateErr != nil {
		log.Printf("mark deployment failed: %v", updateErr)
	}
	w.log(deployment.ID, "system", "Deployment failed: "+err.Error())
}
```

Early-return blocks work so cleanly **because** there is no `defer recover()` dance. Note
also that `fail` is idempotent enough to be called from any failure branch, and that the
distinction between "cancelled" and "failed" is load-bearing: a shutdown must not mark
in-flight deployments as broken.

## The error block

```go
projects, err := s.store.ListProjects()
if err != nil {
	writeError(w, http.StatusInternalServerError, err)
	return
}
writeJSON(w, http.StatusOK, projects)
```

This is the most recognisable pattern in Go, and you will write it ten thousand times. It
is a direct consequence of errors being return values: handling them is the path of least
resistance. No framework middleware, no annotations, no hidden exception handler.

## Deliberate discarding

Go style distinguishes three cases, and the distinction is **visible in the code**:

| Pattern | Meaning |
| --- | --- |
| `x, err := f()` then handle | I care about this error |
| `x, _ := f()` | checked, and I know it cannot fail here |
| `_ = f()` | knowingly ignored |

Real examples:

```go
_ = json.NewEncoder(w).Encode(value)   // response already committed; nothing to do
_, _ = s.store.AddLog(deployment.ID, "system", "Deployment queued")
_ = w.store.UpdateDeployment(active)   // best effort
_ = file.Close()                       // error already handled above
```

```go
// but this one is handled
if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
	return err
}
```

Same language, different stakes, explicitly expressed. `_ = f()` is a decision, not
oversight.

## log.Fatalf has exactly one legal home

```go
// main.go
log.Fatalf("create postgres store: %v", err)
```

`Fatalf` prints and calls `os.Exit(1)`. In `main`, that is correct: if you cannot open your
database, there is no program left to run.

Inside a request handler it would be a serious bug. One bad request would kill the server
for every user. Notice that the HTTP layer uses `writeError` everywhere and never
`Fatalf`.

**Exercise.** Add a method to the `Store` interface. Run `go build ./...`. The
`var _ Store = (*PostgresStore)(nil)` line fails even though nothing calls the new method.
That is the assertion doing its job.

---

# Phase 5: Concurrency

Goroutines, channels, `select`, and `context` are language features, not libraries. This
is where Go departs most sharply from other languages, and where your code is strongest.

## Goroutines

```go
// main.go
go func() {
	log.Printf("deploy platform listening on http://localhost%s", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}()
```

`go f()` runs on a new goroutine and returns immediately. `ListenAndServe` blocks forever;
prefixed with `go`, the main function continues to the shutdown logic.

A goroutine is a **lightweight concurrently-executing function**. Go can hold millions
because stacks start around 2 KB and grow on demand, and because the runtime multiplexes
them onto OS threads.

**The one rule:** `go f()` gives you no handle. No thread id, no join, no return value.
Once it starts you cannot ask "did it finish?" or "what did it return?" That is why the
next three tools exist.

## WaitGroup, the manual join

```go
type Worker struct {
	wg sync.WaitGroup
}

func (w *Worker) Start() {
	w.wg.Add(1)   // 1: one goroutine is coming
	go w.loop()
}

func (w *Worker) Stop() {
	w.cancel()    // 2: ask it to stop
	w.wg.Wait()   // 3: block until it actually has
}

func (w *Worker) loop() {
	defer w.wg.Done()
```

Three counters, and `defer` is the idiomatic and **safe** placement. If `loop` panics,
`Done()` still runs. That is not stylistic, it is correctness.

Go does not implicitly join goroutines when a function returns. `WaitGroup` is how you say
"do not let this object go away while I am still working."

## context.Context

```go
ctx, cancel := context.WithCancel(context.Background())
```

A context carries a **deadline, a cancellation signal, and request-scoped values** down a
call tree. First principles:

- Cancelling a parent cancels **all** children.
- `context.Background()` is the root. Never cancelled.
- `cancel` is a `func()` that cancels this subtree and everything under it.

The `Worker` owns a cancelable subtree:

```go
func New(repository store.Store, exec executor.Executor) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		store:    repository,
		executor: exec,
		ctx:      ctx,
		cancel:   cancel,
	}
}
```

And that context is then **passed down** to the executor, so the whole tree shares one
cancellation signal:

```go
imageTag, err := w.executor.Deploy(w.ctx, project, deployment, ...)
```

## Done() is a channel

`ctx.Done()` returns a `<-chan struct{}`. **Reading from it blocks until the context is
cancelled.** `struct{}` is the zero-size type, so the channel carries no data, only a
signal.

That is the entire cancellation idiom: block on `Done`, wake up, notice, return.

## select waits on multiple channels

```go
func (w *Worker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.processOnce()
		}
	}
}
```

`select` blocks until **any one** case is ready. The worker ticks twice a second while
healthy and wakes **immediately** on shutdown.

Contrast the alternatives: a `time.Sleep` would delay teardown by up to a second, and a
polled boolean flag would busy-wait. Here cancellation is free, because it is just another
case.

A cancellable sleep, in five lines:

```go
func pause(ctx context.Context) error {
	timer := time.NewTimer(550 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

No polling, no goroutine leak, no `Thread.sleep` that ignores interrupts.

## signal.NotifyContext

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
<-ctx.Done()
```

This intercepts SIGINT (Ctrl-C) and SIGTERM (`docker stop`, systemd) and returns a
**context that cancels when one arrives**.

This is a genuinely elegant piece of design: an event from outside your program entirely,
arriving as an ordinary value flowing through your ordinary cancellation mechanism. No
signal channel to manage, no `for` loop over `os.Signal`, no second code path.

The main goroutine blocks on `<-ctx.Done()`, which wakes on Ctrl-C. Then:

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := server.Shutdown(shutdownCtx); err != nil {
	log.Printf("server shutdown error: %v", err)
}
```

A **bounded** graceful shutdown. Note carefully that this is built from
`context.Background()`, **not** from the already-cancelled `ctx`. Deriving from a dead
parent gives an already-expired context, and `Shutdown` would return instantly, defeating
the whole point. This is a subtle detail, and you got it right.

There is no `os.Exit(0)`. **Returning from `main` is equivalent to `os.Exit(0)`, and it
runs all deferred functions first.** That is why graceful shutdown works without manual
ceremony.

## Channels

```go
channel := make(chan domain.DeploymentLog, 32)
```

Three parameters: element type, buffer size, and direction.

A channel is a typed, goroutine-safe queue. Unbuffered channels **synchronise** (the sender
blocks until a receiver is ready), which is how Go expresses a handshake without a mutex.
Buffered channels decouple producer and consumer.

The buffer size in your code is load-bearing:

```go
for _, subscriber := range subscribers {
	select {
	case subscriber <- logEntry:
	default:
	}
}
```

That `default` branch makes it a **non-blocking send**. If the subscriber's buffer is
full, drop the log instead of blocking.

Without it, one browser tab that stops reading would block the worker mid-deploy and stall
every deployment on the platform, permanently. With it, a slow consumer degrades instead
of cascading.

**Go's policy: never let one slow reader stall the pipeline.** You will see this `select`
with `default` in a lot of Go code.

### Closing

```go
unsubscribe := func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscribers[deploymentID], channel)
	close(channel)
}
```

`close` signals that no more values will be sent. Every receiver is unblocked and gets the
zero value. Closing a channel twice panics, so ownership of `close` matters enormously,
which is why the store owns it and hands out a receive-only channel.

## Maps as sets

```go
subscribers map[string]map[chan domain.DeploymentLog]struct{}
```

Read right to left:

- `map[K]struct{}` is a **set**. `struct{}` is zero-size, so membership costs no memory.
  This is THE Go idiom for a set. There is no `Set` type in the stdlib and you do not
  need one.
- `map[chan domain.DeploymentLog]struct{}` is a set of **channels**, keyed by identity.
  Channels are reference types and are comparable, so they work as map keys.
- The outer `map[string]...` is keyed by deployment ID.

So: *for deployment X, which channels are listening?* This is what lets SSE scale to many
concurrent browser tabs with no extra plumbing. Each tab subscribes, gets its own channel,
gets its own buffered queue.

Go maps read missing keys as zero values, but **writing to a nil map panics**, hence the
lazy initialisation:

```go
if _, ok := s.subscribers[deploymentID]; !ok {
	s.subscribers[deploymentID] = make(map[chan domain.DeploymentLog]struct{})
}
s.subscribers[deploymentID][channel] = struct{}{}
```

## RWMutex

```go
mu sync.RWMutex
```

- `Lock` / `Unlock` — exclusive write lock. `CreateProject`, `UpdateProject`, `AddLog`.
- `RLock` / `RUnlock` — shared read lock. Multiple goroutines may hold it **simultaneously**.
  `ListProjects`, `GetProject`, `ListLogs`.

The naming convention is universal: `RLock` for read, `Lock` for write. Same mutex, two
levels.

Why it matters here: your HTTP server handles every request in its own goroutine, and
your worker writes from another. Without `RWMutex`, a `GetProject` during a
`CreateProject` is a data race on a slice: undefined behaviour, possible crash, no
compiler warning. With it, reads scale in parallel and writes are safely serialised.

## Never do slow work under a lock

```go
s.mu.Lock()
s.state.Logs = append(s.state.Logs, logEntry)
err := s.saveLocked()
subscribers := append([]chan domain.DeploymentLog(nil), s.logSubscribersLocked(deploymentID)...)
s.mu.Unlock()          // unlock BEFORE broadcasting

for _, subscriber := range subscribers {
	select {
	case subscriber <- logEntry:
	default:
	}
}
```

Broadcasting while holding the lock is the top deadlock recipe in concurrent Go. Every
other goroutine touching the store stalls behind it, including whatever the broadcaster
might need. Unlock first, then send.

Note `append([]chan domain.DeploymentLog(nil), s.logSubscribersLocked(...)...)` — a
defensive copy made under the lock, so the send loop can iterate outside it safely.

## Known race in this code

That pattern has a real bug, and it is worth understanding precisely.

`unsubscribe` acquires the lock and calls `close(channel)`. If it lands **after**
`AddLog` released the lock but **before** the send loop runs, then:

```go
case subscriber <- logEntry:   // send on a closed channel -> panic -> process dies
```

The rule being violated: **if you must do work outside the lock, you may not use a
resource obtained under it.**

Two valid fixes:

1. Hold the lock through the send loop. The sends are non-blocking (`select` with
   `default`), so the hold time stays bounded and this is the pragmatic choice.
2. Change channel ownership so the store alone closes channels, and have the consumer
   signal completion by a different mechanism.

Reproduce it with a test and the race detector:

```go
func TestSubscribeLogsConcurrentUnsubscribe(t *testing.T) {
	repo, _ := NewJSONStore(filepath.Join(t.TempDir(), "state.json"))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = repo.AddLog("dep1", "system", "x") }()
		go func() { defer wg.Done(); _, unsub := repo.SubscribeLogs("dep1"); unsub() }()
	}
	wg.Wait()
}
```

```bash
go test -race ./internal/store/
```

The race detector is the single most valuable tool in Go. Learn it early.

## defer ordering

```go
defer postgresStore.Close()      // registered 1st
defer deploymentWorker.Stop()    // registered 2nd
defer stop()                     // registered 3rd
defer cancel()                   // registered 4th
```

**`defer` runs LIFO**, last in, first out. So the shutdown order is:

```txt
cancel()              -> release the shutdown deadline
stop()                -> stop intercepting signals
deploymentWorker.Stop()   -> drain the worker
postgresStore.Close()     -> close the store
```

That ordering is **correct and important**: the worker must stop using the store before
the store closes, or a deployment finishing mid-shutdown writes to a closed database.

The natural order of writing these lines produced the correct execution order. That is
precisely why `defer` is not just sugar for "run at the end" — it is a mechanism for
acquiring and releasing resources in the order that reads correctly.

## Goroutine leak checklist

When you add concurrency, check these:

| Leak | Symptom | Fix |
| --- | --- | --- |
| Blocked on channel send | goroutine count grows | buffered channel + `select` with `default` |
| Blocked on channel receive | goroutine count grows | `select` with `ctx.Done()` |
| Forgotten `Done()` | `Wait` hangs forever | `defer wg.Done()` |
| Lost subscriber | map grows forever | return cleanup from the constructor |
| Loop variable capture | all workers see the last value | pass as parameter, or Go 1.22+ fixes this |

Your `streamLogs` and `AddLog` already handle the first two and the fourth.

**Exercise.** Run the server, open three browser tabs on the same deployment, then stop
the worker. Watch goroutine count with `pprof` or `GODEBUG=gctrace=1`. The subscriber map
must return to zero, which it does because of `defer unsubscribe()`.

---

# Phase 6: The HTTP layer

A full REST API and an SSE streaming endpoint with zero dependencies. That is a design
goal, not a compromise.

## Handler is one method

```go
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
```

```go
type Handler interface {
	ServeHTTP(ResponseWriter, *Request)
}
```

The entire contract for handling HTTP. Your `Server` satisfies it, and the method body
delegates to the mux.

Because `*http.ServeMux` is also an `http.Handler`, layers compose:

```txt
Request -> Server -> Mux -> your handler
```

Each layer adds a concern (logging, auth, CORS) without knowing about the others. This is
decorator composition using nothing but one method.

## Method-aware routing

```go
s.mux.HandleFunc("GET /api/health", s.health)
s.mux.HandleFunc("GET /api/projects", s.listProjects)
s.mux.HandleFunc("POST /api/projects", s.createProject)
s.mux.HandleFunc("GET /api/projects/{id}", s.getProject)
s.mux.HandleFunc("GET /api/projects/{id}/deployments", s.listDeployments)
s.mux.HandleFunc("POST /api/projects/{id}/deployments", s.createDeployment)
s.mux.HandleFunc("GET /api/deployments/{id}", s.getDeployment)
s.mux.HandleFunc("GET /api/deployments/{id}/logs", s.listLogs)
s.mux.HandleFunc("GET /api/deployments/{id}/logs/stream", s.streamLogs)
s.mux.HandleFunc("POST /api/deployments/{id}/rollback", s.rollback)
```

This is the Go 1.22 improvement, and it is why `go.mod` must declare 1.24.

The **method is part of the pattern**. `POST /api/projects` is a *different route* from
`GET /api/projects`, and Go returns `405 Method Not Allowed` for the wrong method
automatically.

Before 1.22 you needed `mux.HandleFunc("/api/projects", h)` and then a manual
`if r.Method != "GET"` check inside the handler. That was a famous footgun, because people
forgot the check and the endpoint silently accepted any verb.

`{id}` matches **one** path segment. `{id...}` would match the rest of the path. Retrieve
it with `r.PathValue("id")`, which replaced the older `mux.Vars(r)["id"]` map lookup.

There is no third-party router here. No Gin, Echo, Fiber, or Chi. The stdlib mux is now
good enough, which is what makes "no framework on the backend" in the README a real
choice rather than a compromise.

## Reading the request, writing the response

```go
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
```

- **`w http.ResponseWriter`** — the write half. Builds the response: `Header()`,
  `WriteHeader()`, `Write()`.
- **`r *http.Request`** — the read half. Method, URL, headers, `r.Body`.

The unusual part: **Go hands you both halves and never a response object.** You control
exactly when the status code is written, and the rule is **first write wins**.

```go
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")   // 1. headers first
	w.WriteHeader(status)                                  // 2. THEN status
	_ = json.NewEncoder(w).Encode(value)                   // 3. THEN body
}
```

Order is load-bearing. If you call `Encode` first, the status is implicitly committed to
`200 OK`, and `w.WriteHeader(http.StatusCreated)` becomes a no-op with a log warning.

This is the opposite of most frameworks, where you return a value and the framework
handles status and body. Go is more verbose and never magic.

## Mapping domain errors to status codes

```go
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}
```

The store returns `ErrNotFound` and knows nothing about HTTP. The handler translates. That
inward dependency direction is why JSON can be swapped for Postgres without touching a
single handler.

The status choices follow the layers:

| Situation | Status | Where |
| --- | --- | --- |
| Health check | 200 | `health` |
| List succeeded | 200 | `listProjects` |
| Resource created | 201 | `createProject`, `createDeployment` |
| Store lookup failed | 404 | `writeStoreError` |
| Malformed JSON body | 400 | `createProject` |
| No prior success to roll back to | 400 | `rollback` |
| Unexpected store failure | 500 | most handlers |

## Server-Sent Events

The full lifecycle, annotated:

```go
// 1. headers
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")

// 2. flushing must be checked
flusher, ok := w.(http.Flusher)
if !ok {
	writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
	return
}

// 3. replay history
logs, err := s.store.ListLogs(deploymentID)
if err != nil { ...; return }
for _, logEntry := range logs {
	writeEvent(w, logEntry)
}
flusher.Flush()

// 4. subscribe
events, unsubscribe := s.store.SubscribeLogs(deploymentID)
defer unsubscribe()

// 5. stream until disconnect
for {
	select {
	case <-r.Context().Done():
		return
	case logEntry := <-events:
		writeEvent(w, logEntry)
		flusher.Flush()
	}
}
```

**Step 1.** The SSE wire format is text: `event: <name>\ndata: <payload>\n\n`. The trailing
blank line is the message terminator. That is the whole protocol, and the browser consumes
it with `EventSource` in three lines.

**Step 2.** A runtime interface type assertion with the comma-ok idiom: "is this
`ResponseWriter` also a `http.Flusher`?" `ok == false` instead of a panic. You must handle
it, because Go wraps the real writer in a `*response` struct that may not support
flushing. The alternative is that logs arrive in one lump at the end.

**Step 3 matters.** A user opening the page five seconds into a deploy must see those five
seconds, not a blank screen. Replay-then-subscribe is the standard pattern.

There is a real gap here: a log written between `ListLogs` and `SubscribeLogs` is lost.
Production Go fixes it with a monotonically increasing sequence number in the store, or by
subscribing first and buffering until the replay finishes. Worth doing when log lines
become billing-relevant.

**Step 4** is the payoff of `func()` in the interface signature. If the browser closes the
tab, the goroutine returns, the `defer` fires, and the channel is removed from the map
and closed. No goroutine leak, no map leak, guaranteed by the language rather than by
discipline.

**Step 5** is what makes it scale. `r.Context()` is a context **cancelled automatically
when the client disconnects**: the stdlib watches the connection. You never poll, never
time out. You `select` on it alongside the event channel, exactly as the worker selects on
`ctx.Done()`.

**One cancellation concept, applied uniformly**, whether the cancellation came from
Ctrl-C, a deadline, a client hangup, or test teardown. That is why Go feels smaller than
its feature list suggests: `context` is one answer to one question, applied to everything.

## Compile-time embedding

```go
import "embed"

//go:embed static/*
var staticFiles embed.FS
```

`//go:embed` is a **compiler directive**. The toolchain reads those bytes at build time
and bakes them into the binary.

- Your frontend ships **inside the Go binary**. One file to deploy. No nginx, no `COPY`
  step, no version skew between assets and API.
- `import "embed"` is mandatory even though the name is never referenced. The compiler
  checks for it, which is why Go cannot get this wrong. Contrast with the blank import in
  Phase 1, which the compiler does not require.
- `static/*` is a glob. It matched your `index.html`, `app.js`, and `styles.css`.

```go
staticRoot, err := fs.Sub(staticFiles, "static")
if err != nil {
	panic(err)
}
s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticRoot))))
```

Three cooperating pieces, each doing one job:

| Call | Job |
| --- | --- |
| `fs.Sub` | make the `static/` subtree the new root |
| `http.FS` | adapt `embed.FS` to `http.FileSystem` |
| `http.FileServer` | serve a directory as static assets |
| `http.StripPrefix` | remove `/static/` so the handler sees `app.js` |

The `panic` is justified here, and it is one of only two in your codebase. `fs.Sub` on a
compile-time glob cannot fail at runtime; if it does, the binary is broken. Panicking
during initialisation is the correct response. The other is in `NewServer`, for the same
reason.

```go
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, staticFiles, "static/index.html")
}
```

The explicit path check is required because `"GET /"` is a **catch-all**: it matches any
unmatched GET. This guard narrows it back to the root.

## Graceful shutdown

```go
server := &http.Server{
	Addr:              addr,
	Handler:           app.NewServer(repository),
	ReadHeaderTimeout: 5 * time.Second,
}
```

`ReadHeaderTimeout` is **not optional**. It is the fix for the Slowloris denial-of-service
attack, where a client opens a connection and dribbles headers to tie up a goroutine
forever. Every linter flags its absence. Its presence tells me you already know the rule.

Combined with `server.Shutdown(shutdownCtx)`: stop accepting new connections, let in-flight
requests finish within 5 seconds, then exit. No dropped deploy streams on restart.

**Exercise.** Add a handler that sleeps for 10 seconds. Send it a request, then hit Ctrl-C
and observe that the response still completes, because the 5-second shutdown context is
generous enough. Then reduce the timeout to 1 second and confirm the request is cut off.
This is the behaviour you get for free from `net/http` and the reason not to write a
custom server.

---

# Phase 7: Idiom and testing

## Defensive slice copy

```go
// internal/store/json_store.go
projects := append([]domain.Project(nil), s.state.Projects...)
```

This one line protects your entire store, and understanding it requires knowing what a
slice really is.

A slice is a **three-word descriptor**: `{pointer to first element, length, capacity}`.
Copying the descriptor does **not** copy the elements. All copies share one backing array.

Without this line, `ListProjects` returns a slice whose backing array **is**
`s.state.Projects`. If the caller does `projects[0].Name = "hacked"`, they have mutated
your live store state — and they have done it **after `RUnlock`**, so the mutation is
completely unsynchronised. That is a data race that compiles cleanly.

`append([]T(nil), src...)` is the idiom: a `nil` slice has no backing array, so `append`
allocates a fresh one and copies everything in.

This is Java's defensive copy, done in one expression.

## sort.Slice

```go
sort.Slice(projects, func(i, j int) bool {
	return projects[i].CreatedAt.After(projects[j].CreatedAt)
})
```

`sort` and `sync/atomic` are the only camelCase packages in the standard library, and it
is deliberate: **camelCase means behaviour, lowercase means data.** `sort` sorts.

`sync` is lowercase because it synchronises. It does not sort, so it is not `Sync`. Learn
the convention and the stdlib becomes navigable.

`sort.Slice` takes any slice plus a **less function**. That `func(i, j int) bool` is a
closure capturing `projects`.

Go 1.21+ has generics, and the modern form is better:

```go
slices.SortFunc(projects, func(a, b domain.Project) int {
	return b.CreatedAt.Compare(a.CreatedAt)   // newest first
})
```

The element type is in the signature, so it is type-safe, and it is roughly twice as fast
because it skips reflection. Worth using when you refactor.

## Reverse iteration, and why the loop reads backwards

```go
for i := len(deployments) - 1; i >= 0; i-- {
	deployment := deployments[i]
	if deployment.Status == domain.StatusQueued {
		w.runDeployment(project, deployment)
		return
	}
}
```

`ListDeployments` sorts **newest first**:

```go
sort.Slice(deployments, func(i, j int) bool {
	return deployments[i].StartedAt.After(deployments[j].StartedAt)
})
```

So `len(deployments)-1` down to `0` is **oldest first**: FIFO. The worker takes the
longest-waiting job.

Iterating upward instead would pick the newest job and starve older ones indefinitely, and
would start a new deployment while a previous one is still in flight. **A correctness
decision encoded in a loop direction**, which is exactly the kind of thing that deserves a
comment. It does not have one yet.

Also note `for i := ...` rather than `range` with an index. Idiomatic Go: use `range` when
you do not need the index, a classic loop when you do.

## strings.Builder

```go
var builder strings.Builder
lastDash := false
for _, r := range value {
	if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
		builder.WriteRune(r)
		lastDash = false
		continue
	}
	if !lastDash {
		builder.WriteRune('-')
		lastDash = true
	}
}
return strings.Trim(builder.String(), "-")
```

`strings.Builder` exists because `s += "x"` in a loop **reallocates and copies the entire
string every iteration**: O(n^2). `Builder` appends in amortized O(1). The stdlib type is a
performance primitive, not a style preference.

Three other details in fourteen lines:

- `for _, r := range value` iterates **runes**, not bytes. For `"café"`, `r` is an `int32`
  code point, and `r >= 'a' && r <= 'z'` is a rune comparison. Iterating bytes would
  corrupt multi-byte UTF-8. (It also silently drops non-ASCII here, which is fine for a
  slug but worth knowing if project names can be non-English.)
- `lastDash` collapses runs of separators into one dash, so `"my  app"` becomes `"my-app"`.
- `strings.Trim(..., "-")` cleans up leading and trailing dashes that `lastDash` cannot
  prevent.

`WriteRune` returns `(int, error)`. Go's convention is that `bytes.Buffer` and
`strings.Builder` **never** fail; the error exists for interface compatibility. Ignoring
it here is correct, not sloppy.

## fmt verbs

`Errorf` and `Sprintf` are the same function with different return types. Learn the verbs:

| Verb | Meaning |
| --- | --- |
| `%s` | string |
| `%d` | integer |
| `%v` | any value, default format |
| `%q` | quoted string, ideal for test failure messages |
| `%T` | the **type** of the value |
| `%w` | wrapped error, preserves the cause for `errors.Is` |

`%T` is a genuinely useful debugging tool when a log line is not what you expected:

```bash
go run ./cmd/server 2>&1 | grep -i deployment
log.Printf("create deployment: type=%T value=%+v", deployment, deployment)
```

## Random IDs

```go
func shortID() string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes[:])
}

func newID(prefix string) string {
	return prefix + "_" + shortID()
}
```

- `[6]byte` is an **array**, a fixed-size value, not a slice. `bytes[:]` slices it: a
  view of the same memory, but the right type for `rand.Read`.
- `crypto/rand`, **not** `math/rand`. `math/rand` is deterministic and predictable from a
  seed; `crypto/rand` is a CSPRNG. For IDs that gate deployments, `crypto/rand` is correct
  and costs nothing. `gosec` flags `math/rand` here.
- 6 bytes hex-encoded gives 12 characters. `newID("dep")` produces `dep_a1b2c3d4e5f6`: a
  scannable prefix plus randomness, which is why logs and `.data/state.json` are
  greppable. The prefix is a design choice, not decoration; it makes `grep dep_` useful.
- The `UnixNano` fallback on `rand` failure is pragmatic: a slightly weaker ID is far
  better than a total outage.

## Atomic file write

```go
func (s *JSONStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmpPath := s.path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.state); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}
```

Not Go-specific, but worth internalising. Write to a temp file, `Close()` it (flushing OS
buffers to disk), then `os.Rename` over the original.

**`os.Rename` is atomic on POSIX** when both paths share a filesystem. A reader sees
either the complete old file or the complete new one, never a half-written one.

Contrast with `os.WriteFile`, which truncates first: a crash mid-write leaves a corrupt
`state.json` and a platform that cannot start. This pattern is why SQLite, Git, and every
careful config writer work the same way.

`SetIndent("", "  ")` is a nice touch: the state file is meant to be read by a human during
debugging, and it costs only disk space.

## Self-bootstrapping

```go
func (s *JSONStore) load() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = state{}
		return s.save()
	}
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(&s.state)
}
```

`MkdirAll` in both `load` and `saveLocked` makes the store self-bootstrapping:
`NewJSONStore(".data/state.json")` works on a fresh machine with no setup. The
missing-file branch initialises empty state and writes it.

**The constructor produces a valid, ready-to-use store on day one.** That is the zero-value
philosophy applied to a resource: no `Init()` step, no "remember to call Open before use".

## Testing

```go
func TestJSONStoreProjectDeploymentAndLogs(t *testing.T) {
	repository, err := NewJSONStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewJSONStore() error = %v", err)
	}
	...
	if project.Branch != "main" {
		t.Fatalf("expected default branch main, got %q", project.Branch)
	}
}
```

The standard library `testing` package is enough. No framework required.

- **The file is `package store`, not `package store_test`.** That is an **internal** test,
  so it can reach unexported identifiers without production code exporting anything for
  test purposes. `package store_test` would be the **external** variant, testing only the
  public API. Go uses both; internal is the default for same-package tests.
- `t.TempDir()` returns a fresh path per call and registers cleanup with the framework.
  The test can never interfere with another run and never touches the developer's real
  `.data/state.json`. This is better ergonomics than most languages' tooling.
- `t.Fatalf` prints and calls `runtime.Goexit()`, stopping the current test. Correct here,
  because if the constructor fails every later line is meaningless and may panic. Use
  `t.Errorf` when you want to check several conditions in one run.
- Message format `"Func() error = %v"` is the community convention, with `%q` so a string
  mismatch is unambiguous.

What the test pins down:

| Check | Rule it protects |
| --- | --- |
| default `Branch == "main"` | store applies defaults, not the caller |
| create, update, log, list | the full persistence cycle |
| `len(logs) == 1` | `AddLog` appends exactly once |
| `FindPreviousSuccessfulDeployment` | the rollback correctness rule |
| called with `""` as current ID | an empty ID must not exclude the deployment |

**The gap:** `SubscribeLogs` is untested, and given the race in Phase 5, `go test -race`
on a concurrent test would likely catch it.

### Table-driven tests

The idiomatic Go shape for anything with more than one case, and a good fit for your
status machine:

```go
func TestStatusTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    domain.DeploymentStatus
		to      domain.DeploymentStatus
		allowed bool
	}{
		{"queued to cloning", domain.StatusQueued, domain.StatusCloning, true},
		{"queued to success", domain.StatusQueued, domain.StatusSuccess, false},
		{"building to failed", domain.StatusBuilding, domain.StatusFailed, true},
		{"success to building", domain.StatusSuccess, domain.StatusBuilding, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canTransition(tt.from, tt.to); got != tt.allowed {
				t.Errorf("canTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.allowed)
			}
		})
	}
}
```

`t.Run` creates a subtest, so a failure reports as one named case rather than aborting the
whole run. The table is a slice of anonymous structs, which is the standard Go pattern.

**Exercise.** Add a `canTransition` function to `domain` with a map of allowed transitions,
use it in `worker.setStatus`, and write the table test above. This is Phase 8's real work:
your status machine is currently enforced only by the order of statements in `runDeployment`.

---

# Phase 8: The architecture

The patterns in this repo form a coherent architecture, and each is a named Go idiom.

## Ports and adapters

```txt
                            +----------------+
   HTTP  ------------------>|                |<---- worker ----+
                            |  store.Store   |                 |
                            |    (port)      |           +-----+--------+
                            +--------+-------+           | executor     |
                                     |                   | .Executor    |
                            +--------+--------+          |   (port)     |
                            v                 v          +------+-------+
                     +------------+   +---------------+         v
                     | JSONStore  |   | PostgresStore |  +---------------+
                     | (adapter)  |   |   (adapter)   |  |SimulationExec |
                     +------------+   +---------------+  |   (adapter)   |
                                                          +---------------+
```

Arrows point **inward**, toward the interfaces. The interfaces are the stable centre; the
implementations are replaceable edges. Dependencies never flow outward.

This is ports and adapters, also called hexagonal architecture, discovered rather than
designed:

```go
// main.go
var repository store.Store
if databaseURL != "" {
	postgresStore, err := store.NewPostgresStore(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("create postgres store: %v", err)
	}
	defer postgresStore.Close()
	repository = postgresStore
} else {
	jsonStore, err := store.NewJSONStore(dataPath)
	if err != nil {
		log.Fatalf("create store: %v", err)
	}
	repository = jsonStore
}
```

One interface variable, two implementations chosen by configuration. `worker.New` and
`app.NewServer` receive a `store.Store` and cannot tell which they got.

When `PostgresStore` is implemented, the entire platform switches backends on one
environment variable, with **zero changes to `worker.go` or `server.go`**. That is the
payoff of dependency inversion, and the README's claim that the structure "stays unchanged"
is exactly right.

## Interface segregation

`Store` has 12 methods. `Executor` has 2. Not one god interface named `Platform`.

`Executor` being two methods is why `SimulationExecutor` is 73 lines, and why adding Docker
means implementing two functions rather than twenty.

The rule: **an interface should be as small as the consumer needs.**

## The simulation is faithful, not a toy

```go
type SimulationExecutor struct{}

func NewSimulationExecutor() *SimulationExecutor {
	return &SimulationExecutor{}
}
```

An **empty struct**: zero bytes, no state, no I/O, no Docker, no network, no configuration.
`NewSimulationExecutor` cannot fail and has no parameters.

That is what made the architecture testable and CI-runnable on day one. Swap in a
`DockerExecutor` later and nothing else changes, because the worker only ever saw the
two-method interface.

The detail that shows real engineering is `pause(ctx)`:

```go
func pause(ctx context.Context) error {
	timer := time.NewTimer(550 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

The simulation is **cancellable**, exactly like a real Docker build would be. So the V1
simulation already exercises the same cancellation path (`errors.Is(err,
context.Canceled)` in `worker.fail`) that the real executor will. **Your simulation is
faithful, not a mock.** That is a much bigger deal than it looks, and it is the difference
between a throwaway stub and a test double.

## The API and worker split

The invariant, from the README:

> The API never runs infrastructure work. It validates input, creates a `queued`
> deployment row, and returns. The worker owns all state transitions. The store records
> every transition.

The API's entire responsibility:

```go
func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	deployment, err := s.store.CreateDeployment(projectID, "manual", "")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_, _ = s.store.AddLog(deployment.ID, "system", "Deployment queued")
	writeJSON(w, http.StatusCreated, deployment)
}
```

Three operations and a `201`. The request returns in **microseconds** regardless of
whether the deploy takes 30 seconds or 30 minutes. The client polls
`/api/deployments/{id}` and subscribes to `/logs/stream` instead.

The naive alternative, where the handler shells out to Docker, would block a goroutine
per request, exhaust connection limits, and time out the browser. So would threading it:
`go deploy()` in the handler leaks a goroutine per request with no way to observe or bound
it.

**This is a job queue implemented with a database and a one-second ticker.** That is
legitimate for a single developer on a single VPS, and it has a real advantage the README
does not claim: because state lives in the store, a crash and restart **resumes cleanly**.
The worker re-lists queued deployments and picks up where it left off. No broker, no lost
jobs, no delivery semantics to reason about.

## Composition root

```go
// main.go
exec := executor.NewSimulationExecutor()
deploymentWorker := worker.New(repository, exec)
deploymentWorker.Start()
defer deploymentWorker.Stop()

server := &http.Server{
	Addr:    addr,
	Handler: app.NewServer(repository),
}
```

`main` is the **composition root**: the one place that knows about every concrete type and
wires them together. It is the only function that imports all four internal packages.

This is the practical payoff of dependency injection. Testability is not that you *can*
substitute a dependency, it is that the substitution point is a single readable function
at the top of your program. Everything else takes interfaces and cannot be wired
differently even if it wanted to.

## One constraint to be aware of

`processOnce` handles at most one job and returns, so throughput is **one deploy per
second** and deploys are strictly serialised.

For this scope that is fine, arguably desirable: three concurrent Docker builds would fight
over CPU, memory, and disk on a single VPS. But it is a real constraint, not an oversight.

Parallelising later means two changes: a `WaitGroup` around `runDeployment`, and a
`ClaimDeployment(id)` method on the `Store` interface so only one worker can claim a job.
That second change is a nice demonstration of an interface evolving with requirements, and
it is why the interface lives in the consuming package's world.

## One duplication to resolve

`SimulationExecutor.Deploy` simulates "Cloning...", "Checking repository metadata", and
"Dockerfile-based build strategy" internally, while the worker *also* sets `StatusCloning`
and `StatusBuilding` around the call. The phases are described twice, in two places, with
no shared definition.

When the real `DockerExecutor` lands, decide which owns the phase boundaries. The right
answer is the status machine: `runDeployment` sets the status, and the executor only
reports progress. That keeps the state machine testable without Docker and keeps the
executor free of store dependencies.

The clean version is for `Executor.Deploy` to take a callback for transitions, or for the
worker to own a `[]struct{status, logText}` plan and pass it down. Either way, one
definition of "what a deploy does".

**Exercise.** Invert that duplication. Give `domain` a `DeployPlan` type listing the phases,
have the worker drive statuses from it, and have the executor emit output for the phase it
is given. Then a table test can assert the whole plan without running anything.

---

# Reference index

Every Go concept in this codebase and where to find it.

| Concept | Where |
| --- | --- |
| Module declaration | `go.mod` |
| Language version gating | `go.mod`, `server.go` method routing |
| `internal/` visibility | whole layout |
| Composition root | `cmd/server/main.go` |
| Side-effect import | `store/postgres.go` `_ "github.com/lib/pq"` |
| Struct | `domain/domain.go` |
| Zero value | `store/json_store.go` `CreateDeployment` |
| Pointer as optional | `domain/domain.go` `FinishedAt *time.Time` |
| Address-of operator | `worker/worker.go` `&now` |
| Defined type as closed set | `domain/domain.go` `DeploymentStatus` |
| Typed constants | `domain/domain.go` const block |
| Struct tags | `domain/domain.go` all fields |
| `omitempty` | `domain/domain.go` `FinishedAt` |
| `any` | `app/server.go` `writeJSON` |
| Decode by pointer | `app/server.go` `Decode(&input)` |
| Allow-list input type | `domain/domain.go` `CreateProjectInput` |
| Pointer receiver | `store/json_store.go` all methods |
| Mutex-copy rule | `store/json_store.go` `JSONStore.mu` |
| `_Locked` convention | `store/json_store.go` `saveLocked`, `logSubscribersLocked` |
| Closure as value | `store/json_store.go` `unsubscribe` |
| Cleanup returned from constructor | `store/store.go` `SubscribeLogs` |
| Receive-only channel | `store/store.go` `SubscribeLogs` return |
| Interface definition | `store/store.go`, `executor/executor.go` |
| Implicit satisfaction | `main.go` `repository = jsonStore` |
| Consumer-defined interface | `worker/worker.go`, `app/server.go` fields |
| Compile-time assertion | `store/postgres.go` `var _ Store = ...` |
| Function type as dependency | `executor/executor.go` `LogFunc` |
| Closure injection | `worker/worker.go` `Deploy(..., func(...))` |
| `(Result, error)` convention | `store/store.go` all methods |
| Zero result on failure | `store/json_store.go` `GetProject` |
| Sentinel error | `store/json_store.go` `ErrNotFound` |
| Error wrapping with `%w` | `store/postgres.go` `fmt.Errorf("ping postgres: %w")` |
| `errors.Is` chain walking | `app/server.go` `writeStoreError` |
| `os.ErrNotExist` check | `store/json_store.go` `load` |
| `context.Canceled` check | `worker/worker.go` `fail` |
| `http.ErrServerClosed` check | `main.go` `ListenAndServe` guard |
| Deliberate `_ =` discard | `app/server.go` `writeJSON` |
| Handled error, same construct | `store/json_store.go` `MkdirAll` |
| `log.Fatalf` in `main` only | `main.go` |
| Goroutine | `main.go`, `worker/worker.go` `Start` |
| `sync.WaitGroup` | `worker/worker.go` `Start`/`Stop` |
| `defer wg.Done()` | `worker/worker.go` `loop` |
| `context.WithCancel` | `worker/worker.go` `New` |
| `ctx.Done()` | `worker/worker.go` `loop` |
| `select` on two channels | `worker/worker.go` `loop`, `executor/executor.go` `pause` |
| Cancellable sleep | `executor/executor.go` `pause` |
| `signal.NotifyContext` | `main.go` |
| `context.WithTimeout` | `main.go` shutdown |
| Buffered channel | `store/json_store.go` `make(chan ..., 32)` |
| Non-blocking send | `store/json_store.go` `AddLog` `default:` |
| `close(channel)` | `store/json_store.go` `unsubscribe` |
| Map as set | `store/json_store.go` `subscribers` |
| Lazy map init | `store/json_store.go` `SubscribeLogs` |
| `sync.RWMutex` read path | `store/json_store.go` `RLock` |
| Unlock before broadcasting | `store/json_store.go` `AddLog` |
| Known send-on-closed race | `store/json_store.go` `AddLog` |
| `defer` LIFO ordering | `main.go` shutdown |
| `http.Handler` interface | `app/server.go` `ServeHTTP` |
| Decorator composition | `app/server.go` `Server` -> `mux` |
| Method-aware routing | `app/server.go` `routes` |
| Path wildcards | `app/server.go` `{id}` |
| `r.PathValue` | `app/server.go` `getProject` |
| Write-before-status rule | `app/server.go` `writeJSON` |
| Error to status mapping | `app/server.go` `writeStoreError` |
| Interface type assertion | `app/server.go` `w.(http.Flusher)` |
| SSE protocol | `app/server.go` `writeEvent` |
| Replay then subscribe | `app/server.go` `streamLogs` |
| `r.Context()` for disconnects | `app/server.go` `streamLogs` |
| `//go:embed` | `app/server.go` `staticFiles` |
| `fs.Sub` + `http.FS` + `FileServer` | `app/server.go` `routes` |
| `http.StripPrefix` | `app/server.go` `routes` |
| `ReadHeaderTimeout` | `main.go` |
| Graceful `Shutdown` | `main.go` |
| Defensive slice copy | `store/json_store.go` `ListProjects` |
| `sort.Slice` + closure | `store/json_store.go` `ListProjects` |
| `strings.Builder` | `store/json_store.go` `slug` |
| Rune iteration | `store/json_store.go` `slug` |
| `crypto/rand` over `math/rand` | `store/json_store.go` `shortID` |
| Atomic file write | `store/json_store.go` `saveLocked` |
| Self-bootstrapping constructor | `store/json_store.go` `load` |
| `t.TempDir()` | `store/json_store_test.go` |
| Internal test package | `store/json_store_test.go` |
| `t.Fatalf` early stop | `store/json_store_test.go` |
| FIFO job ordering | `worker/worker.go` `processOnce` |

---

# Not used yet

In rough order of value for what comes next in this project.

| Feature | What it buys | Since |
| --- | --- | --- |
| Generics + `slices`/`maps` | `slices.SortFunc` is type-safe and ~2x faster than `sort.Slice`; `slices.Contains` replaces manual loops | 1.21 |
| Struct embedding | `type Server struct { *http.ServeMux }` promotes methods; how Go does inheritance | always |
| `errgroup` | concurrent work with error collection and a `Wait` that returns the first error, better than a bare `WaitGroup` | x/sync |
| Table-driven tests | the idiomatic shape for the status machine | always |
| `min`/`max` builtins | cleaner clamps | 1.21 |
| `for i := range n` | counts without a variable | 1.22 |
| `sync/atomic` | lock-free counters for the V5 `/metrics` work | always |
| `context.WithValue` | request-scoped values, mostly for tracing | always |
| `log/slog` | the structured logging your README already promises | 1.21 |

## Suggested order of work

```bash
go build ./... && go vet ./... && go test -race ./...
```

Then, in order:

1. **Write table-driven tests for the status machine.** Edge cases to expect: a deployment
   stuck in `cloning` after a crash, since nothing resets it to `failed`; a `rollback`
   deployment whose target was itself rolled back; two rollbacks racing for the same
   active deployment.
2. **Fix the `AddLog` race.** Write the concurrent test from Phase 5, confirm the race
   with `-race`, then hold the lock through the non-blocking send.
3. **Implement one `PostgresStore` method**, starting with `ListProjects`, to see interface
   satisfaction work end to end. Use the `migrations/0001_init.sql` schema, which already
   mirrors your domain types exactly.
4. **Resolve the phase duplication** from Phase 8, so the real `DockerExecutor` has one
   clear owner of the status machine.
5. **Add a `ClaimDeployment` method** when you want concurrent workers, and watch the
   interface grow to match.

## Where to read next

Official documentation, all available offline with `go doc`:

```bash
go doc .            # this module
go doc net/http     # start here for Phase 6
go doc context      # the most important stdlib package in Go
go doc encoding/json
go doc sync
go doc errors
```

Then, in order of value:

- [The Go Programming Language Specification](https://go.dev/ref/spec) — short, and the
  authority when the docs and the blog disagree
- [A Tour of Go](https://go.dev/tour/) — interactive, covers the syntax this document
  assumes you know
- [Effective Go](https://go.dev/doc/effective_go) — short, and the source of most of the
  conventions used here
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) — the
  community's actual style rules, worth reading once
- [Share memory by communicating memory](https://blog.golang.org/cgo) — why channels
  instead of shared memory, which is the philosophical basis for Phase 5
- [Go Concurrency Patterns: Pipelines and cancellation](https://blog.golang.org/pipelines)
  and [Context](https://blog.golang.org/context) — the two articles that explain `select`
  and `context` best
