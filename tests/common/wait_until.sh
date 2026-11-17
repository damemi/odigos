# Usage: wait_until <check-function> <diagnostics-function>
# Calls the check every 2 seconds until it succeeds. When chainsaw kills the step on timeout,
# calls the diagnostics and fails.

wait_until() {
  local check="$1" diagnostics="$2"
  # On timeout chainsaw kills only the process it started: this script, or the shell that runs it.
  # The loop runs in a subshell that survives the kill, and chainsaw still logs what it prints.
  (
    while true; do
      if ! kill -0 $$ 2>/dev/null || ! kill -0 $PPID 2>/dev/null; then
        echo "[$(date +%T)] step timed out"
        "$diagnostics"
        exit 1
      fi
      if "$check"; then
        exit 0
      fi
      sleep 2
    done
  ) &
  wait $!
}
