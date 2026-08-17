# Brokoli Examples

These examples are designed to run against a local Brokoli server with no
external services or credentials.

## Hello World

Start the server and install the Python SDK:

```bash
brokoli serve
pip install brokoli
```

Deploy and run the pipeline:

```bash
brokoli deploy examples/hello-world.py --server http://localhost:8080
brokoli run hello-world --server http://localhost:8080
```

Then open `http://localhost:8080` and inspect the run. The pipeline reads the
built-in employee dataset, adds a greeting column, and writes
`/tmp/brokoli-hello-world.csv`.

The run detail shows the execution plan and node evidence. The dashboard and
Lineage view make the same run useful after the first demo: you can see what
was planned, what ran, and how the output relates to the input.
