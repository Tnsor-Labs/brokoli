# Bash Operator

The Bash Operator runs a command on the worker and passes its input dataset
through unchanged. stdout and stderr are recorded in the node log. Configure:

```json
{
  "command": "./scripts/build.sh",
  "working_dir": "/data/project",
  "env": {"BUILD_ENV": "production"}
}
```

`working_dir` must be inside the worker's configured data directories. The
worker environment is filtered before the configured variables are added.
The shared node timeout, retry, cancellation, and execution controls apply.

This is a trusted-worker execution feature, not a sandbox. A Bash command can
read files, use installed programs, and make network requests available to the
worker. Restrict pipeline authoring permissions accordingly.
