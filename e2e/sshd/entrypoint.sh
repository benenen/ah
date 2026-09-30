#!/bin/sh
# A fresh host key per container, so every run exercises first-contact trust.
set -eu
ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
install -d -m 700 -o tester -g tester /home/tester/.ssh
printf '%s\n' "$AUTHORIZED_KEY" > /home/tester/.ssh/authorized_keys
chown tester:tester /home/tester/.ssh/authorized_keys
chmod 600 /home/tester/.ssh/authorized_keys
exec /usr/sbin/sshd -D -e
