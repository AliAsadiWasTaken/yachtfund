# M0 + M1 — Foundations and Ledger Core: Learning Plan

> **This is a plan for Ali to implement, not for Claude to execute.** Claude's role is mentor
> (see `CLAUDE.md`). No task below contains implementation code, by design — code Claude writes is
> code Ali does not learn from. Checkboxes are for Ali to tick.

**Goal:** a Postgres-backed ledger that can post a balanced double-entry transaction over HTTP,
and that refuses every invalid shape at the database level even when attacked directly in SQL.

**Architecture:** hexagonal, one bounded context to start (`internal/ledger`). Domain has stdlib
imports only. Invariants live in the schema wherever a schema can express them.

**Tech stack:** Go 1.26 · PostgreSQL · `pgx/v5` (no ORM) · goose or golang-migrate · stdlib
`net/http` · Docker Compose.

**Spec:** [`docs/superpowers/specs/2026-09-01-yachtfund-design.md`](../specs/2026-09-01-yachtfund-design.md)
— read §4 (architecture), §5 (domain model and schema) and §6.3 (locking) alongside this plan.

**Glossary:** [`GLOSSARY.md`](../../../GLOSSARY.md). Every bolded concept word below has an entry.

---

## Global constraints

Copied from the spec. Every task inherits these.

- **Money is `int64` minor units.** Never `float64`. Never `numeric` for storing amounts.
- **Currency exponent comes from the `currencies` table.** Never hardcoded to 2.
- **Signed postings:** debit positive, credit negative. Invariant is `sum = 0` per
  `(transaction_id, currency)`.
- **`internal/ledger/domain/` imports nothing outside the Go standard library.** No `pgx`, no
  `net/http`, no config, no logging library.
- **Postings are immutable.** No `UPDATE`, no `DELETE`, no soft deletes. Corrections are reversals.
- **A customer wallet is a `liability`.** The platform owes the customer.
- **No ORM.** Hand-written SQL.
- **Tests are M9**, except the experiments explicitly marked as experiments in this plan.

---

## How to use this plan

The rhythm is one task at a time:

1. Read the task's **Understand first** and **Go read** sections *before* writing anything.
2. Build it.
3. Run the **Verify** commands. They are exact; if one doesn't behave as described, that is
   information, not a mistake — bring it to me.
4. Tell me the task is done. I review against **What I'll check**, and we talk before you move on.

**When you get stuck:** tell me where, and what you tried. Per `CLAUDE.md` I will narrow the
problem, name the concept, and hint — I won't hand over the answer unless you explicitly ask for
it. Asking is allowed and is not failure; just say "give me the answer" and I will.

**Questions marked 🤔 are for you to answer, not me.** They are the parts of the design that carry
the actual learning. Bring your answer to review and we'll argue about it.

---

## Decisions needed before you start

Three open questions from the spec block M0. My recommendations, but yours to pick:

| Question | Recommendation | Why |
|---|---|---|
| Migration tool | **goose** | Supports both SQL and Go migrations, embeds cleanly via `embed.FS`, simple up/down semantics. golang-migrate works but its dirty-state handling bites people. |
| HTTP router | **stdlib `net/http`** | Since Go 1.22 the stdlib mux handles method+path patterns. One less dependency, and you'll understand what a router actually does. |
| Postgres major version | **pin an explicit major, never `latest`** | Pin whatever the current stable major is (18 as of late 2025 — check Docker Hub). `latest` means your database silently upgrades under you one day. |

---

## File structure

What exists at the end of M1, and what each file is responsible for.

```
compose.yaml                      Postgres service, healthcheck, named volume
Makefile                          up / down / psql / migrate-up / migrate-down / run
.env.example                      DSN and port, committed; .env is not

migrations/
  0001_currencies.sql             currencies table + seed
  0002_accounts.sql               account_type enum, accounts, unique(id,currency)
  0003_transactions_postings.sql  transactions, postings, composite FK, partial unique index
  0004_balanced_trigger.sql       the deferred constraint trigger
  0005_immutability.sql           UPDATE/DELETE guard trigger + role grants
  0006_account_balances.sql       balance cache table
  0007_chart_of_accounts.sql      internal accounts seed

internal/ledger/
  domain/
    money.go                      Money + Currency value objects, arithmetic
    account.go                    AccountID, AccountType, Account
    posting.go                    Posting value object
    journal.go                    JournalTransaction aggregate + its invariants
    errors.go                     domain error values
  app/
    port/
      repository.go               JournalRepository, BalanceRepository interfaces
      uow.go                      UnitOfWork interface
      clock.go                    Clock, IDGenerator interfaces
    post_transaction.go           the PostTransaction use case
    get_balance.go                the balance query
  adapter/
    postgres/
      pool.go                     pool construction and lifecycle
      uow.go                      UnitOfWork implementation over pgx
      journal_repo.go             the SQL for inserting transactions + postings
      balance_repo.go             the SQL for reading and updating balances
    http/
      server.go                   router and server wiring
      transactions.go             POST /transactions handler
      balances.go                 GET /accounts/{id}/balance handler

cmd/
  api/main.go                     composition root: builds adapters, injects into use cases
  trialbalance/main.go            prints the global trial balance
  scratch/main.go                 throwaway harness for eyeballing behaviour before M9's tests
                                  exist; gitignore it or delete it at the end of M1

docs/experiments/
  m1-schema-attacks.md            results of the constraint-attack experiments
```

**Why this shape:** `domain` is the innermost ring and depends on nothing. `app/port` declares what
the use cases need in *their* vocabulary. `adapter` implements those interfaces. `cmd` is the only
place that knows about all three — the **composition root**. If you ever find yourself importing
`adapter` from `domain`, the architecture has broken and it's worth stopping to fix.

---

# M0 — Foundations

**M0 is done when** `Money` arithmetic is correct, a currency mismatch is an error, the exponent
comes from data rather than a constant, and `make psql` drops you into a database with a
`currencies` table in it.

---

### Task M0.1 — Postgres in Docker, and a connection pool

**Files:** create `compose.yaml`, `Makefile`, `.env.example`, `internal/ledger/adapter/postgres/pool.go`

**Understand first**

- **Connection pool** — opening a TCP connection to Postgres and authenticating costs real
  milliseconds, so you keep a set of open connections and lend them out. `pgxpool` does this.
- Why `pgx` and not `database/sql`: `pgx` speaks Postgres's binary protocol natively, so it handles
  Postgres types (`uuid`, `numeric`, arrays, `timestamptz`) properly instead of stringifying
  everything. `database/sql` is a lowest-common-denominator interface across every SQL database.
- **Healthcheck** — Compose starting the container is not the same as Postgres being ready to
  accept connections. Without a healthcheck your app will race the database on `make up`.
- **Named volume** — where the data lives when the container is destroyed.

**Build**

- [ ] `compose.yaml`: one `postgres` service, explicit major version, a healthcheck using
      `pg_isready`, a named volume, port published to the host, credentials from environment.
- [ ] `.env.example` with `DATABASE_URL` and the Postgres credentials. Commit this file; `.env` is
      already gitignored.
- [ ] `Makefile` targets: `up`, `down`, `psql`, `logs`.
- [ ] `pool.go`: a constructor taking a DSN and returning a `*pgxpool.Pool`, plus a ping to prove
      the connection works. Think about pool size, and about who is responsible for closing it.

**🤔 For you to answer**

- Where should the DSN come from, and who is allowed to read it? Should `pool.go` read the
  environment itself, or be handed a DSN? (One of those answers makes `pool.go` untestable and
  couples it to your deployment. Which, and why?)
- What happens to an in-flight query when someone cancels the request? What does `pgx` need from
  you for that to work at all?

**Verify**

```bash
make up
docker compose ps          # the postgres service should report (healthy), not just Up
make psql                  # then, at the prompt:
#   select version();
#   \q
```

**What I'll check in review:** pool lifecycle (who closes it, and does it actually get closed),
whether `context.Context` is threaded through, whether the DSN is injected rather than read from
inside, and whether the healthcheck is real or decorative.

**Go read:** the `pgxpool` package docs, and the "Environment Variables" and "healthcheck" sections
of the official `postgres` Docker Hub image page.

---

### Task M0.2 — Migrations, and the `currencies` table

**Files:** create `migrations/0001_currencies.sql`, add `migrate-up` / `migrate-down` to `Makefile`

**Understand first**

- A **migration** is a numbered, ordered change to the schema. The number is the point: every
  environment applies the same changes in the same order and ends up identical.
- **You never edit a migration that has been applied.** You add a new one. Editing an applied
  migration means your database and your migration history disagree, silently and permanently.
- A **down migration** is the reverse. Writing it forces you to think about whether your change is
  actually reversible — some aren't, and knowing which is useful.

**Build**

- [ ] Install your chosen tool and add `migrate-up` / `migrate-down` Make targets.
- [ ] `0001_currencies.sql`: the `currencies` table per spec §5.4, plus a seed of USD (exponent 2),
      EUR (2), JPY (0) and KWD (3).
- [ ] Write the down migration too.

**🤔 For you to answer**

- Should the seed data live in a migration, or somewhere else? What breaks if a colleague changes
  USD's exponent in a migration that has already run in production?
- JPY has exponent 0 and KWD has 3. Given that, what is `¥100` in minor units, and what is
  `KWD 1.5`? Write those two answers down — you'll need the intuition in M0.3.

**Verify**

```bash
make migrate-up
make psql
#   \d currencies
#   select * from currencies order by code;
#   -- expect exactly 4 rows with exponents 2, 2, 0, 3
make migrate-down          # table should be gone
make migrate-up            # and back again, cleanly
```

**What I'll check in review:** the down migration actually reverses things, migration naming is
consistent and ordered, the `check (exponent between 0 and 4)` constraint is present, and whether
seed-vs-schema was thought about rather than defaulted.

**Go read:** your migration tool's README on file naming and up/down separation.

---

### Task M0.3 — `Money` and `Currency`

**Files:** create `internal/ledger/domain/money.go`, `internal/ledger/domain/errors.go`

This is the first real domain code, and the first place a mistake would corrupt every number in
the system. Take it slowly.

**Understand first**

- Run this in a Go playground before you write anything: print `0.1 + 0.2` as a `float64` with
  enough decimal places. You will not get `0.3`. **That** is why money is never a float — the
  error is in the representation, not in your arithmetic.
- **Minor units**: `$1.00` is stored as `100`. `¥100` is stored as `100`. `KWD 1.500` is stored as
  `1500`. The exponent tells you where the decimal point goes *for display only*.
- A **value object** is immutable and has no identity — any two `$30 USD` are interchangeable. So
  methods return new values; they never mutate the receiver.
- Invariants are enforced in the **constructor**. If a `Money` exists, it is valid.

**Build**

- [ ] `Currency`: code and exponent. A constructor that validates.
- [ ] `Money`: amount (`int64`) and currency. A constructor.
- [ ] `Add`, `Sub`, `Neg`, `IsZero`, `Equal`. Every operation involving two `Money` values must
      fail on a currency mismatch.
- [ ] A `String()` that formats using the exponent — so `Money{100, JPY}` prints `¥100` (or
      `JPY 100`) and `Money{100, USD}` prints `$1.00`.
- [ ] Sentinel errors in `errors.go` — e.g. a currency-mismatch error and an overflow error — as
      package-level values, so callers can compare against them.

**🤔 For you to answer — these are the interesting parts**

- **Where does `Currency` come from?** Exponents live in a database table, but `domain/` may not
  import `pgx`. So who constructs a `Currency`, and how does the exponent get from the table into
  the domain? There are at least three defensible answers. Pick one and be ready to defend it.
- **Overflow.** `int64` maxes out around 9.2 × 10¹⁸. Adding two `Money` values can overflow, and in
  Go that wraps around silently — a huge positive becomes a huge negative. Should `Add` return an
  error, or panic? Argue it. (Consider: is an overflowing balance a *bug in the caller* or a
  *runtime condition the caller should handle*? The answer determines which.)
- **No `Divide`.** The spec forbids division without an explicit rounding strategy. Work out why by
  trying it: split `$10.00` three ways in minor units. What is left over, and where must it go?
- Should `Money` be comparable with `==`? What does that depend on about your struct?

**Verify — without a test suite**

Tests are M9, so build `cmd/scratch/main.go` (delete it later, or keep it gitignored) that
exercises each operation and prints results, and check them by eye:

```
USD 100 + USD 250        -> 350       ("$3.50")
USD 100 + EUR 100        -> error
JPY 100 formatted        -> 100       (exponent 0, no decimal point)
KWD 1500 formatted       -> 1.500     (exponent 3)
Neg(USD 100)             -> -100
IsZero(USD 0)            -> true
math.MaxInt64 + USD 1    -> whatever you decided overflow should do
```

**One honest flag, then I'll drop it:** `Money` is pure logic with zero dependencies — it is the
cheapest thing in this entire project to test, and a single table-driven test would cover the whole
list above in about fifteen minutes and keep covering it forever. You've said tests are M9 and I'll
respect that; the scratch program above is a real substitute. But this is the one place where the
cost of deferring is highest, so I'm noting it once and then leaving it alone.

**What I'll check in review:** float64 nowhere near this file, mismatch handling on every binary
operation, the overflow decision (and that it's *a decision*), formatting driven by the exponent
rather than by `2`, immutability, and that `domain/` still imports only stdlib.

**Go read:** the "Floating-point arithmetic" trap is worth ten minutes on why `0.1` is not
representable in binary. Then Go's `errors.Is` and sentinel error conventions.

---

### 🚩 M0 review gate

Bring me: `compose.yaml`, the Makefile, `pool.go`, `0001_currencies.sql`, and `money.go`. We'll go
through your answers to the 🤔 questions — particularly where `Currency` comes from, because that
answer shapes every adapter you write afterwards.

- [ ] **Commit M0.** Suggested message: `feat: postgres, migrations and the Money value object`

---

# M1 — Ledger core, atomic

**M1 is done when** a balanced transaction can be posted over HTTP, the database refuses every
invalid shape when attacked directly in SQL, and the trial balance closes to zero.

Spec §5.4 gives you the target *shape* of the schema. What it does **not** give you — and what is
the actual work — is the trigger function bodies, the role grants, the seed, and every line of Go.

---

### Task M1.1 — `accounts`, and the enum

**Files:** create `migrations/0002_accounts.sql`, `internal/ledger/domain/account.go`

**Understand first**

- Postgres **enum types** are real types with a fixed set of values. Adding a value later is
  possible but awkward; removing one is not. The alternatives are a `CHECK` constraint or a lookup
  table — each with a different cost.
- The `unique (id, currency)` on `accounts` looks redundant next to the primary key on `id`. It is
  not: a **composite foreign key** can only point at a set of columns that has a unique index. That
  index exists purely so `postings` can reference `(account_id, currency)` in Task M1.2.

**Build**

- [ ] `0002_accounts.sql`: the `account_type` enum and the `accounts` table per spec §5.4,
      including both unique constraints and the FK to `currencies`.
- [ ] `account.go`: `AccountID` as a distinct type over UUID, `AccountType` as a domain type with
      the five values, and whatever `Account` needs to be. Stdlib only — think about where UUIDs
      come from given that constraint.

**🤔 For you to answer**

- Enum vs `CHECK` vs lookup table for `account_type` — you have to pick one to write the migration.
  Which, and what would make you regret it in a year?
- `owner_id` is nullable because internal accounts have no owner. Is nullable-means-internal a good
  design, or is there something better? What would you have to add to do it better, and is it worth
  it yet?
- Why `AccountID` as its own type rather than passing `uuid.UUID` around? Write down what bug this
  prevents. (Hint: imagine a function taking an account id and a transaction id.)

**Verify**

```bash
make migrate-up
make psql
#   \d accounts
#   \dT+ account_type
#   -- prove the FK works:
#   insert into accounts (id, type, currency, code)
#     values (gen_random_uuid(), 'liability', 'XXX', 'test');
#   -- expect: violates foreign key constraint
```

**What I'll check in review:** both unique constraints present, the FK to `currencies` present,
and whether `account.go` still imports only stdlib.

---

### Task M1.2 — `transactions` and `postings`

**Files:** create `migrations/0003_transactions_postings.sql`, `internal/ledger/domain/posting.go`

**Understand first**

- A **composite foreign key** on `(account_id, currency)` means a posting's currency *must* match
  its account's currency. Not validated — impossible. This is the clearest example in the project
  of an invariant living in the schema.
- A **partial unique index** (`... where reverses_id is not null`) enforces uniqueness only over
  rows matching a condition. Here: a transaction may be reversed at most once, but the many
  transactions that reverse nothing don't collide with each other on `NULL`.
- `unique (transaction_id, seq)` gives postings a stable order within a transaction. Think about
  why order matters at all for something that just has to sum to zero.

**Build**

- [ ] `0003_transactions_postings.sql`: both tables per spec §5.4 — the unique idempotency key, the
      self-referencing `reverses_id` FK, the partial unique index, `check (amount <> 0)`, and the
      composite FK.
- [ ] `posting.go`: the `Posting` value object.

**🤔 For you to answer**

- `check (amount <> 0)` forbids zero-amount postings. Why would a zero posting be a problem, given
  that it wouldn't break the sum-to-zero rule?
- `idempotency_key` is `text not null unique` — globally unique across every transaction ever. Is
  global the right scope? What would scoping it per-customer buy you, and what would it cost?
- `effective_at` is `not null` with no default, while `created_at` defaults to `now()`. Why the
  asymmetry?

**Verify**

```bash
make psql
#   -- create two accounts in different currencies first, then:
#   -- 1. try a posting whose currency differs from its account's -> composite FK rejects
#   -- 2. try amount = 0 -> check constraint rejects
#   -- 3. insert two transactions with the same idempotency_key -> unique index rejects
#   -- 4. insert two transactions reversing the same transaction -> partial index rejects
#   -- 5. insert two transactions reversing NOTHING (reverses_id null) -> both must SUCCEED
```

Number 5 is the one that proves you understand partial indexes. If it fails, your index is wrong.

**What I'll check in review:** all five behaviours above, and that you tried number 5 rather than
assuming it.

---

### Task M1.3 — The balanced-sum trigger 🌟

**Files:** create `migrations/0004_balanced_trigger.sql`

The most interesting piece of SQL in M1. Budget real time for it.

**Understand first**

- Try to express "the postings of a transaction sum to zero" as a `CHECK` constraint. You can't —
  a `CHECK` sees one row at a time, and this rule is about a *set* of rows. Prove this to yourself
  before reading on; the failure is the lesson.
- Even a trigger firing per row can't work: while you are inserting the first posting of a pair,
  the sum is legitimately non-zero. The rule is only meaningful **once the whole transaction is
  finished**.
- **`CONSTRAINT TRIGGER ... DEFERRABLE INITIALLY DEFERRED`** is the mechanism: the check runs at
  `COMMIT`, not at statement time. That is exactly the semantics you need.
- The check must group by `(transaction_id, currency)`, not just `transaction_id` — a multi-currency
  transaction must balance *within each currency* independently.

**Build**

- [ ] A trigger function that raises a useful error naming the offending transaction and currency.
- [ ] A constraint trigger on `postings`, deferrable, initially deferred.

**🤔 For you to answer**

- Should this trigger fire `FOR EACH ROW` or `FOR EACH STATEMENT`? What does each cost when a
  transaction has two postings, versus two hundred?
- Deferred until commit means an intermediate unbalanced state is *visible inside your own
  transaction*. Does that matter? Could another session ever see it?
- What error message would you want at 3am? Include the transaction id and the actual sum, not just
  "unbalanced".

**Verify**

```bash
make psql
--   begin;
--     insert a transaction, then ONE posting of +100
--     select * from postings;   -- your unbalanced row is visible here, inside the txn
--   commit;                     -- THIS is where it must fail
--
--   begin;
--     insert a transaction, then +100 and -100
--   commit;                     -- must succeed
--
--   begin;
--     +100 USD, -100 USD, +50 EUR    -- balances in USD, not in EUR
--   commit;                     -- must FAIL, and the error should say EUR
```

The middle of the first block is the moment to pause on: the constraint has not fired yet, and the
bad data is sitting there in front of you. Understanding *why that's fine* is understanding
deferred constraints.

**What I'll check in review:** deferred rather than immediate, grouping includes currency, error
message quality, and that you tried the mixed-currency case.

**Go read:** Postgres docs — `CREATE TRIGGER` (the `CONSTRAINT` and `DEFERRABLE` parts) and
`SET CONSTRAINTS`.

---

### Task M1.4 — Immutability: trigger plus grants

**Files:** create `migrations/0005_immutability.sql`

**Understand first**

- Two independent layers, and you want both. A **trigger** stops the write and explains why. A
  **`REVOKE`** means the application's database role never had permission in the first place.
- This implies **two roles**: an owner/migration role that can change the schema, and an
  application role that can `INSERT` and `SELECT` postings but not `UPDATE` or `DELETE` them. If
  your app connects as the owner, none of the grants mean anything.

**Build**

- [ ] A trigger on `postings` raising on `UPDATE` and on `DELETE`.
- [ ] An application role, with `INSERT` and `SELECT` on `postings` but not `UPDATE`/`DELETE`.
- [ ] Switch your `DATABASE_URL` to connect as the application role.

**🤔 For you to answer**

- Should `transactions` be immutable too, or only `postings`? What legitimate reason could there
  ever be to update a `transactions` row?
- Reconciliation needs to read everything but write nothing. Is that a third role? What is the
  actual argument for or against a read-only role here?
- Your migration tool needs `CREATE TABLE`. Your app must not have it. How do you arrange that
  without keeping two DSNs in your head?

**Verify**

```bash
make psql            # as the APP role now
#   update postings set amount = 999;   -- expect: permission denied (or the trigger's error)
#   delete from postings;               -- expect the same
#   insert ... a valid balanced pair    -- must still work
```

**What I'll check in review:** that you actually switched the app's DSN to the restricted role
(this is the step everyone skips, and it makes the grants theatre), and that inserts still work.

---

### Task M1.5 — `account_balances` and the chart of accounts

**Files:** create `migrations/0006_account_balances.sql`, `migrations/0007_chart_of_accounts.sql`

**Understand first**

- `account_balances` is a **cache**. The truth is `SUM(postings)`. The cache exists so a balance
  read doesn't scan a million rows. Every cache can drift, which is why Reconciliation exists in
  M6 — the cache is a performance decision that creates an ongoing obligation.
- The `version` column does nothing yet. It is the surface M2 uses for optimistic locking.
- Per spec §5.5, `reserved` is **not** a column — it's derived from `holds`, which arrives in M3.
  So in M1, `available = posted`.

**Build**

- [ ] `0006`: the `account_balances` table per spec §5.4, with its own composite FK.
- [ ] `0007`: seed the internal accounts from spec §5.2 — settlement/cash (asset), fee revenue
      (revenue), suspense (liability), rounding remainder. Give them stable, readable `code` values.

**🤔 For you to answer**

- Should there be a `account_balances` row for every account from the moment it's created, or
  created lazily on first posting? What does each choice do to the SQL you'll write in M1.9?
- The suspense account is a liability. Convince yourself why, using the withdrawal walkthrough in
  spec §7.6. If you conclude it should be something else, say so — I'd rather argue than have you
  accept it.

**Verify**

```bash
make psql
#   select code, type, currency from accounts where owner_id is null order by code;
#   select * from account_balances;
```

**What I'll check in review:** account types are right (especially suspense), codes are stable and
readable, and the lazy-vs-eager balance row decision was made deliberately.

---

### Task M1.6 — Schema attack experiments 🧪

**Files:** create `docs/experiments/m1-schema-attacks.md`

**This is an experiment, not a test suite** — it's part of M1's definition of done. You attack your
own schema from `psql`, bypassing every line of Go you will ever write, and record what happens.

The point: prove the invariants live in the **schema**. If any attack succeeds, the invariant was
only ever in your Go code, and a stray script — or a future bug — can corrupt the ledger.

**Build**

- [ ] Write the SQL for each attack yourself, run it, and record the exact error Postgres gives.

| # | Attack | Must be refused by |
|---|---|---|
| 1 | Insert a transaction with a single `+100` posting and commit | deferred balanced-sum trigger |
| 2 | Insert `+100 USD` into an account whose currency is EUR | composite FK |
| 3 | Insert a posting with `amount = 0` | check constraint |
| 4 | Insert two transactions with the same `idempotency_key` | unique index |
| 5 | `UPDATE` a posting's amount | immutability trigger and/or grants |
| 6 | `DELETE` a posting | immutability trigger and/or grants |
| 7 | Reverse the same transaction twice | partial unique index |
| 8 | A transaction balancing in USD but not in EUR | balanced-sum trigger, grouped by currency |
| 9 | Insert a posting referencing a non-existent account | FK |
| 10 | **Control:** two transactions that reverse nothing | must **SUCCEED** |
| 11 | **Control:** a correctly balanced two-posting transaction | must **SUCCEED** |

**🤔 For you to answer**

- Attacks 5 and 6 have two defences. Disable the trigger temporarily and confirm the grants alone
  stop it. Then re-enable. Do you actually need both? Argue either way — there's a real answer and
  a real cost.
- Which of these eleven could you *not* have expressed in the schema, and would have had to put in
  Go? What does that tell you about the limits of this approach?

**Verify:** all eleven behave as the table says, with the real Postgres error text recorded in your
document. If an attack you expected to fail succeeds, stop and bring it to me — that's a genuine
hole in the schema, and finding it now is the whole point of this task.

**What I'll check in review:** all eleven attempted (including both controls), real error text
rather than paraphrase, and your answer on whether both immutability defences are needed.

---

### Task M1.7 — The `JournalTransaction` aggregate

**Files:** create `internal/ledger/domain/journal.go`

**Understand first**

- An **aggregate** is a cluster of objects changed as one unit, with one **aggregate root** as the
  only way in. Here the root is `JournalTransaction` and the postings are its children. Nothing
  outside may hold or modify a `Posting` independently — that's what makes sum-to-zero enforceable.
- The aggregate's invariants are enforced at construction. If a `JournalTransaction` value exists,
  it is balanced. There is no "validate" method to forget to call.
- Invariants to enforce here: at least two postings; sums to zero per currency; every posting's
  currency matches its account; no zero amounts.

**Build**

- [ ] `JournalTransaction` with a constructor enforcing every invariant above.
- [ ] Whatever accessors the use case and the repository genuinely need — and no more. Think about
      what the repository needs to read in order to write SQL, and whether that can be given
      without exposing a mutable slice of postings.

**🤔 For you to answer — the big one**

- **The database already enforces sum-to-zero (Task M1.3). So why enforce it in the domain too?**
  This is not a rhetorical question and "defence in depth" is not a sufficient answer. There are at
  least three concrete reasons, and articulating them will tell you a lot about what a domain layer
  is actually *for*. Bring your answer to review.
- If a caller can get the postings slice out of the aggregate, they can append to it and break the
  invariant. How do you prevent that in Go, which has no `const`?
- Should `JournalTransaction` know its own id, or is the id assigned by the database? What does
  each choice do to your ability to build and reason about the aggregate in memory?

**Verify:** extend your `cmd/scratch` program — construct a balanced transaction (succeeds), an
unbalanced one (fails), a mixed-currency one that balances per currency (succeeds), a
single-posting one (fails).

**What I'll check in review:** invariants in the constructor rather than a separate validator, no
way to mutate a constructed aggregate from outside, stdlib-only imports, and your answer to the
"why enforce it twice" question.

---

### Task M1.8 — Ports

**Files:** create `internal/ledger/app/port/repository.go`, `uow.go`, `clock.go`

**Understand first**

- A **port** is an interface written in the *use case's* vocabulary, not the database's. Not
  `ExecQuery` — `Save(JournalTransaction)`.
- **Dependency inversion**: the interface is declared here, in the inner ring, and the adapter in
  the outer ring implements it. The arrows point inward. That is the whole trick of hexagonal
  architecture.
- `Clock` and `IDGenerator` are ports for the same reason a repository is: `time.Now()` and random
  UUIDs are uncontrollable inputs, and code that reaches for them directly cannot be reasoned about
  or replayed.

**Build**

- [ ] `UnitOfWork` — "run all of this in one DB transaction". Spec §6.3 sketches the shape:
      a method taking a context and a function, giving that function access to repositories that
      are all enrolled in the same DB transaction.
- [ ] `JournalRepository` — save a transaction; find one by idempotency key.
- [ ] Whatever balance access the use case needs.
- [ ] `Clock` and `IDGenerator`.

**🤔 For you to answer**

- The `UnitOfWork` hands the callback a set of repositories. **Why can't the use case just hold the
  repositories directly as struct fields?** Work through what would have to be true for two
  repository calls to land in the same DB transaction. This is the question the whole design of
  `UnitOfWork` answers.
- Retrying on SQLSTATE `40001` (M2) — does that belong in the port or the adapter? Justify it in
  terms of who is allowed to know that Postgres exists.
- Should the port return domain errors or database errors? Where does the translation happen?

**Verify:** `go build ./...` compiles, and `go list -deps` on the domain package shows no external
dependencies. Worth writing that check into your Makefile now, so drift gets caught.

**What I'll check in review:** interfaces phrased in domain language, `UnitOfWork` shaped so that
same-transaction guarantees are actually possible, and no `pgx` types anywhere in `port/`.

---

### Task M1.9 — The Postgres adapter

**Files:** create `internal/ledger/adapter/postgres/uow.go`, `journal_repo.go`, `balance_repo.go`

**Understand first**

- In `pgx`, a `pgxpool.Pool` and a `pgx.Tx` both satisfy a common query interface. That's what lets
  a repository run either inside a transaction or standalone — worth finding the exact interface
  name yourself, it's a good hunt.
- `defer tx.Rollback(ctx)` after a successful `Commit` is *not* an error — rollback on a committed
  transaction is a no-op. This is the standard Go pattern and it is how you guarantee no
  transaction is ever left open on a panic.
- Inserting many postings: one round trip per posting works but is wasteful. `pgx` offers batching
  and `COPY`. Know both exist; pick deliberately for two postings.

**Build**

- [ ] `uow.go`: `UnitOfWork` over `pgxpool` — begin, build repositories bound to the `pgx.Tx`, run
      the callback, commit on nil error, roll back on error or panic.
- [ ] `journal_repo.go`: insert the transaction row and its postings; look up by idempotency key.
- [ ] `balance_repo.go`: read a balance; apply a delta to `posted` and bump `version`.

**🤔 For you to answer**

- The balanced-sum trigger is **deferred**, so it fires at `COMMIT` — meaning `Commit()` can return
  a constraint violation, not just your inserts. Does your code handle an error from `Commit`? What
  do you translate it into?
- If the callback panics, is the transaction rolled back? Write the code so the answer is yes, and
  understand which mechanism makes it yes.
- You're updating `account_balances` for two accounts. **In what order?** There's a right answer
  and it's the subject of all of M2 — but form a hypothesis now and write it down. You'll get to
  find out whether you were right.
- How does a Postgres unique-violation error become the domain's "duplicate idempotency key"? Where
  does that translation live, and how do you detect it without string-matching the error message?

**Verify:** extend `cmd/scratch` to post a balanced transaction through the `UnitOfWork` and then
read the balances back. Then deliberately post an unbalanced one and confirm the error surfaces
from `Commit` as something meaningful rather than a raw `pgx` error.

**What I'll check in review:** rollback-on-panic, the error from `Commit` being handled, error
translation not done by string matching, and no domain types leaking database concerns.

---

### Task M1.10 — The `PostTransaction` use case

**Files:** create `internal/ledger/app/post_transaction.go`, `get_balance.go`

**Understand first**

- A **use case** orchestrates: validate input, build the aggregate, run the unit of work, return a
  result. It contains no SQL and no HTTP.
- **Idempotency**: the caller sends a key. If you've seen it, you must not post again — and what
  you *return* matters as much as what you don't do.

**Build**

- [ ] `PostTransaction`: takes a command (idempotency key, kind, effective time, the legs), builds
      the aggregate, runs it in one unit of work, applies the balance deltas.
- [ ] `GetBalance`: reads a balance and presents it with the correct sign for the account type.

**🤔 For you to answer**

- **A duplicate idempotency key arrives. What do you return?** An error? A success? The *original*
  transaction? Think about it from the caller's side: they retried because they don't know whether
  the first attempt worked. Which response makes their life correct rather than merely defined?
- Two requests with the same idempotency key arrive **simultaneously**. Your "check then insert"
  has a race. Does the unique index save you? What error do you get, and how must you respond to
  it? (You have now independently discovered a large part of M2.)
- `GetBalance` on a liability wallet: the stored number is negative and the customer must see a
  positive. Where does the sign flip belong — domain, use case, or HTTP layer? Argue it.

**Verify:** through `cmd/scratch` — post a transaction, read the balance, post the *same*
idempotency key again, and confirm your chosen behaviour happens. Then read the balance again and
confirm it did not move.

**What I'll check in review:** no SQL in the use case, the idempotency decision (and that it's
reasoned rather than default), the sign-flip placement, and whether the concurrent-duplicate race
was noticed.

---

### Task M1.11 — HTTP

**Files:** create `internal/ledger/adapter/http/server.go`, `transactions.go`, `balances.go`,
`cmd/api/main.go`

**Understand first**

- The HTTP layer is an **adapter**: parse the request into a use case command, call it, map the
  result to a status code. It holds no business logic. If you find yourself checking a balance
  here, something is in the wrong ring.
- `cmd/api/main.go` is the **composition root** — the one place that knows about the pool, the
  adapters and the use cases at once, and wires them together.
- Status codes carry meaning: what's the right one for an unbalanced transaction versus a duplicate
  idempotency key versus insufficient funds? They are not all `400`.

**Build**

- [ ] `POST /transactions` — JSON in, the posted transaction out.
- [ ] `GET /accounts/{id}/balance`.
- [ ] `server.go` with stdlib routing, and sensible server timeouts.
- [ ] `cmd/api/main.go`, and a `make run`.

**🤔 For you to answer**

- Map each failure to a status code and justify each: unbalanced transaction; unknown account;
  currency mismatch; duplicate idempotency key; a serialization failure that exhausted its retries.
- Should the idempotency key come from the JSON body or an `Idempotency-Key` header? Both are used
  in the industry. Which and why?
- `http.Server` has zero timeouts by default, which means a slow client can hold a connection
  forever. Which timeouts do you set, and what does each one actually bound?

**Verify**

```bash
make up && make migrate-up && make run
# in another shell — use real account ids from your seed:
curl -sS -X POST localhost:8080/transactions -H 'content-type: application/json' -d '{...}' | jq
curl -sS localhost:8080/accounts/<id>/balance | jq
# then POST the exact same body again and observe your idempotency behaviour
# then POST a deliberately unbalanced body and check the status code and message
```

**What I'll check in review:** no business logic in handlers, status codes justified, timeouts set,
and the composition root being the only place that imports both `app` and `adapter`.

---

### Task M1.12 — The trial balance

**Files:** create `cmd/trialbalance/main.go`

**Understand first**

- The **trial balance** is the whole-system check: sum every posting in the database, per currency.
  It must be exactly zero. Not close to zero — zero. It's integers; there is no rounding excuse.
- Second check: for every account, `account_balances.posted` must equal `SUM(postings)` for that
  account. This is the cache-drift check that becomes Reconciliation in M6.

**Build**

- [ ] A command printing, per currency: the global sum of postings, and any account whose cached
      balance disagrees with its postings. Exit non-zero if anything is wrong.

**🤔 For you to answer**

- Run it inside a DB transaction at `SERIALIZABLE`, or just query? What could a concurrent posting
  do to your answer if you don't? (This is a genuinely good question and you'll revisit it in M6.)
- Now break it on purpose: as the *owner* role, `UPDATE account_balances` to a wrong number, and
  confirm the check catches it. Notice that you had to escalate privileges to break it — that's the
  M1.4 grants earning their keep.

**Verify**

```bash
make migrate-up && make run          # post a few transactions first
go run ./cmd/trialbalance            # expect: zero per currency, no drift, exit 0
# then corrupt a cached balance as the owner role and run it again -> must report drift, exit 1
```

**What I'll check in review:** grouping by currency, the drift check present, non-zero exit on
failure, and that you actually corrupted a balance to prove the check works.

---

### 🚩 M1 review gate

Bring me the seven migrations, `docs/experiments/m1-schema-attacks.md`, the domain package, the
ports, the Postgres adapter, the use cases, the HTTP layer and the trial-balance command.

What we'll talk through:

- your answer to "why enforce sum-to-zero in both the schema and the domain"
- what you decided a duplicate idempotency key should return, and why
- the order in which you update the two `account_balances` rows — and your hypothesis about why it
  matters, which M2 will confirm or demolish
- whether both immutability defences are worth keeping
- anything in the attack experiments that surprised you

- [ ] **Commit M1.** Suggested message: `feat: ledger core with schema-enforced invariants`
- [ ] **Push to GitHub** — first push, so the repo name is now fixed for good.

---

## What M2 does with all this

So you can see where the thread goes. M2 takes the `version` column you created and never used, the
balance-update ordering you guessed at in M1.9, and the concurrent-duplicate race you found in
M1.10, and turns all three into the anomaly experiments: you'll make Ali's balance go negative on
purpose, then watch `FOR UPDATE`, a version check, and `SERIALIZABLE` each stop it — and find out
which of your M1 hypotheses were right.

Everything you built in M1 is the apparatus for that experiment. That's why it comes first.
