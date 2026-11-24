# Users

The **Users** page adds and removes the people who use the computer. It also changes passwords and who is an administrator, and adds your fingerprints.

Open the page: press [[Super+Escape]], choose **System**, then choose **Users**.

Only an administrator can add, remove or change other users. Every user can change their own password.

> On COSMIC, change users here, not in COSMIC Settings. Isotope keeps the users that you change on this page after a restart.

## Add a user

1. Click **Add user…**.
2. Type the **Name** to log in with. Use lowercase letters, digits, `-` and `_`, and start with a letter or `_`.
3. Optional: type the **Full name**.
4. Type the **Password** two times.
5. Optional: select **Administrator** to let the user change the system and the other users.
6. Click **Add**.

In a terminal, use `isotope users add`. Type `isotope users help` for all the commands.

## Change a password

1. Click **Change password** next to the user.
2. To change your own password, type your **Current password** first.
3. Type the **New password** two times.
4. Click **Change password**.

## Make a user an administrator

Click **Make administrator** next to the user. To take it back, click **Remove administrator**, then **Remove from administrators**.

An administrator can install updates, change the system, and add and remove users. Keep at least one administrator.

## Remove a user

1. Click **Remove…** next to the user.
2. To keep the files of the user on the disk, select **Keep the home folder**.
3. Click **Remove** and the name of the user.

The user can no longer log in.

## Sign in with a fingerprint

With a fingerprint reader, your finger can unlock the screen. It can also approve `sudo` and administrator prompts. Logging in still asks for your password, because your password also unlocks your keyring.

The **FINGERPRINTS** section is at the bottom of the **Users** page. It shows only your own fingerprints.

1. If the section says **Fingerprint sign-in is not installed**, click **Install the fingerprint extension**. Only an administrator can install it. See [Extensions](extensions.md).
2. Click **Add a fingerprint…**.
3. Select a finger, then click **Next**.
4. Type your password in **Confirm it is you**, then click **OK**.
5. Touch the reader again and again until the scans are done.

To remove a finger, click **Remove…** next to it, then **Remove**.

On sway and i3, type any key and press [[Enter]] on the lock screen, then touch the reader. Your password still unlocks the screen at once. On COSMIC, touch the reader first. The password field comes after 10 seconds.
