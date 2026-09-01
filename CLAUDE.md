# yatchfund

A digital wallet / neobank core built on a correct double-entry ledger.

Repo: `github.com/AliAsadiWasTaken/yatchfund` · Module: `github.com/AliAsadiWasTaken/yatchfund`

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

## The flagship exercise

A transfer touches two accounts, so the DDD aggregate boundary is genuinely contested:

- `JournalTransaction` as aggregate → balanced-sum is enforceable inside it, but
  "balance must not go negative" becomes a cross-aggregate invariant with no atomic home.
- `Account` as aggregate → non-negative balance is trivial, but a transfer becomes a process
  manager coordinating two aggregates: reserve → commit → compensate.

**Build both.** Compare under load and under `kill -9` mid-flight: latency, contention, and what
each does to correctness. Selecting between them should be a port/adapter choice.

## Challenge inventory (the actual work)

1. Aggregate boundaries vs atomicity — built twice, measured
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

## Working agreements for Claude in this repo

- **Brainstorm before building.** Design gets approved before code exists.
- **TDD.** For the ledger core this means: reproduce the anomaly with a failing test *before*
  fixing it. The failing test is the artefact, not just the fix.
- Prefer the boring, explicit thing in SQL. Readability of a query beats cleverness.
- When a design decision is made, record it here. This file is the session memory.
- Don't add a library where 30 lines of stdlib does the job — the point is to understand it.

## Status

- [x] Design Section 1 — domain model and ledger schema (approved)
- [ ] Design Section 2 — aggregate-boundary fork and the two transfer implementations
- [ ] Design Section 3 — contexts, events, broker topology
- [ ] Design Section 4 — testing strategy incl. the anomaly harness
- [ ] Written spec + implementation plan
