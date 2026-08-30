"""Probe module used by the task-bundle isolation tests.

This file exists in the wrapper's own directory and is importable under
the legacy execution path, which runs the user script with the wrapper
directory on sys.path. A task-bundle execution must NOT be able to
import it — the bundle mount scopes imports to the bundle root plus the
stdlib and excludes the wrapper directory — so its friendliness doubles
as a canary: if a bundle execution can import this, the scoping has
failed.
"""

VALUE = "escaped-host-probe"
