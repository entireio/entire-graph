CREATE TABLE ledger (
    id integer PRIMARY KEY,
    total integer NOT NULL
);

CREATE VIEW ledger_totals AS
    SELECT id, total FROM ledger;

CREATE FUNCTION ledger_add(amount integer) RETURNS integer AS $$
    SELECT total + amount FROM ledger LIMIT 1;
$$ LANGUAGE sql;

CREATE FUNCTION ledger_double(amount integer) RETURNS integer AS $$
    SELECT ledger_add(amount) * 2;
$$ LANGUAGE sql;
