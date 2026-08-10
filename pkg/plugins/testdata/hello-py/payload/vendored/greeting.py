"""The vendored dependency: trivially small, but proves the payload tree
(and sys.path wiring) survives packaging, hashing, and installation."""


def greet(name):
    return f"hello, {name}"
