# Pause a procedure until something outside the fleet says to go on.
#
# The step waits for an event whose tag matches, counting only events that
# arrive AFTER it starts waiting, and fails if none comes within `timeout`
# seconds. Whoever approves -- a person, a chat bot, a ticketing system
# through the API's webhook -- sends the event:
#
#     halite-hub runner event.send tag=halite/audit/approved data='{"by":"ed"}'
request_approval:
  salt.runner:
    - name: event.send
    - kwarg:
        tag: halite/audit/approval_requested
        data:
          change: "widen the connection pool"

wait_for_approval:
  salt.wait_for_event:
    - name: halite/audit/approved
    - timeout: 120
    - require:
      - salt: request_approval

proceed:
  salt.function:
    - name: test.echo
    - tgt: 'web*'
    - arg:
      - approved, carrying on
    - require:
      - salt: wait_for_approval
