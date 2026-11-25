# Disks

The **Disks** page shows the disks of the computer and the disks that you plug in, with the space that they use. It mounts, ejects and formats USB disks. It also connects to [network shares](network-shares.md).

Open the page: press [[Super+Escape]], choose **System**, then choose **Disks**.

The system disk of Isotope shows as **Isotope's system disk**. You cannot change it on this page.

## Use a USB disk

1. Plug in the disk.
2. Click **Mount** next to the disk.

Isotope mounts the disk at `/run/media/<your user name>/<the label of the disk>`. Open this folder in your file manager.

Isotope reads disks with exFAT, FAT, NTFS, ext4, Btrfs and XFS.

## Remove a USB disk

1. Close the files that are open on the disk.
2. Click **Eject** next to the disk. Isotope writes what is not written yet, then unmounts the disk.
3. Remove the disk.

**Power off** also stops the disk. Use it for a disk that spins.

> Do not remove a disk before you eject it. Files that are not written yet can be lost.

## Format a USB disk

Only an administrator can format a disk.

> Formatting erases everything on the disk. You cannot undo it.

1. Click **Format…** next to the disk. On a USB disk, click **⋯** next to the disk, then **Format…**.
2. Choose **exFAT** or **ext4**. exFAT works on Windows, macOS and Linux. ext4 works on Linux only.
3. Optional: type a **Label**. An exFAT label has at most 11 characters.
4. Click **Erase and format**.

## Disk health

The `smartctl` command reads the health of a disk, for example `sudo smartctl -a /dev/nvme0n1`.
