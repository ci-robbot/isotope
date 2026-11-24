/* A stand-in owner of org.freedesktop.ModemManager1 for the extension's test
 * stage: it takes the name on the system bus and sends a signal every 100 ms,
 * so the test can check who may receive ModemManager's signals (an SMS
 * arrives in one). Built in the build stage, used only in the test stage;
 * never part of the extension. */
#include <gio/gio.h>

static GDBusConnection *bus;

static gboolean ping(gpointer data)
{
    (void)data;
    g_dbus_connection_emit_signal(bus, NULL, "/org/freedesktop/ModemManager1",
                                  "org.freedesktop.ModemManager1.IsotopeTest", "Ping", NULL, NULL);
    g_dbus_connection_flush_sync(bus, NULL, NULL);
    return G_SOURCE_CONTINUE;
}

int main(void)
{
    GError *err = NULL;
    guint32 code = 0;
    GVariant *r;

    bus = g_bus_get_sync(G_BUS_TYPE_SYSTEM, NULL, &err);
    if (!bus) {
        g_printerr("mm-signal-stub: %s\n", err->message);
        return 1;
    }
    /* 4: DBUS_NAME_FLAG_DO_NOT_QUEUE; 1: the primary owner. */
    r = g_dbus_connection_call_sync(bus, "org.freedesktop.DBus", "/org/freedesktop/DBus",
                                    "org.freedesktop.DBus", "RequestName",
                                    g_variant_new("(su)", "org.freedesktop.ModemManager1", 4u),
                                    G_VARIANT_TYPE("(u)"), G_DBUS_CALL_FLAGS_NONE, -1, NULL, &err);
    if (!r) {
        g_printerr("mm-signal-stub: %s\n", err->message);
        return 1;
    }
    g_variant_get(r, "(u)", &code);
    g_variant_unref(r);
    if (code != 1) {
        g_printerr("mm-signal-stub: not the owner of the name (%u)\n", code);
        return 1;
    }
    g_timeout_add(100, ping, NULL);
    g_main_loop_run(g_main_loop_new(NULL, FALSE));
    return 0;
}
