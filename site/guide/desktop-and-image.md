# Desktop and image

You can change an installed computer to another desktop, or to another image. Your files and your apps stay. Only an administrator can do this.

Open the page: press [[Super+Escape]], choose **System**, then choose **Desktop & image**.

## Change to another desktop

Isotope has four desktops: COSMIC, niri, Sway and i3. See [Desktops, the Isotope menu and keys](desktops.md).

1. Under **DESKTOP**, find the desktop that you want.
2. Click **Switch…**, then confirm.
3. Wait until the installation is done.
4. Restart the computer.

The new desktop starts at the next restart. Isotope installs the newest release of that desktop, the same as an update. Later updates follow the new desktop.

In a terminal, use `isotope switch niri`. Use `cosmic`, `niri`, `sway` or `i3`.

If you do not like the new desktop, you can [go back to the previous version](rollback.md), or switch again.

## Change the image

Each desktop has two images:

| Image | For |
|---|---|
| Hardware | PCs and laptops. It has the graphics drivers for Intel, AMD and NVIDIA cards, and the firmware for Wi-Fi, Bluetooth and sound. |
| Virtual machine | Virtual machines. It has no hardware drivers and no firmware. |

If you installed the virtual machine image on a real computer, the screen is drawn in software and can be slow. A notification then tells you to change to the hardware image.

1. Under **IMAGE**, click **Switch to the hardware image** or **Switch to the virtual machine image**.
2. Confirm, then restart when the installation is done.

In a terminal, use `isotope switch --gpu full` for the hardware image, or `isotope switch --gpu vm`.

## The Trusted Boot edition

A computer with the Trusted Boot edition has one image. You can change its desktop, but not its image.
