#!/usr/bin/env bash

# Render the preflight result for the background govulncheck process.
# Any nonzero scanner status keeps the overall preflight non-green.
govulncheck_verdict() {
  local status=$1 log_file=$2
  if [ "$status" -eq 0 ]; then
    return 0
  fi
  if [ "$status" -eq 2 ]; then
    printf '\033[33mSKIP\033[0m govulncheck (scan could not run) — see %s\n' "$log_file"
  else
    printf '\033[31mFAIL\033[0m govulncheck — see %s\n' "$log_file"
  fi
  return 1
}
