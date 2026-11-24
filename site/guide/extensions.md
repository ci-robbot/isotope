# Extensions

The system of Isotope is a read-only image. A system extension adds programs to it: drivers, tools and services that are not in the image. Extensions run as root, so only an administrator can install them.

For desktop apps, use [Apps](apps.md) instead. Apps from Flathub do not change the system.

Open the page: press [[Super+Escape]] and choose **Extensions**.

## Install an extension

1. Under **AVAILABLE**, find the extension. Type a word in the search field to find it faster.
2. Click **Install**.
3. Wait until the page shows it under **INSTALLED**.

Isotope turns the extension on at once and at every start. If the extension has a service, Isotope starts it.

Isotope compares the files of the extension with the system before it turns it on. If the extension replaces a file of the system, the page asks **Replace system files?**. Click **Install anyway** only if you know why the extension does this: it can break the desktop.

## Turn an extension off, or remove it

Under **INSTALLED**:

- Click **Turn off** to stop the extension. It stays on the disk. Click **Turn on** to use it again.
- Click **Remove** to turn it off and delete it. You can install it again from the catalog.

## Use the terminal

| Command | What it does |
|---|---|
| `isotope ext` | List the installed extensions. |
| `isotope ext browse` | List the extensions that you can install. |
| `isotope ext info NAME` | Show the description, the versions and the files of an extension. |
| `isotope ext install NAME` | Install an extension. |
| `isotope ext disable NAME` | Turn an extension off. |
| `isotope ext remove NAME` | Remove an extension. |

## Where extensions come from

The page reads the catalogs in `/etc/isotope/extensions.conf`: first the catalog of Isotope, then the one of Hadron. The page needs a network to read them.

An example is `modemmanager`, which adds support for mobile broadband modems. See [Mobile broadband](network.md#mobile-broadband).
