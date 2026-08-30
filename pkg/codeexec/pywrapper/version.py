# Single source of truth for the wrapper contract version. Go parses
# this file's bytes at init (pkg/codeexec/version.go), so the constant
# cannot drift between the two sides.
WRAPPER_VERSION = 2
