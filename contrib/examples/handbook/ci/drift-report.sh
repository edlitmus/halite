#!/bin/sh
# drift-report.sh
#
# Read the JSON that a fleet-wide rehearsal prints on standard input and list
# every state that would change something, one per line:
#
#     node <TAB> state ID <TAB> what it would do
#
# Usage:
#     halite-hub run '*' state.apply --test --out json | drift-report.sh
#
# In test mode a state's result is null exactly when a real run would change
# it, so the report is empty on a converged fleet. A node that failed to
# compile or did not answer has no per-state entries and is not listed here:
# check halite-hub's exit status as well (1 is a failure, 3 is no answer).
exec jq -r '
    to_entries[]
    | .key as $node
    | .value
    | to_entries[]
    | select(.value.result == null)
    | [$node, .value.__id__, .value.comment]
    | @tsv'
