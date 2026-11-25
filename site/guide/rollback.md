# Go back to the previous version

Isotope keeps the system that you had before the last update. If something does not work after an update, you can start that system again. Your files, your apps and your settings stay as they are.

## If the new system does not start

If the new system does not start, the computer starts the previous system again by itself. You do not have to do anything.

## Restart into the previous system

Only an administrator can do this.

1. Press [[Super+Escape]] and choose **Update**. The **Updates** page of Isotope System opens.
2. Under **Previous system**, click **Restart into the previous system**.
3. Read the message, then confirm.

The computer restarts into the previous system.

You can also use the menu of the update icon in the panel, or a terminal:

```
isotope rollback
```

> This is for the next start only. The start after that uses the new system again.

## Stay on the previous version

To keep the older version, install it:

1. Open the **Updates** page.
2. Under **Other releases**, find the version that you want.
3. Click **Install**, then restart.

Isotope then stays on that version. It does not offer newer releases until you install the latest release again.

When automatic updates are on, Isotope does not install the version that you left again. It installs the next newer release. See [Install automatic updates](updates.md#install-automatic-updates).

## From the boot menu

The boot menu also has the previous system. Its entry ends in **(fallback)**. Use it when the desktop does not start and you cannot open Isotope System.

## Tell us what went wrong

If an update broke something, [report the problem](help.md#report-a-problem). Write the version that works and the version that does not. The **About** page of Isotope System shows the version.
