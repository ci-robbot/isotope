// Runs the real polkit rule files against a table of cases.
//
//   node rules-harness.js 50-isotope-udisks.rules 50-isotope-update.rules 50-isotope-fwupd.rules
//
// Each file is loaded with its own rule list, as polkitd loads it, and each
// case asks the rules of the file it names, in order: the first answer that
// is not undefined wins, and no answer is "fallthrough" (polkitd then asks
// the next file, and at the end the action's own defaults). Plain node, no
// packages.
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const Result = { YES: "yes", NO: "no", AUTH_ADMIN: "auth_admin", AUTH_ADMIN_KEEP: "auth_admin_keep" };

// rules is the list addRule fills: load points it at each file's own list.
// The rules call polkit when they run, so it stays defined.
let rules = [];
globalThis.polkit = { Result, addRule(f) { rules.push(f); }, log() {} };

function load(file) {
    rules = [];
    vm.runInThisContext(fs.readFileSync(file, "utf8"), { filename: file });
    return rules;
}

function subject(c) {
    return {
        user: c.user || "anna",
        local: c.local,
        active: c.active,
        groups: c.groups,
        isInGroup(g) { return c.groups.indexOf(g) >= 0; },
        isInNetGroup() { return false; },
    };
}

function answer(list, c) {
    const action = { id: c.action, lookup() { return undefined; } };
    for (const r of list) {
        const got = r(action, subject(c));
        if (got !== undefined) {
            return got;
        }
    }
    return "fallthrough";
}

const ud = "org.freedesktop.udisks2.";
const up = "io.kairos.isotope-update.";
const fw = "org.freedesktop.fwupd.";
const admin = ["users", "admin"];
const user = ["users"];
const here = { local: true, active: true };
const cases = [
    { file: "udisks", action: ud + "modify-device", groups: admin, ...here, want: "yes", what: "admin, local, active" },
    { file: "udisks", action: ud + "modify-device", groups: admin, local: false, active: true, want: "no", what: "admin, remote" },
    { file: "udisks", action: ud + "modify-device", groups: admin, local: true, active: false, want: "no", what: "admin, inactive" },
    { file: "udisks", action: ud + "modify-device", groups: user, ...here, want: "no", what: "non-admin, local, active" },
    { file: "udisks", action: ud + "modify-device-system", groups: admin, ...here, want: "fallthrough", what: "admin, local, active" },
    { file: "udisks", action: ud + "modify-device-other-seat", groups: admin, ...here, want: "fallthrough", what: "admin, local, active" },
    { file: "udisks", action: ud + "filesystem-mount", groups: user, ...here, want: "yes", what: "non-admin, local, active" },
    { file: "udisks", action: ud + "filesystem-mount", groups: admin, ...here, want: "yes", what: "admin, local, active" },
    { file: "udisks", action: ud + "filesystem-mount-system", groups: admin, ...here, want: "fallthrough", what: "admin, local, active" },
    { file: "udisks", action: ud + "filesystem-unmount-others", groups: admin, ...here, want: "fallthrough", what: "admin, local, active" },
    { file: "update", action: up + "users", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "update", action: up + "users", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "update", action: up + "restore", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "update", action: up + "restore", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "update", action: up + "firewall", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "update", action: up + "firewall", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "update", action: up + "firewall", groups: admin, local: false, active: false, want: "yes", what: "admin over SSH" },
    { file: "update", action: up + "shares", groups: user, ...here, want: "yes", what: "non-admin, local, active" },
    { file: "update", action: up + "shares", groups: admin, ...here, want: "yes", what: "admin, local, active" },
    { file: "update", action: up + "shares", groups: user, local: true, active: false, want: "no", what: "non-admin, inactive" },
    { file: "update", action: up + "shares", groups: admin, local: true, active: false, want: "no", what: "admin, inactive" },
    { file: "update", action: up + "shares", groups: user, local: false, active: true, want: "no", what: "non-admin, remote" },
    { file: "update", action: up + "shares", groups: admin, local: false, active: false, want: "no", what: "admin over SSH" },
    { file: "update", action: up + "shares-nfs", groups: admin, ...here, want: "yes", what: "admin, local, active" },
    { file: "update", action: up + "shares-nfs", groups: user, ...here, want: "no", what: "non-admin, local, active" },
    { file: "update", action: up + "shares-nfs", groups: admin, local: true, active: false, want: "no", what: "admin, inactive" },
    { file: "update", action: up + "shares-nfs", groups: admin, local: false, active: false, want: "no", what: "admin over SSH" },
    { file: "update", action: up + "printers", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "update", action: up + "printers", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "update", action: up + "printers", groups: user, local: false, active: false, want: "no", what: "non-admin over SSH" },
    // fwupd's own defaults let any active local user install a signed
    // update with no password; on Isotope only admin may, and only a plain
    // update goes without a password.
    { file: "fwupd", action: fw + "update-internal-trusted", groups: user, ...here, want: "no", what: "non-admin, local, active" },
    { file: "fwupd", action: fw + "update-internal-trusted", groups: admin, ...here, want: "yes", what: "admin, local, active" },
    { file: "fwupd", action: fw + "update-hotplug-trusted", groups: admin, local: false, active: false, want: "yes", what: "admin over SSH" },
    { file: "fwupd", action: fw + "update-hotplug-trusted", groups: user, ...here, want: "no", what: "non-admin, local, active" },
    { file: "fwupd", action: fw + "device-activate", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "refresh-remote", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "get-remotes", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "verify", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "verify-update", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "clear-results", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "get-bios-settings", groups: admin, ...here, want: "yes", what: "admin" },
    { file: "fwupd", action: fw + "quit", groups: admin, ...here, want: "yes", what: "admin" },
    // Anything that is not a plain update asks for the password.
    { file: "fwupd", action: fw + "update-internal", groups: admin, ...here, want: "auth_admin_keep", what: "admin, unsigned firmware" },
    { file: "fwupd", action: fw + "update-hotplug", groups: admin, ...here, want: "auth_admin_keep", what: "admin, unsigned firmware" },
    { file: "fwupd", action: fw + "downgrade-internal-trusted", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "downgrade-internal", groups: admin, local: false, active: false, want: "auth_admin_keep", what: "admin over SSH" },
    { file: "fwupd", action: fw + "downgrade-hotplug-trusted", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "downgrade-hotplug", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "set-bios-settings", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "modify-config", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "reset-config", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "enable-remote", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "modify-remote", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "clean-remote", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "set-approved-firmware", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "self-sign", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "fix-host-security-attr", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "undo-host-security-attr", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "emulation-load", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "emulation-save", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "emulation-tag", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "device-emulate", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "device-unlock", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "modify-device", groups: admin, ...here, want: "auth_admin_keep", what: "admin" },
    { file: "fwupd", action: fw + "some-future-action", groups: admin, ...here, want: "auth_admin_keep", what: "admin, an action a newer fwupd adds" },
    // Nobody else, whatever the action.
    { file: "fwupd", action: fw + "downgrade-internal", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "fwupd", action: fw + "set-bios-settings", groups: user, ...here, want: "no", what: "non-admin" },
    { file: "fwupd", action: fw + "refresh-remote", groups: user, ...here, want: "no", what: "non-admin" },
    // The refresh service lists and refreshes the remotes, nothing more.
    { file: "fwupd", action: fw + "refresh-remote", groups: [], user: "fwupd-refresh", local: false, active: false, want: "yes", what: "the refresh service" },
    { file: "fwupd", action: fw + "get-remotes", groups: [], user: "fwupd-refresh", local: false, active: false, want: "yes", what: "the refresh service" },
    { file: "fwupd", action: fw + "update-internal-trusted", groups: [], user: "fwupd-refresh", local: false, active: false, want: "no", what: "the refresh service installing" },
    { file: "fwupd", action: fw + "modify-remote", groups: [], user: "fwupd-refresh", local: false, active: false, want: "no", what: "the refresh service changing a remote" },
    { file: "fwupd", action: up + "users", groups: user, ...here, want: "fallthrough", what: "another service's action" },
];

const files = process.argv.slice(2);
if (files.length === 0) {
    console.error("usage: node rules-harness.js RULES_FILE...");
    process.exit(2);
}
const rulesOf = {};
for (const f of files) {
    const name = path.basename(f);
    const key = Object.keys({ udisks: 1, update: 1, fwupd: 1 }).find((k) => name.indexOf(k) >= 0);
    if (!key) {
        console.error("FAIL: no cases for " + f);
        process.exit(1);
    }
    rulesOf[key] = load(f);
}

let failed = 0;
for (const c of cases) {
    const list = rulesOf[c.file];
    if (!list) {
        console.log("FAIL: " + c.file + " rules not given");
        failed++;
        continue;
    }
    const got = answer(list, c);
    const line = c.file + ": " + c.action + " (" + c.what + ") is " + got;
    if (got === c.want) {
        console.log("ok: " + line);
    } else {
        console.log("FAIL: " + line + ", want " + c.want);
        failed++;
    }
}
process.exit(failed ? 1 : 0);
