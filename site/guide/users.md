# Users

The **Users** page adds and removes the people who use the computer. It also changes passwords and who is an administrator.

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
