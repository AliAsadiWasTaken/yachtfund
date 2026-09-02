# yachtfund

A digital wallet / neobank core built on a correct double-entry ledger.

Repo: `github.com/AliAsadiWasTaken/yachtfund` · Module: `github.com/AliAsadiWasTaken/yachtfund`

## Why this project exists

This is a deliberate skill-building project, not a product. Three goals, in order:

1. **Databases** — specifically two areas: (a) transactions, isolation levels, MVCC, locking;
   (b) schema design where constraints carry the invariants.
2. **System design** — hexagonal architecture + DDD across real bounded contexts, with a
   message broker, sagas, and eventual consistency.
3. **Coding** — Go, hand-written SQL, no ORM.

Fintech was chosen because correctness is not optional there: a bug is a wrong balance, not a
wrong pixel. That pressure is the point.

## Non-negotiables (set by Ali, do not quietly relax these)

- **Real challenges only.** No CRUD filler. Every context must earn its existence with an
  invariant it owns. If a piece of the design is just folders, cut it.
- **Hexagonal + DDD**, properly: domain packages import nothing outside stdlib.
- **Both Kafka and RabbitMQ**, split by responsibility (see below) — each with a defensible reason.
- **Not calendar-driven.** Work is organised as capability milestones, each independently
  finishable and worth describing in an interview. Never plan around dates.
- **No ORM.** `pgx` + SQL written by hand.
- **Ali writes the code. Claude is the mentor.** See the section below — this is the most
  important rule in this file.
- **Tests come last** — the ordinary unit/integration suite is milestone M9. **One decided
  carve-out:** the concurrency anomaly experiments stay in M2, because they are how Ali *sees*
  Postgres behave, not a test protecting code. Ali agreed to this.

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.26 |
| Database | PostgreSQL |
| DB driver | `pgx` (no ORM) |
| Migrations | goose or golang-migrate (TBD) |
| Event backbone | **Kafka** — domain events, partitioned by account ID for per-account ordering, consumer groups, offsets, replay, compacted topics for projections |
| Work queues | **RabbitMQ** — delayed messages (hold/quote expiry), retry ladders with backoff, DLQs, competing consumers on the PSP gateway |
| Tests | domain: in-memory fakes, microseconds. integration: Testcontainers (Postgres + brokers) |
| Tracing | OpenTelemetry, trace context propagated through broker headers |

## Bounded contexts

Each owns an invariant. Nothing else may violate it.

| Context | Owns |
|---|---|
| **Ledger** (core domain) | Every journal transaction sums to zero per currency; postings are immutable. Sole writer of postings. |
| **Wallet** | Customer wallets, limits, KYC state; available vs posted balance. |
| **Authorization** | A hold is reserved-but-unposted funds; expires; captures at most once. |
| **Payments** | A money movement reaches exactly one terminal state. Owns compensation. |
| **Risk/Compliance** | Velocity and threshold rules; can freeze. Pure event consumer, deliberately eventually consistent. |
| **Reconciliation** | Derived truth == recorded truth. Independent verifier — must be able to disagree with the ledger. |

## Package layout (per context)

```
internal/<context>/
  domain/     aggregates, value objects, domain events, domain errors — stdlib imports only
  app/        use cases; app/port/ declares interfaces (Repository, UnitOfWork,
              EventPublisher, Clock, IDGenerator, PSPGateway)
  adapter/    postgres/ · kafka/ · amqp/ · http/ · memory/ (fakes)
```

## Ledger rules (decided — see Section 1 of the design)

- **Money is `int64` minor units.** Never floats. Never `numeric` for amounts.
- **Currency exponent comes from the `currencies` table, never hardcoded to 2.** JPY=0, KWD=3.
- **Signed postings**: debit positive, credit negative; invariant is `sum = 0` per
  `(transaction_id, currency)`. Not separate debit/credit columns.
- **A customer wallet is a liability**, not an asset. Internal accounts are first-class:
  cash/settlement (asset), fee revenue (revenue), FX position, rounding remainder, suspense.
  Rounding remainders must land in a real account — money never evaporates.
- **UUIDv7** for ids (time-ordered → index locality on an append-only journal).
- **Postings are immutable.** Corrections are reversal transactions (`reverses_id`), and a
  transaction can be reversed at most once (partial unique index). No soft deletes, ever.
- **Invariants live in the schema:**
  - composite FK `(account_id, currency) → accounts(id, currency)` makes a currency mismatch
    structurally impossible
  - balanced-sum requires a `CONSTRAINT TRIGGER ... DEFERRABLE INITIALLY DEFERRED`
    (it cannot be a `CHECK` — that is the lesson)
  - `idempotency_key` is `UNIQUE` on `transactions`, so a retry cannot double-post even if the
    Go code is wrong
  - immutability enforced by trigger + revoked UPDATE/DELETE grants on the app role
- **Bitemporal**: `effective_at` (business time) vs `created_at` (system time).
- `account_balances(account_id, currency, posted, version)` is a **cache** written in the same
  DB transaction as the postings. The `version` column is the surface for comparing optimistic
  locking vs `SELECT … FOR UPDATE` vs `SERIALIZABLE`-with-retry.

## Atomic vs saga — the decision rule (decided)

One question decides it, every time:

> **Can all the writes reach one database in one DB transaction?**
> **Yes -> use one DB transaction. No -> you need a saga.**

A saga is not an advanced DB transaction. It is a **workaround for not having one**. Using a saga
where a single transaction would do buys all of the cost and none of the reason.

| Flow | Design | Why |
|---|---|---|
| Internal transfer (wallet -> wallet) | **One atomic DB transaction** | Two rows, one Postgres. Nothing else is justified. |
| Deposit from bank/card | **Saga** | An outside provider is involved. Forced. |
| Withdrawal to bank | **Saga** | Same. Forced. |

What a saga gives up: a DB transaction guarantees all-or-nothing *and* that nobody sees the
middle. A saga keeps all-or-nothing only if every undo step is written correctly, and **loses**
the second guarantee entirely — the half-done state is visible. That is acceptable only when the
half-done state is a **true, nameable state** ("$30 is in suspense, in transit"), never a lie
("$30 vanished"). Making it true is the entire job of the suspense account.

**A saga is safe only with all five of these.** The pattern alone is not safe:

1. Every step balances on its own (suspense account) — the books are never wrong, only incomplete
2. Every step is idempotent — networks retry
3. Every undo is a new posting, never a deletion (a reversal) — history stays auditable
4. Saga state lives in the database — a crash resumes the job instead of forgetting it
5. Reconciliation independently proves the books close, and flags anything aged in suspense

### The aggregate-boundary tension (still real, still worth understanding)

A transfer touches two accounts, so no aggregate boundary makes both invariants atomic:

- `JournalTransaction` as aggregate -> can enforce "sums to zero" (I1), cannot reach the account
  balances to enforce "no negative balance" (I2)
- `Account` as aggregate -> can enforce I2 locally, cannot span the two accounts for I1

**Resolution for internal transfers:** `JournalTransaction` is the aggregate, and the use case
deliberately touches account balances in the same DB transaction — a conscious, documented
departure from the one-aggregate-per-transaction guideline, because a synchronous overdraft check
is worth more than aggregate purity. The *rule* stays in the domain as an `OverdraftPolicy`
(`available = posted - reserved`); only the *locking* lives in the adapter.

**Optional side quest, after the real thing works:** implement the internal transfer as a saga
too, purely to feel firsthand why it is worse here. An experiment, never a production candidate.

## Challenge inventory (the actual work)

1. Aggregate boundaries vs atomicity — the tension above, resolved deliberately and documented
2. Exactly-once effects over at-least-once delivery — transactional outbox + consumer inbox/dedupe
3. Per-account ordering via partition key
4. Sagas and compensation; PSP returns "unknown" — now what?
5. Timeouts and scheduled domain events (hold expiry, saga deadlines) without polling
6. **Concurrency anomaly harness** — a failing test per anomaly first, then the fix, then a note
   on which isolation level or lock removed it: double-spend, lost update, write skew
   (two holds each within balance, together overdrawn), phantom read, deadlock + retry
7. Invariants in the schema (above)
8. Multi-currency and FX — each currency balances independently; rounding remainder accounting
9. Eventual consistency at the API edge — read-your-writes, or honest pending states
10. Crash correctness — kill at every saga step; reconciliation must always close to zero
11. Distributed tracing across the broker — one transfer, one trace
12. Continuous trial balance — global sum of postings == 0, and derived == cached

## Claude's role: mentor, not implementer

**Ali implements everything. Claude does not write implementation code.**

This project exists so Ali learns. Code written by Claude is code Ali did not learn from, so
writing it defeats the entire purpose — even when it would be faster, even when Ali is stuck,
even when the code is "boring plumbing".

**What Claude does:**

- designs and explains, in plain language, defining loaded vocabulary as it comes up
- says what to build next, and what concepts to understand *before* building it
- points at the right documentation or the right Postgres behaviour to go read
- reviews Ali's code: correctness first, then invariants, then clarity — and says plainly when
  something is wrong
- asks the awkward questions ("what happens if this crashes here?", "what row does that lock?")
- writes and maintains docs: this file, `GLOSSARY.md`, specs, the anomaly matrix
- when Ali is stuck: narrows the problem, offers a hint, names the concept — does not hand over
  the answer unless Ali explicitly asks for it

**What Claude does not do:**

- write implementation code, unless Ali explicitly asks for a specific snippet
- write Ali's tests for him
- "just fix it" — a bug Ali is working through is the lesson, not an obstacle

If Ali asks directly for code, give it — but say what it demonstrates, so it teaches rather than
just lands.

## Working agreements for Claude in this repo

- **Brainstorm before building.** Design gets approved before code exists.
- **Test suite comes last** (M9, Ali's call). Decided exception: the concurrency anomaly
  experiments stay in M2 — they teach Postgres behaviour rather than protect code.
- Prefer the boring, explicit thing in SQL. Readability of a query beats cleverness.
- When a design decision is made, record it here. This file is the session memory.
- Don't add a library where 30 lines of stdlib does the job — the point is to understand it.
- Ali is new to this vocabulary. Define loaded terms in plain language when they come up, and
  keep `GLOSSARY.md` up to date — every new concept word gets an entry with a concrete example.

## Open questions

1. **Does the anomaly harness stay in the concurrency milestone, or move to the end with the rest
   of the tests?** Claude's view: keep it in the concurrency milestone. It is not a test suite —
   it is how Ali *sees* write skew happen and *sees* a mechanism stop it. Deferred to the end, the
   concurrency milestone becomes "write locking code and hope", and the two database gaps Ali
   named never close. Ali's call.
2. Repo name spelling: `yachtfund` as given, or `yachtfund`? Cheap now, annoying after first push.
3. Migration tool: goose or golang-migrate.

## Where we left off  (update this at the end of every session)

**Last session: 2026-09-01.** Design complete and approved; spec and M0/M1 plan written. No code
exists yet. Ali is starting **Task M0.1** (Postgres in Docker + a pgx connection pool).

**Decided in M0.1 so far:** `NewPool` takes the DSN as a parameter rather than reading the
environment itself — dependency injection, so a test database can be wired in and so the
migration role and app role DSNs (M1.4) can coexist. Ali reached this himself.

**Still open for Ali in M0.1:** the cancellation question — what happens to an in-flight
`SELECT pg_sleep(10)` in `pg_stat_activity` when the Go context is cancelled, and what must be true
of every function between the HTTP handler and the query for that cancellation to arrive.

**Still to choose before M0 proper:** migration tool (recommended: goose), HTTP router
(recommended: stdlib `net/http`), Postgres major version (pin it explicitly, never `latest`).

## Status

- [x] Design Section 1 — domain model and ledger schema (approved)
- [x] Design Section 2 — atomic vs saga decision rule (approved; supersedes "build both")
- [x] Design Section 3 — contexts, events, broker topology (approved)
- [x] Design Section 4 — testing strategy incl. the anomaly harness (approved)
- [x] Spec written: `docs/superpowers/specs/2026-09-01-yachtfund-design.md` (awaiting Ali's review)
- [x] Plan written for M0+M1: `docs/superpowers/plans/2026-09-01-m0-m1-foundations-and-ledger-core.md`
- [ ] M0 — Foundations (spec §9). Blocked on Ali's three choices: migration tool, HTTP router,
      Postgres major version — recommendations are in the plan.
- [ ] M1 — Ledger core (spec §9)
