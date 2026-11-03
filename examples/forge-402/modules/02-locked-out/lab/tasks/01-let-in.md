# Let yourself back in

Someone "hardened" sshd on your machine: it listens on port 2222 and takes no passwords, and nothing answers on port 22
any more. It breaks itself about a minute after the lab starts. Find the instance id in the lab's outputs, or with
`aws ec2 describe-instances --filters Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID`.

1. Get a shell another way: `aws ssm start-session --target <instance id>`, or `lab-console <instance id>`.
2. Look at `/etc/ssh/sshd_config.d/99-crucible.conf` and `systemctl status ssh ssh.socket`.
3. Put sshd back on port 22. Test with `sudo sshd -t`, then restart the service.

Press **Check** when `lab-ssh --direct <instance id> ubuntu true` works from the workspace.
