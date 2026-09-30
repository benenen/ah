# Real OpenSSH target for `make e2e`. Nothing here is a real credential: the
# test injects a throwaway public key at run time, and the ops password only
# exists inside disposable containers.
FROM alpine:3.20
RUN apk add --no-cache openssh-server openssh-sftp-server sudo busybox-extras bash \
 && adduser -D -s /bin/sh tester && echo 'tester:unused-login-password' | chpasswd \
 && adduser -D -s /bin/sh ops && echo 'ops:ops-e2e-pass' | chpasswd \
 && echo 'tester ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/tester \
 && echo 'ops ALL=(ALL) ALL' > /etc/sudoers.d/ops \
 && chmod 0440 /etc/sudoers.d/*
COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /entrypoint.sh
EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
