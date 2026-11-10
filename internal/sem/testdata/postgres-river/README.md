# River PostgreSQL migration regression corpus

These 16 SQL migrations come from River commit
`03b193e2a263b52ac523de36eafa45375c1b99b6`:

https://github.com/riverqueue/river/tree/03b193e2a263b52ac523de36eafa45375c1b99b6/riverdriver/riverdatabasesql/migration/main

Trailing whitespace is removed; SQL statements and line numbers are unchanged.
They are test inputs only, distributed under the upstream Mozilla Public License
2.0 (see LICENSE). Tests do not fetch or execute them against a database.

The corpus exercises identity-related ALTER TABLE statements, named CHECK
constraints, anonymous DO blocks with trailing LANGUAGE clauses, and partial
indexes with keyword-prefixed column names. Every migration must parse without
partial failures. Static declarations inside DO blocks retain their original
source positions; dynamic SQL strings and comments must not create symbols.
