-- A model that refs another model, so the fixture has a real DAG edge
-- rather than one isolated node.
select
    city,
    count(*) as n
from {{ ref('stg_orders') }}
group by city
