# Noria Monitor

The managed uptime-monitoring SaaS: accounts add monitors, regional probes check them, alerts go out when they fail, and each account can publish status pages. Monitor Host lives in `internal/host` in the same Go module. Reusable checks live in `internal/checks`; keep Cloud business logic in `internal/cloud` and Host collectors in `internal/host`.

## One file per area, not one file per behaviour

A new behaviour goes in the file that already covers its area. `handlers_monitors.go` is every monitor route, `internal/checks` every reusable probe, `internal/cloud/notify` every Cloud alert channel. Size is never a reason to split; start a new file only for a genuinely new area, named for the area.

## Comments: zero, always

This codebase carries **no comments**. Not sparse comments, not "only where
helpful" — none. This is a hard rule with no judgement call attached, and it
applies to every file you write or touch: Go, TypeScript, TSX, CSS, HTML, SQL
migrations, shell scripts, Dockerfiles, Compose files, CI workflows.

It applies to **new code as much as cleanup**. Do not write a comment intending
to remove it later. Do not add one because the logic is subtle, because you made
a non-obvious trade-off, or because you want the next reader to understand why.
Those are exactly the moments the rule exists for.

Tests are not an exception. Migrations are not an exception. Scripts are not an
exception.

### The only things that may look like comments

An exact allowlist. Everything outside it is a violation:

- Go directives: `//go:embed`, `//go:build`, `//line`, `//nolint`
- SQL: `-- migrate:up` and `-- migrate:down` (dbmate requires them)
- Shell: `#!` shebang on line 1
- TypeScript: `/// <reference ... />`
- Lint pragmas the build depends on: `oxlint-disable-*`, `eslint-disable-*`,
  `@ts-expect-error`, `@ts-ignore`, `prettier-ignore`
- Licence banners: `@license`, `@preserve`

`go.mod`'s `// indirect` markers are written by the toolchain; leave them alone.

### What is not a comment

Text inside a string or template literal is content, not a comment. The Go and
shell samples rendered in the API docs contain lines beginning with `//` and `#`
inside template literals. They stay. When checking for comments, never use a
plain regex: it will flag `https://` inside a string and miss nothing useful.
Use the language's own parser or scanner.

### Where the explanation goes instead

The reasoning is not discarded, it is relocated:

- **Why a change was made, and what was rejected** → the commit message. Write it
  properly; it is the permanent record.
- **How to operate something** → `README.md`.
- **What the code does** → the names. Rename the function, split it, or
  introduce a named intermediate rather than annotating it.
- **What a subtle behaviour guarantees** → a test whose name asserts it. A test
  called `TestIntegrationCustomerRoutesAreTenantIsolated` documents the
  guarantee and enforces it; a comment only claims it.

If you catch yourself wanting to write a comment, that is a signal the name is
wrong or the function is doing too much. Fix that instead.

### If you find comments in the tree

Remove them, in a separate commit from any behaviour change, using the
language's parser rather than a regex. If the removal touches a migration,
rebuild the schema on a clean database and diff indexes and columns against the
previous schema to prove nothing changed.

## Naming: every foreign key ends in `_id`

A column that references another row is named `<role>_id`. There is no `_by`,
no `_for`, no bare entity name. `granted_by` is wrong; `grantor_id` is right.

The `<role>` part is the singular name of the referenced table when the row
plays no special part in the relationship:

- `account_id`, `monitor_id`, `principal_id`, `incident_id`, `credential_id`

When the referenced row plays a *particular* part, `<role>` is the agentive
noun for that part, not the verb phrase describing what happened:

- `grantor_id`, `revoker_id`, `inviter_id`
- and by the same rule, an author would be `author_id`, a creator `creator_id`

This matters most where one table points at another twice. `access_grants`
references `principals` three times, and only the role names can tell them
apart: `principal_id` is the grant's subject, `grantor_id` gave it,
`revoker_id` took it away.

### The `_id` suffix is not only for foreign keys

Some columns end in `_id` without a foreign key, and that is correct. Do not
"fix" them:

- **Polymorphic references** — `access_grants.scope_id` and
  `audit_events.target_id` point at different tables depending on a sibling
  column, so no single constraint can express them.
- **Deliberately unlinked references** — `audit_events.account_id` carries no
  constraint because the audit trail must outlive the account it describes.
  Adding the foreign key would make erasure delete the record of erasure.
- **Identifiers from other systems** — `provider_transaction_id`,
  `request_id`. They are ids, they are named as ids, and they reference
  nothing in this database.

### Checking it

The rule is checkable in one query, against any Noria Monitor database. It should
return no rows:

```sql
SELECT c.conrelid::regclass || '.' || a.attname AS offending_column
FROM pg_constraint c
JOIN pg_namespace n ON n.oid = c.connamespace
JOIN unnest(c.conkey) AS k(attnum) ON true
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
WHERE n.nspname = 'public' AND c.contype = 'f' AND a.attname NOT LIKE '%\_id';
```

Run it after any migration that adds a reference. It is a check to run, not a
gate to install.

### Renaming an existing column

The database is the source of the name; Go struct fields, query text, and the
`columns` lists that the control plane exposes as API field names all follow
it. Rename in the migration first, then let the compiler and the tests find
the rest. Search with word boundaries, not substrings: `revoked_by` and
`revoked_at` differ by two characters.
