# Backup and restore

A backup keeps what survives a restart: the home folders, the system settings, the users, and the list of your apps and extensions. The backup is encrypted with its own password. After the first backup, Isotope copies only the changes.

A backup does not keep the apps themselves. A restore installs them again from Flathub.

Only an administrator can back up and restore the computer.

Open the page: press [[Super+Escape]] and choose **Backup**.

## Set up the backup

You need a place for the backup. Isotope can use these:

- **A USB disk**. A disk with exFAT, FAT, NTFS, ext4, Btrfs or XFS can hold backups.
- **A remote server**: a REST server, Amazon S3 (or a service that works like it), or SFTP, a server that you reach with SSH.

To set up the backup:

1. On the **Backup** page, under **Back up this machine**, choose **A USB disk** or **A remote server**.
2. For a USB disk, plug it in and choose it. For a server, type its address and the login.
3. Type the folder for the backup. Isotope makes it if it is not there. Then click **Continue**.
4. Choose **Create a new backup**. If the folder already has a backup of this computer, choose **Use an existing backup** and type its password.
5. Isotope shows the password of the new backup. Write it down and keep it away from the computer. Click **I saved it**, then type the group of letters that Isotope asks for.
6. Click **Back up now** for the first backup.

> Isotope shows the password of the backup one time only. Without it, nobody can read the backup, not even you.

For an SFTP server, Isotope shows the key of the server. Make sure that it is the key of your server before you connect.

Optional: select **Include Docker and Podman storage** to also back up images, containers and volumes. They can be large.

## Back up now

Click **Back up now**. You can use the computer while the backup runs.

## Back up automatically

Under **Automatic backups**, choose one of these:

- **Daily**: once a day.
- **Weekly**: once a week.
- **Only when you click Back up now**.

If the USB disk is not plugged in, Isotope skips the backup and tries again at the next time.

In a terminal, use `isotope backup schedule daily` or `isotope backup schedule weekly`.

## Copy a backup to one file

1. Plug in a USB disk.
2. Click **Export to a file…**.
3. Choose the disk and type a file name. Then click **Export**.

The file holds the whole backup, encrypted with its password. You can restore it on any computer with `isotope restore`.

## Restore on a new computer

You can restore a whole computer only on a new installation. The new installation must use the same disk setup as the old one: the same encryption choice and the same edition.

1. Install Isotope. See [Install Isotope](install.md).
2. Log in. In Isotope Welcome, choose **Restore from a backup**. You can also use the **Restore** section of the **Backup** page.
3. Choose where the backup is: plug in the USB disk, or type the server.
4. Type the password of the backup.
5. Choose which backup to restore.
6. Click **Restore and restart**.

> A restore replaces what is on the computer now. The computer restarts at the end. After the restart, Isotope installs your apps and extensions again.

## Restore on a computer that you use

On a computer that is not new, restore your files from a terminal:

```
isotope restore --only homes,apps,extensions
```

Type `isotope restore help` for all the choices.
