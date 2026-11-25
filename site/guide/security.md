# Security and the recovery key

The **Security** page shows if the disk is encrypted, and manages the keys that open it. Only an administrator can see and change the keys of the disk.

Open the page: press [[Super+Escape]] and choose **Recovery key**. This entry is in the menu only on an encrypted disk. You can also choose **System**, then **Security**.

## Is the disk encrypted?

The page says **The disk is encrypted** or **The disk is not encrypted**.

You choose encryption when you install Isotope. To encrypt a disk that is not encrypted, back up the computer, install Isotope again with encryption, and restore the backup. See [Encrypt the disk](install.md#encrypt-the-disk) and [Backup and restore](backup.md).

## What a recovery key is

A recovery key is a second way to open an encrypted disk. It is 8 groups of 8 letters. Use it when:

- You forgot the disk passphrase.
- On the Trusted Boot edition, the TPM chip does not open the disk, for example after a firmware update.

At the start of the computer, type the recovery key where Isotope asks for the passphrase.

## Make a new recovery key

Make a new key if the disk has no recovery key, or if you lost the key or someone else saw it.

1. Under **Recovery key**, click **New recovery key…**.
2. Type the disk passphrase or the current recovery key. On the Trusted Boot edition, the TPM opens the disk and Isotope does not ask.
3. Isotope shows the new key one time only. Write it down and keep it away from the computer.
4. Click **I saved it**, then type the group of letters that Isotope asks for.

The new key replaces the old key. The old key then stops working.

> If Isotope says that it changed only a part of the disk, keep the old key and the new key. Some parts of the disk can already use the new key.

In a terminal, use `isotope recovery new-key`.

## Seal the disk to the TPM again (Trusted Boot)

On the Trusted Boot edition, the TPM chip opens the disk when the computer starts. A firmware update can change what the TPM checks. Then the computer asks for the recovery key at the start.

After such a start:

1. Open the **Security** page.
2. Click **Seal again**.
3. Type the recovery key.

The next start opens the disk without the recovery key.

In a terminal, use `isotope recovery reseal`.
