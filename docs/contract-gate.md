# Contract Gate

`contract_gate` is Brokoli's native integration for actually-fine contracts.
It evaluates the incoming dataset with the canonical contract IR rather than
translating the contract into Brokoli's legacy `quality_check` rule model.

Example node configuration:

```json
{
  "id": "orders-contract",
  "type": "contract_gate",
  "name": "Orders contract",
  "config": {
    "contract": {
      "ir_version": "1.0",
      "contract": {"id": "orders", "version": "1"},
      "input": {"kind": "record-stream"},
      "rules": [
        {
          "id": "email-required",
          "kind": "record",
          "path": "$.email",
          "predicate": {"op": "required"},
          "on_breach": {"action": "quarantine"}
        }
      ]
    }
  }
}
```

The gate passes clear records downstream, removes quarantined records from its
output, and records each warning or breach in the node run log. `reject` and
`halt` actions fail the node after emitting their evidence. Unsupported IR or
contract rules fail before input records are evaluated.

This node consumes Brokoli's native `common.DataSet`. Arrow IPC remains an
optional transport boundary for high-throughput task and artifact paths; the
gate does not re-encode an already materialized dataset through Arrow.
