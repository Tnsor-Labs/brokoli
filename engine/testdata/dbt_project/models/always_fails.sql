-- Selects from a relation that does not exist. Deliberately a raw table
-- name rather than a ref(): an unresolvable ref fails compilation for the
-- entire project, which would make every other model in this fixture
-- unrunnable. This compiles and fails at execution, which is the failure
-- the node has to report per-model.
select * from brokoli_no_such_table_exists
