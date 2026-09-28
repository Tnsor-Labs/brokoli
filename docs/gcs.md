# Google Cloud Storage

GCS connections are used by file nodes to read and write objects:

```json
{
  "bucket": "my-bucket",
  "credentials": "{service-account JSON}",
  "max_download_bytes": 10737418240
}
```

Put the service-account JSON in the connection's encrypted `extra` secret.
The connector requires an explicit service-account key and does not use ambient
machine credentials for customer-owned buckets. `endpoint` is optional for a
private endpoint or a GCS-compatible emulator.

Downloads are bounded by `max_download_bytes` (10 GiB by default). Uploads are
committed when the object writer closes successfully. Connection testing lists
the configured bucket through the same client path used by file nodes.

Live GCS verification is opt-in because the project does not keep a shared
customer bucket in CI.
