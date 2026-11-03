# Boot it again

Your machine rebooted and never came back: `/boot/grub/grub.cfg` is gone, so GRUB stops at a `grub>` or
`grub rescue>` prompt. The instance id is in `terraform output instance_id` (or in the lab's outputs).

1. Open the serial console: run `lab-console <instance id>` in the workspace, or use the AWS console button on the lab
   page and pick EC2 serial console. `aws ec2 get-console-screenshot` shows what the machine last drew.
2. At the prompt, boot by hand:

   ```
   set root=(hd0,gpt1)
   linux /boot/vmlinuz root=/dev/nvme0n1p1
   initrd /boot/initrd.img
   boot
   ```

   If `ls` shows other partitions, find the one with `/boot` first.
3. Once you are logged in, make it stick: `sudo update-grub`.

The other way in: stop the instance, attach its root volume to a second instance, restore `grub.cfg` there, and put
the volume back. Press **Check** when the machine boots on its own and `/boot/grub/grub.cfg` exists.
