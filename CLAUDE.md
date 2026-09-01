# yatchfund

A digital wallet / neobank core built on a correct double-entry ledger.

Repo: `github.com/AliAsadiWasTaken/yatchfund` · Module: `github.com/AliAsadiWasTaken/yatchfund`

**See [GLOSSARY.md](GLOSSARY.md)** — every concept word in this project defined in plain language
with a concrete example. Keep it updated as new concepts appear.

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

## Working agreements for Claude in this repo

- **Brainstorm before building.** Design gets approved before code exists.
- **TDD.** For the ledger core this means: reproduce the anomaly with a failing test *before*
  fixing it. The failing test is the artefact, not just the fix.
- Prefer the boring, explicit thing in SQL. Readability of a query beats cleverness.
- When a design decision is made, record it here. This file is the session memory.
- Don't add a library where 30 lines of stdlib does the job — the point is to understand it.
- Ali is new to this vocabulary. Define loaded terms in plain language when they come up, and
  keep `GLOSSARY.md` up to date — every new concept word gets an entry with a concrete example.

## Status

- [x] Design Section 1 — domain model and ledger schema (approved)
- [x] Design Section 2 — atomic vs saga decision rule (approved; supersedes "build both")
- [ ] Design Section 3 — contexts, events, broker topology
- [ ] Design Section 4 — testing strategy incl. the anomaly harness
- [ ] Written spec + implementation plan
