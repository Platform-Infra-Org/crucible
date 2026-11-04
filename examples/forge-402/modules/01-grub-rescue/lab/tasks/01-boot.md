# Boot it again

Your machine rebooted with a bad `root=` on its kernel line, so it never finishes booting: it stops in the initramfs
emergency shell. It breaks itself about a minute after the lab starts, and you need the install's serial console access
for this one. Find the instance id in the lab's outputs, or with
`aws ec2 describe-instances --filters Name=tag:crucible:lab-id,Values=$CRUCIBLE_LAB_ID`.

1. Open the serial console: run `lab-console <instance id>` in the workspace, or use the AWS console button on the lab
   page and pick EC2 serial console. `aws ec2 get-console-screenshot` shows what the machine last drew.
2. Reboot from there, or wait for the GRUB menu (10 seconds). Press `e` on the default entry and fix `root=`: use
   `blkid` from the emergency shell to find the right one, or `root=LABEL=cloudimg-rootfs`. Press Ctrl-X to boot.
3. Once it boots, log in with `aws ssm start-session --target <instance id>` and make it stick: `sudo update-grub`.

The other way in: stop the instance, attach its root volume to a second instance, fix `/boot/grub/grub.cfg` there, and
put the volume back. Press **Check** when the machine boots on its own.
