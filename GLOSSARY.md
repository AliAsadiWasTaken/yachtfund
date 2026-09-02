# Glossary

Every concept word used in this project, in plain language, with a concrete example.

Running example throughout: **Ali has $100. Sara has $0. Ali sends Sara $30.**

---

## Accounting and the ledger

**Ledger** — a notebook of money movements. Nothing is ever edited or deleted in it; mistakes are
corrected by adding an opposite entry.

**Double-entry** — the rule that every movement is written twice: once for where money left, once
for where it arrived. The two lines cancel to zero, which proves the money came from somewhere
real instead of being invented.

**Posting** — one line in the ledger. `Ali's wallet -30` is a posting.

**Journal transaction** — a complete set of postings that belong together and sum to zero.
Ali's transfer is one transaction made of two postings.

*Careful:* "transaction" means two different things in this project. A **journal transaction** is
money movement. A **DB transaction** is a database mechanism. This repo always says "DB
transaction" for the second one.

**Debit / credit** — the two directions of a posting. We store them as a **signed amount**: debit
is positive, credit is negative. Whether a debit *increases* or *decreases* an account depends on
the account's type, which is why `account_type` exists.

**Chart of accounts** — the named list of every account in the system, including internal ones.

**Liability** — money you owe someone. A customer's wallet is a liability of the platform, not an
asset. If Ali has $100 in his wallet, the platform *owes Ali $100*. Getting this backwards is the
most common mistake in homemade ledgers.

**Asset** — money or value the platform holds, e.g. the real bank/settlement account.

**Suspense account (also: clearing account)** — a named holding account for money that is in
transit. When a movement happens in more than one step, money sits in suspense between steps, so
every individual step still balances to zero. Money is always *somewhere nameable*.

**Rounding remainder account** — where stray minor units go when a currency conversion doesn't
divide evenly. It exists because money is not allowed to evaporate.

**Reversal** — the way you undo something in a ledger. You never delete a posting; you add an
opposite posting that references the original. History stays intact and auditable.

**Trial balance** — the check that the sum of *every* posting in the whole system is exactly zero.
If it isn't, something is broken. Run continuously here.

**Reconciliation** — independently recomputing the truth and comparing it to what's recorded. Its
job is to be *able to disagree* with the ledger.

**Hold (authorization)** — money reserved but not yet taken. Like a hotel putting $200 aside on
your card. Reduces *available* balance without changing *posted* balance.

**Posted vs available balance** — posted is what the ledger says you have. Available is
`posted - active holds`, which is what you can actually spend.

**Minor units** — money stored as a whole number of the smallest unit: $1.00 is stored as `100`.
Never use floating point for money; `0.1 + 0.2` is not `0.3` in binary floating point.

**Currency exponent** — how many decimal places a currency has. USD is 2, JPY is 0, KWD is 3.
Hardcoding 2 is a classic bug, so it comes from the `currencies` table.

**Bitemporality** — storing two different times: `effective_at` (when it happened in business
terms) and `created_at` (when we recorded it). Lets you answer "what did we believe last Tuesday?"
and lets you record backdated entries honestly.

---

## Databases

**DB transaction** — a group of writes that all happen or none happen (`BEGIN … COMMIT`).

**Atomic** — all-or-nothing. Why a crash can't leave Ali debited and Sara not credited.

**ACID** — the four properties a DB transaction gives you: **A**tomic (all or nothing),
**C**onsistent (constraints hold), **I**solated (concurrent transactions don't corrupt each
other), **D**urable (committed means survives a power cut).

**Invariant** — a rule that must be true *always*, no exceptions. Not "we validate it" but "it
cannot be false." Our two: every transaction sums to zero; no balance goes negative.

**Constraint** — a rule enforced by the database itself, so it holds even if the application code
is wrong. `CHECK`, `UNIQUE`, `FOREIGN KEY`. The point of putting invariants here is that a bug in
Go cannot produce bad data.

**Foreign key (FK)** — a column that must point at an existing row in another table.
A *composite* FK points at multiple columns at once — we use `(account_id, currency)` so posting
USD into a EUR account is structurally impossible rather than merely checked.

**Partial index** — an index (and a uniqueness rule) that applies only to rows matching a
condition, e.g. "only one *active* hold per authorization" using `WHERE state = 'placed'`.

**Deferred constraint trigger** — a check the database runs at `COMMIT` time rather than per row.
Needed for "these postings sum to zero", because you can't check a sum while you're still
inserting the rows. This is why balanced-sum cannot be a plain `CHECK`.

**Race condition** — two things happening at once producing a result neither would alone.
Ali has $100, two $80 transfers arrive simultaneously, both check "does he have $80? yes", both
write. Ali ends at -$60.

**Double-spend** — the money version of a race condition: the same balance spent twice.

**Isolation level** — how much two simultaneous DB transactions can see and interfere with each
other. Postgres, cheapest to strictest: **Read Committed** (default), **Repeatable Read**,
**Serializable**. Stricter is safer, slower, and produces more retries.

**MVCC (Multi-Version Concurrency Control)** — how Postgres lets readers and writers avoid
blocking each other: each transaction sees a consistent snapshot of the data rather than the
live rows. Explains most surprising isolation behaviour.

**Lost update** — two DB transactions read `100`, both compute a new value, and the second write
erases the first's work.

**Write skew** — the subtle one. Two DB transactions each check a rule, each is individually
correct, but together they break it. Ali has $100; two holds of $60 each arrive at once; each
checks "is 60 <= 100? yes"; together they reserve $120. **Nothing was overwritten**, which is why
this is not a lost update, why Read Committed and Repeatable Read both miss it, and why only
Serializable catches it. The single most instructive anomaly in this project.

**Phantom read** — a row appears (or disappears) between two reads in the same DB transaction,
changing the answer to a query you already asked.

**Pessimistic locking** — claim the row first and make everyone else wait. `SELECT … FOR UPDATE`
means "give me this row and let no one else touch it until I commit." Like taking a file off the
shelf.

**Optimistic locking** — don't lock. Note the row's `version` (say 7) and write with
`… WHERE version = 7`. If someone got there first, your write affects zero rows and you retry.
Like a wiki saying "someone else edited this page."

**CAS (compare-and-swap)** — the general name for the "only write if it still looks like this"
technique that optimistic locking uses.

**Deadlock** — A holds row 1 and wants row 2; B holds row 2 and wants row 1. Both wait forever, so
Postgres kills one. Prevented entirely by **always locking rows in the same order** (sorted by id).

**Serialization failure (SQLSTATE `40001`)** — under Serializable, Postgres notices two DB
transactions cannot both be valid and aborts one with this error code. Normal operation, not a
bug: catch it and retry.

**Retry amplification** — when retries under contention create more contention, making things
worse. Why retry strategy needs measuring, not guessing.

**Idempotency** — doing something twice has the same effect as doing it once. Required, because
networks retry.

**Idempotency key** — a unique id the caller sends with a request. A `UNIQUE` constraint on it
means a duplicate simply fails to insert instead of paying Sara twice — the database refuses, so
the bug cannot happen even if the Go code is wrong.

**EXPLAIN ANALYZE** — the Postgres command that shows how a query was actually executed and where
the time went. How you find out why something is slow instead of guessing.

**Partitioning** — splitting one huge table into physical pieces (e.g. by month) so queries and
maintenance touch less data. Relevant once the journal is large.

**Migration** — a versioned, ordered script that changes the database schema, so every environment
gets the same structure in the same order.

---

## Design (DDD and hexagonal)

**Domain** — the actual business ideas: money, accounts, holds, transfers. Not HTTP, not SQL, not
Kafka.

**DDD (Domain-Driven Design)** — writing the business rules as clean, standalone code, and keeping
plumbing far away from them.

**Value object** — a small immutable thing defined only by its values, with no identity. `Money{30,
USD}` is a value object: any two $30 USD are interchangeable.

**Entity** — a thing with an identity that persists as its values change. Ali's account is the same
account whether it holds $100 or $70.

**Aggregate** — a group of things you always change together, with one object in charge (the
**aggregate root**). The root is the only way in and guarantees its own rules. Core guideline:
**change one aggregate per DB transaction.** `JournalTransaction` plus its postings is an
aggregate — you never edit one posting alone, because that would break the sums-to-zero rule.

**Cross-aggregate invariant** — a rule that spans more than one aggregate, so no single aggregate
can enforce it atomically. "No negative balance" during a transfer is one. The reason the
aggregate-boundary question in this project has no free answer.

**Bounded context** — a section of the system with its own vocabulary and responsibility, like
departments in a bank. "Account" means something slightly different in Ledger than in Compliance,
and pretending otherwise is what makes large systems rot.

**Context map** — the picture of which contexts exist and how they talk to each other.

**Domain service / policy** — a business rule that doesn't naturally belong to a single object.
`OverdraftPolicy` deciding whether a debit is permitted is one.

**Hexagonal architecture (ports and adapters)** — three layers:
- **domain** — pure rules; imports nothing from Postgres, Kafka or HTTP
- **port** — an interface written from the business side's point of view ("I need something that
  can save a transaction")
- **adapter** — the real implementation of a port: a Postgres one, and a fake in-memory one

The payoff is concrete: rule tests run against the fake in microseconds, the *same* tests run
against real Postgres, and implementations can be swapped without touching a business rule.

**Dependency inversion** — the reason hexagonal works: the domain defines the interface, and
infrastructure implements it, so the arrows point inward and the domain depends on nothing.

**Dependency injection** — giving a function or struct what it needs as an argument instead of
letting it fetch things for itself. `NewPool(ctx, dsn)` is injected; a `NewPool(ctx)` that reads
`os.Getenv("DATABASE_URL")` internally is not. The defect in the second one is that its signature
*lies* about what it depends on — being hard to test is how you notice, not the reason it's wrong.

**Composition root** — the single place that knows about every layer at once and wires them
together. Here that is `cmd/api/main.go`: it reads configuration, builds the adapters, injects them
into the use cases. Nothing deeper in the program reads the environment.

**DSN (Data Source Name)** — the connection string that says where a database is and who is
connecting: `postgres://user:pass@host:5432/dbname`. Configuration, therefore owned by the
composition root.

**Unit of Work** — a port meaning "run all of this inside one DB transaction", so domain code
never mentions `BEGIN` or `COMMIT`. Retrying on `40001` belongs in its adapter, because that is
infrastructure knowledge.

**Repository** — a port that looks like a collection ("save this transaction", "get this account")
and hides the SQL behind it.

**Saga** — a long job broken into small steps, each its own DB transaction, each with an undo.
**Not** an advanced DB transaction — a *workaround for not having one*, used when writes are spread
across services, databases, or an outside company's API.

**Compensation** — the undo step of a saga. In a ledger this is always a reversal posting, never a
deletion.

**Process manager (also: orchestrator)** — the small piece of code that remembers which saga step
we're on and what happens next. Its state lives in the database so a crash resumes rather than
forgets.

**Eventual consistency** — for a short moment different parts of the system disagree, then they
catch up. A real trade-off, not a flaw — but only acceptable when the in-between state is *true*
(money in suspense) rather than a lie (money missing).

**Read-your-writes** — the specific problem where a client posts a transfer and immediately reads
a stale balance. Either solved explicitly, or surfaced honestly as a pending state.

---

## Messaging

**Message broker** — infrastructure that carries messages between services so they don't call each
other directly. Kafka and RabbitMQ are the two used here.

**Domain event** — a statement of fact about something that *already happened*, named in the past
tense and never rejected: `TransactionPosted`, `HoldExpired`. Many listeners may care.

**Command** — a *request* for something to happen, which may be rejected, and which has exactly
one handler: `ExecuteWithdrawal`. The event/command distinction is precisely why two brokers are
justified here.

**Publish / subscribe** — one service announces an event; any number of others listen.

**Producer / consumer** — the thing that sends messages; the thing that receives them.

**Topic** — a named stream of messages in Kafka.

**Partition** — a lane within a topic. Messages with the same **partition key** always go to the
same lane and are handled **in order** by one worker. Key by account id and every transfer touching
Ali is processed one at a time — the race becomes impossible *by construction* instead of by
locking. Choosing the key is a domain decision, not a config value.

**Single-writer** — the resulting design where exactly one worker may modify a given account at a
time.

**Consumer group** — a set of workers sharing out a topic's partitions between them, so you scale
by adding workers.

**Offset** — a consumer's bookmark: how far through a partition it has read.

**Replay** — re-reading a Kafka topic from the beginning to rebuild something. Possible because
Kafka is a durable log, not a queue that forgets.

**Log compaction** — a Kafka mode that keeps only the latest message per key, turning a topic into
a snapshot of current state.

**At-most-once / at-least-once / exactly-once** — delivery guarantees.
At-most-once may lose messages. At-least-once never loses but may duplicate — **this is what real
brokers give you**. Exactly-once *delivery* is essentially unachievable across a network; what you
actually build is **exactly-once effects** = at-least-once delivery + idempotent consumers.

**The dual-write problem** — you committed Ali -30 / Sara +30, and now want to publish
`TransactionPosted`. Commit first and the process may die before publishing (money moved, nobody
told). Publish first and the commit may fail (announced something that never happened). **No
ordering works**, because you're writing to two systems that can't commit together.

**Transactional outbox** — the fix. Write the event into an `outbox` table *in the same DB
transaction* as the money, so it's all-or-nothing by the database. A separate worker reads the
table and publishes, marking rows sent. If it crashes it re-sends, producing duplicates — which is
fine, because consumers are idempotent.

**Inbox (dedupe table)** — the consumer's half. Keep a table of processed message ids; insert the
id and do the work in the *same* DB transaction. A duplicate hits the unique constraint and is
skipped. Together with the outbox, this is how exactly-once *effects* are achieved.

**Ack / nack** — a consumer telling the broker "handled it" or "couldn't handle it". RabbitMQ works
per message this way; Kafka works with offsets instead.

**Retry ladder / backoff** — retrying a failed message after increasing delays (1s, 10s, 1m, 10m)
instead of hammering a broken provider.

**DLQ (dead letter queue)** — where a message goes after failing too many times, so a human can
look at it instead of it being lost or retried forever.

**Delayed message** — a message scheduled to arrive later. How hold expiry and saga timeouts work
without polling the database in a loop.

**Projection / read model** — a table built by consuming events, shaped for reading rather than
writing (e.g. a statement view). Rebuildable by replay.

**Schema evolution** — changing an event's shape over time without breaking consumers. Adding a
field is safe; renaming or removing one is not. Why topics carry a `.v1` suffix.

---

## Operations

**Testcontainers** — a library that starts real Postgres/Kafka/RabbitMQ in Docker for the duration
of a test, so integration tests run against the real thing instead of a mock.

**OpenTelemetry** — the standard for emitting traces, metrics and logs.

**Trace / span / trace context propagation** — a trace is the full story of one request across
every service; a span is one step in it. Propagation means passing the trace id along — including
through message headers, so one transfer is one trace end to end.

**p50 / p99** — the typical request's time, and the slowest 1%'s time. p99 matters because that's
the user who complains.

**PSP (Payment Service Provider)** — the outside company that actually moves money to and from real
bank accounts. Mocked here, but mocked *badly on purpose*: it will time out and return "unknown".
