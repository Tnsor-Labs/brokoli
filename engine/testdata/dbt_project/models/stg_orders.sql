-- A model with no dependencies beyond the seed.
select
    id,
    city,
    amount
from {{ ref('raw_orders') }}
