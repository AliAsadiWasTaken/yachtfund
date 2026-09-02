# yachtfund — Design

**Date:** 2026-09-01
**Status:** approved through Section 4; roadmap pending Ali's review
**Vocabulary:** every concept word here is defined in plain language with an example in
[`GLOSSARY.md`](../../../GLOSSARY.md). Read that alongside this document, not after it.

---

## 1. Purpose

A digital wallet / neobank core built on a correct double-entry ledger, in Go and PostgreSQL.

This is a **skill-building project, not a product**. Three goals, in priority order:

1. **Databases** — two specific areas Ali named: (a) DB transactions, isolation levels, MVCC and
   locking; (b) schema design where constraints carry the invariants.
2. **System design** — hexagonal architecture and DDD across real bounded contexts, with message
   brokers, sagas and eventual consistency.
3. **Coding** — Go, hand-written SQL, no ORM.

Fintech was chosen deliberately: correctness is not optional there. A bug is a wrong balance, not
a wrong pixel. That pressure is the point of the exercise.

### Success criteria

The project has succeeded when Ali can, without notes:

- explain what write skew is, produce it on demand, and name three different mechanisms that stop it
- say which invariants live in the schema and why each one cannot live in application code
- explain why the dual-write problem has no correct ordering, and what the outbox does about it
- justify when a saga is required and when it is strictly worse than one DB transaction
- point at a document of measured results — the anomaly matrix — rather than recite vocabulary

## 2. Scope

**In scope:** the ledger core; wallets; holds and their expiry; internal transfers; deposits and
withdrawals against a deliberately unreliable mock payment provider; multi-currency with FX;
velocity/risk rules as an event consumer; continuous reconciliation; Kafka and RabbitMQ; tracing.

**Out of scope:** a frontend beyond whatever is needed to demonstrate a flow; real KYC or real
provider integrations; authentication beyond a token stub; regulatory reporting; a mobile app.
None of these teach anything on the goal list.

## 3. Working model

**Ali implements everything. Claude is the mentor** — designs, explains, reviews, questions,
maintains documentation, and does not write implementation code unless explicitly asked for a
specific snippet. The rationale and the full division of labour are in `CLAUDE.md`.

**Sequencing is by capability milestone, never by date.** Ali may spend a week or an evening on a
milestone. Each milestone must be independently finishable and worth describing in an interview on
its own, so stopping at any point leaves a complete thing rather than a torso.

**The test suite comes last** (M9). One decided carve-out: the concurrency anomaly experiments
(§8.3) belong to M2, because they are how Ali *sees* Postgres behave rather than tests that protect
code.

## 4. Architecture

### 4.1 Bounded contexts

One hard rule holds the whole design together:

> **Only the Ledger writes postings.** Every other context asks it to, or listens to what it did.

If any other context could write a posting, the sums-to-zero invariant would have six places to be
broken instead of one.

| Context | Owns the invariant | Why it is not just a package |
|---|---|---|
| **Ledger** (core domain) | Every journal transaction sums to zero per currency; postings are immutable | System of record. Sole writer of postings. |
| **Wallet** | Customer wallets, limits, KYC state; posted vs available balance | Different lifecycle and consistency needs than the journal |
| **Authorization** | A hold is reserved-but-unposted funds; it expires; it captures at most once | Time-based state machines exist nowhere else in the system |
| **Payments** | A money movement reaches exactly one terminal state | Long-running processes over an unreliable provider; owns compensation |
| **Risk/Compliance** | Velocity and threshold rules; may freeze a wallet | Pure event consumer, deliberately eventually consistent |
| **Reconciliation** | Derived truth equals recorded truth | An independent verifier must be able to *disagree* with the ledger |

### 4.2 Hexagonal layout, per context

```
internal/<context>/
  domain/     aggregates, value objects, domain events, domain errors — stdlib imports only
  app/        use cases
    port/     interfaces: Repository, UnitOfWork, EventPublisher, Clock, IDGenerator, PSPGateway
  adapter/    postgres/ · kafka/ · amqp/ · http/ · memory/ (fakes)
```

The payoff is concrete rather than philosophical: domain rules can be exercised against in-memory
fakes in microseconds, the same use-case tests can run against real Postgres, and an
implementation can be swapped without touching a business rule.

The `domain/` package importing anything from `adapter/` is the single clearest sign the
architecture has broken. It is worth an explicit lint rule.

## 5. Domain model and ledger schema

### 5.1 Value objects

- `Money{ amount int64; currency Currency }` — **minor units as `int64`**. Never floats, never
  `numeric` for storage of amounts. `Add`/`Sub`/`Neg` return an error on currency mismatch. No
  `Divide` without an explicit rounding-strategy argument, because division is where money leaks.
- `Currency{ code, exponent }` — the exponent comes from the `currencies` table, **never hardcoded
  to 2**. USD is 2, JPY is 0, KWD is 3. Assuming two decimals is the most common money bug there is.
- `AccountID`, `TransactionID`, `PostingID` — distinct types over **UUIDv7**, which is time-ordered
  and therefore gives good index locality on an append-only journal.
- `IdempotencyKey`.
- `AccountType` — `asset | liability | equity | revenue | expense`. Determines whether a debit
  increases or decreases the account.

### 5.2 Chart of accounts

The part most homemade ledgers get wrong: **a customer wallet is a liability, not an asset.** If
Ali holds $100, the platform *owes Ali $100*. Internal accounts are first-class and named:

| Account | Type | Purpose |
|---|---|---|
| Customer wallet | liability | what the platform owes a customer |
| Bank / settlement | asset | real money the platform holds |
| Fee revenue | revenue | fees charged |
| FX position (per currency) | asset or liability | the platform's exposure in that currency; details deferred to M7 |
| FX gain/loss | revenue/expense | where realised conversion differences land |
| Rounding remainder | revenue/expense | where stray minor units land, because money may not evaporate |
| Suspense / clearing | liability | money in transit between saga steps |

Getting this right is what makes the trial balance genuinely close to zero across the whole system.

### 5.3 Sign convention (decided)

Postings carry a **signed amount** — debit positive, credit negative — with the invariant
`sum = 0` per `(transaction_id, currency)`. Not separate `debit`/`credit` columns.

Rationale: the balanced-transaction invariant becomes a single `SUM(...) = 0` check instead of a
two-column comparison, and "normal balance direction" is derived from `AccountType` at
presentation time. Consequence to remember: liability accounts hold credit-normal balances, so a
wallet's stored balance is negative internally and the sign is flipped when shown to the customer.

### 5.4 Schema

```sql
create table currencies (
  code      char(3) primary key,
  exponent  smallint not null check (exponent between 0 and 4)
);

create type account_type as enum ('asset','liability','equity','revenue','expense');

create table accounts (
  id        uuid primary key,
  type      account_type not null,
  currency  char(3) not null references currencies(code),
  owner_id  uuid,                      -- null for internal accounts
  code      text not null,
  closed_at timestamptz,
  unique (code, currency),
  unique (id, currency)                -- exists solely to enable the composite FK below
);

create table transactions (
  id              uuid primary key,     -- uuidv7
  idempotency_key text not null unique,
  kind            text not null,        -- transfer | deposit | withdrawal | fee | fx | reversal
  reverses_id     uuid references transactions(id),
  effective_at    timestamptz not null, -- business time
  created_at      timestamptz not null default now()   -- system time
);

create unique index transactions_one_reversal
  on transactions (reverses_id) where reverses_id is not null;

create table postings (
  id             uuid    primary key,
  transaction_id uuid    not null references transactions(id),
  account_id     uuid    not null,
  currency       char(3) not null,
  amount         bigint  not null check (amount <> 0),  -- signed minor units
  seq            smallint not null,
  unique (transaction_id, seq),
  foreign key (account_id, currency) references accounts (id, currency)
);

create table account_balances (
  account_id uuid    not null,
  currency   char(3) not null,
  posted     bigint  not null default 0,
  version    bigint  not null default 0,
  primary key (account_id, currency),
  foreign key (account_id, currency) references accounts (id, currency)
);
```

### 5.5 Invariants, and where each one lives

| Invariant | Enforced by | Why there |
|---|---|---|
| Postings sum to zero per transaction per currency | `CONSTRAINT TRIGGER … DEFERRABLE INITIALLY DEFERRED` on `postings` | Cannot be a `CHECK` — a sum cannot be checked while rows are still being inserted. Discovering this is the lesson. |
| A posting's currency matches its account's currency | composite FK `(account_id, currency)` | Makes the mistake *structurally impossible* rather than merely validated |
| A retry cannot double-post | `UNIQUE (idempotency_key)` on `transactions` | The database refuses, so the bug cannot occur even if the Go code is wrong |
| Postings are immutable | trigger raising on `UPDATE`/`DELETE`, plus revoked grants on the app role | Corrections are reversals; history must stay auditable |
| A transaction is reversed at most once | partial unique index on `reverses_id` | Cheap, exact, and impossible to forget |
| Available balance never goes negative | `OverdraftPolicy` in the domain + locking in the adapter | It is a cross-aggregate rule; see §6 |

**Where `reserved` comes from.** `account_balances` stores only `posted`. `reserved` is
**derived** from the `holds` table (`SUM(amount) WHERE state = 'placed'`), which arrives in M3 —
so in M1 and M2 `reserved` is zero and `available = posted`. This is deliberate: deriving it first
means the write-skew experiment in §8.3 operates on a real predicate over rows (which is what
makes write skew possible at all). Caching `reserved` as a column is a later optimisation, and if
it is ever added it becomes another thing Reconciliation must prove.
| Global trial balance is zero | Reconciliation, continuously | Nothing enforces it at write time; it must be independently verified |

`account_balances` is a **cache**, written in the same DB transaction as the postings. Its
`version` column is the surface used to compare optimistic locking, `SELECT … FOR UPDATE` and
`SERIALIZABLE`-with-retry on the same operation (§6.3). Reconciliation's standing job is to prove
`posted = SUM(postings)` forever.

`effective_at` versus `created_at` gives **bitemporality**: honest backdated entries, and the
ability to answer "what did we believe the balance was last Tuesday?"

## 6. Consistency: when one DB transaction, when a saga

### 6.1 The decision rule

> **Can all the writes reach one database in one DB transaction?**
> **Yes → use one DB transaction. No → you need a saga.**

A saga is not an advanced DB transaction. It is a **workaround for not having one**, used when the
writes are spread across services, databases, or an outside company's API. Using a saga where a
single transaction would do buys all of the cost and none of the reason.

| Flow | Design | Why |
|---|---|---|
| Internal transfer (wallet → wallet) | one atomic DB transaction | Two rows, one Postgres. Nothing else is justified. |
| Deposit from bank/card | saga | An outside provider is involved. Forced. |
| Withdrawal to bank | saga | Same. Forced. |

### 6.2 The aggregate-boundary tension

Two invariants are in play. **I1** (structural): a transaction sums to zero — *spans accounts*.
**I2** (business): an account's available balance must not go negative — *per account*.

| Aggregate choice | Enforces I1? | Enforces I2? |
|---|---|---|
| `JournalTransaction` | **Yes** — it holds every posting | **No** — the accounts are separate aggregates |
| `Account` | **No** — a transfer spans two aggregates | **Yes** — locally, trivially |

Neither gives both. This is the genuine design tension, not a modelling failure.

**Resolution for internal transfers:** `JournalTransaction` is the aggregate, and the use case
deliberately touches `account_balances` in the same DB transaction — a conscious, documented
departure from the one-aggregate-per-transaction guideline, because a synchronous overdraft check
is worth more than aggregate purity. This is what production ledgers do.

The *rule* stays in the domain as an `OverdraftPolicy` (`available = posted − reserved`); only the
*locking* lives in the adapter. That split is what keeps the hexagonal boundary honest.

**Optional side quest, after the real thing works:** implement the internal transfer as a saga
too, purely to feel firsthand why it is worse here. An experiment, never a production candidate.

### 6.3 Concurrency control, three ways behind one port

| Strategy | Mechanism | What it teaches |
|---|---|---|
| Pessimistic | `SELECT … FOR UPDATE` on affected balance rows, **ordered by account id** | Lock ordering as deadlock avoidance; lock waits; why the ordering rule is not optional |
| Optimistic | `UPDATE … WHERE version = $n`, retry on zero rows affected | Lost update; compare-and-swap; retry amplification under contention |
| Serializable | `SERIALIZABLE` + retry on SQLSTATE `40001` | SSI; predicate locking; write skew; false-positive aborts |

The retry loop belongs in the `UnitOfWork` adapter, not the domain — "retry on `40001`" is
infrastructure knowledge, and noticing that is part of the lesson.

```go
type UnitOfWork interface {
    Within(ctx context.Context, fn func(context.Context, Repositories) error) error
}
```

### 6.4 What a saga gives up, and the five conditions that make one safe

A DB transaction guarantees all-or-nothing **and** that nobody observes the middle. A saga keeps
the first only if every undo step is written correctly, and **loses the second entirely** — the
half-done state is visible.

That is acceptable only when the half-done state is a **true, nameable state** ("$30 is in
suspense, in transit"), never a lie ("$30 vanished"). Making it true is the entire job of the
suspense account.

**A saga is safe only with all five. The pattern alone is not safe:**

1. Every step balances on its own (suspense account) — the books are never wrong, only incomplete
2. Every step is idempotent — networks retry
3. Every undo is a new posting, never a deletion (a reversal) — history stays auditable
4. Saga state lives in the database — a crash resumes the job instead of forgetting it
5. Reconciliation independently proves the books close, and flags anything aged in suspense

## 7. Events and the two brokers

### 7.1 Two kinds of message

|  | **Event** | **Command** |
|---|---|---|
| Means | "this happened" | "please do this" |
| Naming | past tense — `TransactionPosted` | imperative — `ExecuteWithdrawal` |
| Can be refused? | No, it is a fact | Yes |
| Listeners | any number | exactly one handler |
| Needs retry/delay/DLQ? | rarely | constantly |

Events want a durable, replayable, per-key-ordered log readable by many independent listeners —
Kafka. Commands want per-message retry, delays and somewhere to put failures — RabbitMQ.

> **Rule for this repo: facts go to Kafka. Work and timers go to RabbitMQ.**

### 7.2 The dual-write problem

Money has been committed to Postgres; the event must now reach Kafka. Two orderings, both broken:

- **Commit, then publish** → the process dies in between. The money moved and **nobody was told**.
- **Publish, then commit** → the publish succeeds, the commit fails. You **announced fiction**.

There is no third ordering, because two separate systems cannot commit together.

### 7.3 Transactional outbox

Stop writing to two systems. Write the event into a table in the same database, in the same DB
transaction as the money:

```
BEGIN
  insert postings
  update account_balances
  insert outbox (event, payload, published_at = NULL)
COMMIT
```

All-or-nothing, by the database. A separate worker then publishes and marks rows sent. A crash
between publishing and marking produces a **duplicate**, which is expected and handled by §7.4.

The worker's claim query is itself a lesson:

```sql
SELECT * FROM outbox
WHERE published_at IS NULL
ORDER BY id
FOR UPDATE SKIP LOCKED
LIMIT 100;
```

`FOR UPDATE SKIP LOCKED` means "lock these rows, and skip any another worker already holds instead
of waiting" — the standard way to build a work queue on Postgres.

**Open trade-off:** several workers with `SKIP LOCKED` can publish slightly out of order. Either
run one worker per account-hash to preserve order, or make consumers order-tolerant. To be decided
deliberately, not discovered in production.

### 7.4 Inbox (consumer deduplication)

```
BEGIN
  insert into inbox (message_id)   -- UNIQUE; a duplicate fails here and aborts harmlessly
  ...do the work...
COMMIT
```

Outbox plus inbox is what people mean by "exactly-once". Precisely: exactly-once *delivery* over a
network is essentially unachievable; what is built here is **exactly-once effects** —
at-least-once delivery plus idempotent consumers.

### 7.5 Topology

**Kafka — facts.** Topics carry `.v1`; their shape is a contract other contexts depend on. Adding
a field is safe, renaming or removing one is not.

| Topic | Partition key | Consumed by |
|---|---|---|
| `ledger.transactions.v1` | transaction id | Reconciliation, audit |
| `ledger.account-entries.v1` | **account id** | Wallet projections, Risk |
| `authorization.events.v1` | account id | Wallet, Ledger, Risk |
| `payments.events.v1` | movement id | Wallet, Reconciliation |
| `risk.events.v1` | customer id | Wallet, Payments |

**Why two ledger topics.** A transaction touches two accounts, so there is no single account id to
key it by. Consumers needing per-account ordering (Risk counting velocity for one customer) need
one message per account, keyed by account. Consumers needing the whole balanced picture
(Reconciliation) need the transaction as a unit. This is the concrete moment where the partition
key is revealed to be a **domain decision, not a config value**.

Cost, stated rather than hidden: two message shapes for one fact, and a consumer may observe an
account entry before the transaction fact.

**RabbitMQ — work and timers.**

| Queue | Purpose | Mechanism |
|---|---|---|
| `psp.withdrawal.execute` | call the outside provider | competing consumers, retry ladder, DLQ |
| `psp.deposit.confirm` | confirm/poll a deposit | same |
| `hold.expire` | expire a hold at a set time | delayed message published when the hold is created |
| `saga.timeout` | escalate a stuck movement | delayed message |
| `*.dlq` | failed too many times; a human looks | dead letter exchange |

`hold.expire` is the clearest justification for RabbitMQ: "deliver this message in 30 minutes" is
not "read a log". The alternative is polling the database forever, which works but is worse.

### 7.6 Worked example — Ali withdraws $50

A wallet is a **liability**, so "debit Ali's wallet" *reduces what the platform owes him*.

**Step 1 — reserve.** One atomic DB transaction:

```
debit  Ali's wallet   +50      (the platform owes Ali $50 less)
credit Suspense       -50      (the obligation is now in transit)
                      ────
                        0  ✓
```
Movement → `Reserved`. The outbox row is inserted in the same DB transaction.

**Step 2 — ask the provider.** A RabbitMQ command `psp.withdrawal.execute` is dispatched. Three
outcomes:

- **Success** → `debit Suspense +50 / credit Cash −50`. Balanced. Movement → `Completed`.
- **Failure** → compensation as a **reversal**, never a delete:
  `debit Suspense +50 / credit Ali's wallet −50`. Movement → `Failed`.
- **Unknown** (the provider timed out) → **it is not known whether the money left.** This is the
  hardest genuine problem in payments; guessing is how real companies lose real money. The only
  safe moves are to retry with the *same idempotency key* so the provider deduplicates, or to poll
  its status endpoint. The movement stays non-terminal, which is the honest answer.

**Step 3 — the safety net.** A `saga.timeout` delayed message scheduled at step 1 escalates if the
movement is still non-terminal. Reconciliation independently watches suspense: **money aged in
suspense means a stuck saga.** The alarm falls out of the accounting rather than being bolted on.

Every individual step summed to zero. The books were never wrong — only incomplete.

## 8. Testing strategy

Sequenced last, per §3 — **except §8.3**, the concurrency anomaly experiments, which are part of
M2 by decision. Everything else in this section is M9.

### 8.1 Three tiers

| Tier | Runs against | Speed | Answers |
|---|---|---|---|
| Domain | in-memory fakes, no Docker | microseconds | Are the business rules right? |
| Integration | real Postgres via Testcontainers | seconds | Is the SQL right, and do the constraints actually fire? |
| System | real Postgres + Kafka + RabbitMQ | tens of seconds | Do sagas survive crashes and duplicates? |

**Prefer fakes over mocks.** A mock asserts that the code called the functions expected of it,
which is close to a tautology; a fake asserts that it works. The exception is the PSP, where
"we did not call the provider twice" is exactly the assertion needed.

### 8.2 Proving the invariants live in the schema

For each constraint, attempt the bad write **directly in SQL, bypassing all Go code**, and assert
Postgres refuses: unbalanced postings; a USD posting into a EUR account; a duplicate idempotency
key; an `UPDATE` or `DELETE` of a posting; a second reversal of one transaction.

This is the proof the invariants are in the schema and not merely in the application — and the
test that still protects the system when someone runs a script against the database directly.

### 8.3 The concurrency anomaly harness

**Method, in order:** reproduce the anomaly with a **failing** test → fix it with one specific
mechanism → confirm green → record which mechanism fixed it and why. Step one is the step people
skip, and the step that teaches.

**Technique.** A race cannot be reproduced by spawning goroutines and hoping. Control the
interleaving exactly: acquire **two separate database connections** and hold them, issue `BEGIN`
manually on each, and use a **barrier** (a channel both goroutines wait on) to force each step
into a precise order. For a lost update:

```
T1: BEGIN                       T1: read balance -> 100
      ┄ barrier ┄
T2: BEGIN                       T2: read balance -> 100
      ┄ barrier ┄
T1: write 20, COMMIT
      ┄ barrier ┄
T2: write 20, COMMIT            <- $160 spent from $100
```

Deterministic: it fails every time, because the order was dictated. That is what makes it a
harness rather than a stress test.

**The matrix.** Predictions, to be verified by Ali and then recorded as measured results:

| Anomaly | Read Committed | Repeatable Read | Serializable | `FOR UPDATE` | Version/CAS |
|---|---|---|---|---|---|
| Lost update — read-modify-write on one balance | breaks | *predicted:* refused with `40001` (first-updater-wins) | safe | safe | safe, with retry |
| Double-spend — two withdrawals check then debit | breaks | *predicted:* refused on the same row | safe | safe | safe |
| **Write skew** — two $60 holds on $100 | breaks | **breaks** | safe (`40001`) | safe *only if the right row is locked* | safe *only if the account is versioned* |
| Phantom — a `SUM` over rows that changes | breaks | breaks | safe | needs a parent row to lock | needs a parent row to version |
| Deadlock — transfers A→B and B→A at once | occurs | occurs | occurs | **prevented by lock ordering** | n/a |

**The write-skew lesson.** Two $60 holds against $100: each inserts a *new row* after checking
`SUM(existing holds) + 60 <= 100`. Nothing is overwritten, so there is no update conflict and
Repeatable Read has nothing to object to. Both succeed.

The instinct is to lock the `holds` rows — but the dangerous rows **do not exist yet**, and an
absent row cannot be locked. The **account** row must be locked, even though the insert targets
`holds`.

> **Lock the thing that guards the invariant, not the thing being written.**

Serializable catches it without that reasoning because it tracks the *query* rather than the rows
touched — which is what predicate locking means, and why Serializable costs more.

**Output:** a filled-in matrix with a short note per row, committed to `docs/anomalies/`, every
claim backed by a test in the repo. This is the single best artefact in the project to show an
interviewer: evidence rather than vocabulary.

### 8.4 Property-based tests

State a rule that must hold for *any* input and let the library generate hundreds of attempts to
break it. A ledger has properties that are absolutely true:

- for any sequence of valid operations, the sum of every posting in the database is exactly zero
- for any account, its cached balance equals the sum of its postings
- for any account, available equals posted minus active holds
- for any completed or failed movement, suspense returns to zero for that movement

Then run it as chaos: a few hundred random operations fired concurrently at random accounts, then
assert all four properties. This finds the bugs nobody was imaginative enough to write a test for —
in a ledger, most of them.

### 8.5 Crash and duplicate testing

Named **fault injection points** in each saga — `after_reserve`, `before_psp_call`,
`after_psp_call`, `before_settle` — configurable to panic. For **every** point: crash, restart,
let recovery run, then assert (1) the movement reaches a terminal state or is correctly still
pending, (2) **no money was created or destroyed**, (3) Reconciliation notices anything stuck.

Then the duplicate case: deliver every event **twice** and assert nothing happens twice.

### 8.6 Explicitly not doing

No mocked database for repository tests — the SQL is the thing under test. No coverage target — it
drives people to test getters; the anomaly matrix and the properties are the real measure. No unit
tests for trivial delegation. No testing that Postgres's `UNIQUE` works.

## 9. Roadmap — capability milestones

No dates. Each milestone is independently finishable and independently worth describing.

### M0 — Foundations
`docker-compose` with Postgres; migration tooling; the `Money` and `Currency` value objects.
**Done when:** `Money` arithmetic is correct, currency mismatch is an error, and the exponent
comes from data rather than a constant.

### M1 — The ledger core, atomic
The full schema with every constraint from §5.5; the chart of accounts seeded; the
`JournalTransaction` aggregate; a `PostTransaction` use case; the `UnitOfWork` port and its
Postgres adapter; the balance cache.
**Done when:** a balanced transaction can be posted over HTTP, the database refuses every invalid
shape when attacked directly in SQL, and the trial balance closes to zero.

### M2 — Concurrency, three ways  ← the database milestone
`OverdraftPolicy`; the three locking strategies behind one port; deadlock-safe lock ordering;
retry on `40001`; the anomaly harness (§8.3).
**Done when:** the anomaly matrix is filled in with measured results and a test behind every cell.

### M3 — Wallet and Authorization
Wallets, limits, posted vs available balance; holds with capture, release and expiry.
**Done when:** a hold reduces available without changing posted, capture posts a real transaction,
and expiry releases automatically.

### M4 — Outbox and Kafka
The outbox table and publisher worker with `SKIP LOCKED`; both ledger topics; the first consumer
(Risk velocity rules) with inbox deduplication.
**Done when:** killing the publisher mid-flight loses no events and duplicates no effects.

### M5 — RabbitMQ, timers and the PSP saga
Delayed messages for hold expiry and saga timeouts; a deliberately unreliable mock PSP that times
out and returns "unknown"; withdrawal and deposit sagas with compensation; retry ladder; DLQ.
**Done when:** every fault injection point survives a crash with money conserved.

### M6 — Reconciliation and observability
Continuous trial balance; suspense aging alerts; derived-vs-cached drift detection; OpenTelemetry
tracing propagated through broker headers.
**Done when:** one transfer is one end-to-end trace, and an artificially injected drift is caught
automatically.

### M7 — Multi-currency and FX
A second currency; FX as balanced multi-leg transactions; the rounding remainder account.
**Done when:** an FX conversion balances per currency and every stray minor unit is accounted for.

### M8 — Scale and operations (optional)
Journal partitioning; `EXPLAIN ANALYZE`-driven indexing; a load test; read replicas.
**Done when:** there are measured before-and-after numbers, not opinions.

### M9 — Test suite
Ali's sequencing: the ordinary unit and integration coverage of handlers, use cases and adapters
comes here, at the end.

## 10. Open questions

1. Migration tool: goose or golang-migrate.
2. Outbox ordering (§7.3): one publisher worker per account-hash, or order-tolerant consumers?
3. HTTP layer: net/http with the 1.22+ router, or chi? Leaning stdlib — fewer dependencies, more
   understanding.

### Resolved

- **Anomaly experiments live in M2, not M9** (decided by Ali). They are how Ali *sees* Postgres
  behave rather than tests that protect code. The ordinary test suite still goes to M9.
- **Project name is `yachtfund`** — `yacht` is the correct English spelling (`yatch` is a common
  misspelling and is in no dictionary). Renamed before the first commit, so it cost nothing.

## 11. Decision log

| # | Decision | Rationale |
|---|---|---|
| 1 | Double-entry ledger under a wallet product, not a generic ledger engine | Concrete features generate the hard problems naturally, and the story is legible in 30 seconds |
| 2 | Go + Postgres, hand-written SQL, no ORM | An ORM hides exactly the database behaviour that is the point; Go+Postgres is the dominant payments stack |
| 3 | Both Kafka and RabbitMQ, split facts/work | Each is the right tool for one job; "I used both and can say why" is a stronger answer than either alone |
| 4 | `int64` minor units; exponent from data | Floats lose money; hardcoded 2 decimals is wrong for JPY and KWD |
| 5 | Signed postings, `sum = 0` | One sum check instead of a two-column comparison |
| 6 | Invariants in the schema wherever possible | The database refuses bad data even when application code is wrong |
| 7 | One atomic DB transaction for internal transfers; sagas only where forced | A saga where a transaction would do buys all the cost and none of the reason |
| 8 | `JournalTransaction` as the aggregate, with a documented cross-aggregate write | A synchronous overdraft check is worth more than aggregate purity; documented, not accidental |
| 9 | Suspense account for every multi-step movement | Makes the half-done state true rather than a lie; hands Reconciliation a free alarm |
| 10 | Two ledger topics, keyed differently | The partition key must match what each consumer needs ordered |
| 11 | Ali implements; Claude mentors | Code Claude writes is code Ali does not learn from |
| 12 | Capability milestones, never dates | Ali's constraint; also keeps every stopping point complete |
