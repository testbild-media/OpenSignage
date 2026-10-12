#!/bin/sh
# Run by opensignage-agent-rollback.service (as the service user) after the agent unit
# failed repeatedly: the PREVIOUS agent binary points `current` back at the release an
# update came from. The unit then resets and restarts the agent (privileged ExecStopPost).
set -u
CONF=/etc/opensignage/agent.env
rc=1
for b in /opt/opensignage-agent/previous/bin/opensignage-agent /opt/opensignage-agent/current/bin/opensignage-agent; do
	if [ -x "$b" ] && "$b" rollback -config "$CONF"; then
		rc=0
		break
	fi
done
[ "$rc" -eq 0 ] || echo "opensignage rollback: no usable agent binary" >&2
# Pause before the unit restarts the agent: when both releases are broken, the agent must
# not be restarted in a tight loop (start limit → rollback → restart → …).
sleep 20
exit "$rc"
