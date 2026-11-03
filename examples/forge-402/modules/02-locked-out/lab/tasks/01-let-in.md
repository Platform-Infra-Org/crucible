# Let yourself back in

Someone "hardened" sshd on your machine: it listens on port 2222, takes no passwords, and EC2 Instance Connect no
longer works. You cannot reach it with `lab-ssh --direct <instance id>`.

1. Get a shell another way: `aws ssm start-session --target <instance id>`, or `lab-console <instance id>`.
2. Look at `/etc/ssh/sshd_config.d/99-crucible.conf` and `systemctl status ssh ssh.socket`.
3. Put sshd back on port 22 and restore the `AuthorizedKeysCommand` that Instance Connect uses. Test with
   `sudo sshd -t`, then restart the service.

Press **Check** when `lab-ssh --direct <instance id> true` works from the workspace.
