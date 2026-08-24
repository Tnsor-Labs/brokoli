"""A first Brokoli pipeline that runs without external connections.

Start Brokoli, deploy this file, and run the ``hello-world`` pipeline from the
UI or with ``brokoli run hello-world``.
"""

from brokoli import Pipeline, sink_file, source_api, transform


with Pipeline("hello-world", description="Fetch employees and greet them") as pipeline:
    employees = source_api(
        "Fetch Employees",
        url="/api/samples/data/employees.json",
    )

    greeted = transform(
        "Add Greeting",
        input=employees,
        rules=[
            {
                "type": "add_column",
                "name": "greeting",
                "expression": "'Hello, ' + name",
            }
        ],
    )

    sink_file(
        "Save Result",
        input=greeted,
        path="/tmp/brokoli-hello-world.csv",
        format="csv",
    )
