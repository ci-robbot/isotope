// Runs the real polkit rule files against a table of cases.
//
//   node rules-harness.js 50-isotope-udisks.rules 50-isotope-update.rules
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

const Result = { YES: "yes", NO: "no", AUTH_ADMIN: "auth_admin" };

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
        user: "anna",
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
];

const files = process.argv.slice(2);
if (files.length === 0) {
    console.error("usage: node rules-harness.js RULES_FILE...");
    process.exit(2);
}
const rulesOf = {};
for (const f of files) {
    const name = path.basename(f);
    const key = Object.keys({ udisks: 1, update: 1 }).find((k) => name.indexOf(k) >= 0);
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
