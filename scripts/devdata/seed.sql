-- Synthetic data for local pipeline testing.
--
-- Deliberately awkward. Every column type here is one that a format
-- conversion can get wrong, and several rows carry values chosen to
-- break a naive writer:
--
--   * apostrophes, which end a SQL string literal if not doubled
--   * commas and embedded quotes, which break unquoted CSV
--   * newlines inside a value, which break line-oriented parsers
--   * non-ASCII text, which breaks anything assuming one byte per char
--   * NULLs, which are not the same as an empty string
--   * a bigint past 2^53, which loses precision if it goes through a
--     JSON double on the way (this class of bug has been found three
--     times in this codebase)
--
-- If an export looks right on this table, it is probably right.

CREATE TABLE customers (
    id           bigserial PRIMARY KEY,
    external_id  bigint      NOT NULL,
    name         text        NOT NULL,
    email        text,
    country      text        NOT NULL,
    balance      numeric(14,2) NOT NULL,
    score        double precision,
    is_active    boolean     NOT NULL,
    signed_up_at timestamptz NOT NULL,
    notes        text
);

-- 100,000 rows. generate_series keeps this to one statement and a few
-- seconds rather than a loop.
INSERT INTO customers (external_id, name, email, country, balance, score, is_active, signed_up_at, notes)
SELECT
    -- Past 2^53, so a writer that round-trips through a float loses it.
    9007199254740993 + i,
    CASE (i % 7)
        WHEN 0 THEN 'O''Brien, Sinead'
        WHEN 1 THEN 'Zoë Müller'
        WHEN 2 THEN 'Ada Lovelace'
        WHEN 3 THEN '山田 太郎'
        WHEN 4 THEN 'Smith "Skip" Jones'
        WHEN 5 THEN 'Ana Sofia'
        ELSE 'Customer ' || i
    END,
    CASE WHEN i % 11 = 0 THEN NULL ELSE 'user' || i || '@example.test' END,
    (ARRAY['MZ','PT','BR','ZA','KE','US','DE','JP'])[1 + (i % 8)],
    ROUND(((i % 100000)::numeric / 100), 2) * CASE WHEN i % 13 = 0 THEN -1 ELSE 1 END,
    CASE WHEN i % 17 = 0 THEN NULL ELSE (i % 1000) / 7.0 END,
    (i % 3 <> 0),
    TIMESTAMPTZ '2024-01-01 00:00:00+00' + (i % 86400) * INTERVAL '1 second' + (i % 700) * INTERVAL '1 day',
    CASE
        WHEN i % 23 = 0 THEN E'multi\nline note, with a comma'
        WHEN i % 29 = 0 THEN 'tab\there'
        ELSE NULL
    END
FROM generate_series(1, 100000) AS s(i);

CREATE INDEX ON customers (country);
CREATE INDEX ON customers (signed_up_at);

-- A second, small table, so joins and multi-source pipelines have
-- something to work with.
CREATE TABLE countries (
    code text PRIMARY KEY,
    name text NOT NULL,
    region text NOT NULL
);

INSERT INTO countries (code, name, region) VALUES
    ('MZ', 'Mozambique',     'Africa'),
    ('PT', 'Portugal',       'Europe'),
    ('BR', 'Brazil',         'Americas'),
    ('ZA', 'South Africa',   'Africa'),
    ('KE', 'Kenya',          'Africa'),
    ('US', 'United States',  'Americas'),
    ('DE', 'Germany',        'Europe'),
    ('JP', 'Japan',          'Asia');

ANALYZE customers;
ANALYZE countries;
