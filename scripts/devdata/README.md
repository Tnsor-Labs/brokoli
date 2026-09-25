# Local test data

A Postgres with 100,000 synthetic rows, for driving a real pipeline by
hand: extract from a database, write to a file, and check what came out.

## Start it

```bash
docker compose -f scripts/devdata/docker-compose.yml up -d
```

The health check waits for the seed, so the container only reports
healthy once all 100,000 rows are in. Watch it with:

```bash
docker inspect --format '{{.State.Health.Status}}' brokoli-devdata
```

Port **55432**, not 5432, so it cannot collide with a Postgres already
running on the host.

| setting | value |
| --- | --- |
| host | `127.0.0.1` |
| port | `55432` |
| database | `brokoli_dev` |
| user / password | `brokoli` / `brokoli` |

The password is in the compose file on purpose. Nothing here is meant to
leave your machine.

## What is in it

`customers`, 100,000 rows, and `countries`, 8 rows, for joins.

The data is deliberately awkward. Every column is one a format conversion
can get wrong:

| what | rows | why it is there |
| --- | --- | --- |
| apostrophes in names | 14,285 | ends a SQL string literal if not doubled |
| embedded double quotes | 14,286 | breaks naive CSV quoting |
| newlines inside a value | 4,347 | breaks line-oriented parsers |
| non-ASCII (`Zoë`, `山田 太郎`) | many | breaks anything assuming one byte per character |
| NULL emails | 9,090 | not the same as an empty string |
| `external_id` past 2^53 | 100,000 | loses precision through a JSON double |

That last one has caught three real bugs in this codebase, and a fourth
while this fixture was being written: `CREATE TABLE` declared `INTEGER`
for a bigint column, so the generated script would not load (#547).

If an export is right on this table, it is probably right.

## Use it

Create a connection with the settings above, then a pipeline with a
`source_db` node:

```sql
select id, external_id, name, email, country, balance,
       score, is_active, signed_up_at, notes
from customers
order by id
```

and one `sink_file` node per format you want to check. For SQL output,
set the dialect and tick Create Table.

Point the sinks at `scripts/devdata/out/`. That directory is ignored, so
a 30MB JSON export cannot end up in a commit.

## Check the output

Line counting lies here, because 4,347 rows contain newlines. Parse
properly:

```bash
python3 - <<'PY'
import csv, json
csv.field_size_limit(10**7)
print("csv ", len(list(csv.DictReader(open("scripts/devdata/out/customers.csv", newline="")))))
print("json", len(json.load(open("scripts/devdata/out/customers.json"))))
PY
```

Both should say 100000.

The real test of a SQL export is whether it loads:

```bash
docker exec brokoli-devdata psql -U brokoli -d postgres -c "CREATE DATABASE roundtrip;"
docker cp scripts/devdata/out/customers.sql brokoli-devdata:/tmp/c.sql
docker exec brokoli-devdata psql -U brokoli -d roundtrip -v ON_ERROR_STOP=1 -f /tmp/c.sql
docker exec brokoli-devdata psql -U brokoli -d roundtrip -tAc "select count(*) from customers"
```

100000, and the first `external_id` still reading 9007199254740994, means
the export survived a full round trip.

## Stop it

```bash
docker compose -f scripts/devdata/docker-compose.yml down -v
```

`-v` drops the volume, so the next `up` reseeds from scratch.
