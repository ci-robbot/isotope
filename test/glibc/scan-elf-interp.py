#!/usr/bin/env python3
"""Read a tar stream (`docker export IMAGE`) on stdin and print a line
"PATH<TAB>INTERP" for every regular file that is an ELF. INTERP is its
PT_INTERP, empty for a static ELF or a library without one.

test/glibc/check-glibc.sh uses it to prove that nothing the image ships names
ld-linux-x86-64.so.2 as its interpreter: such a file would start through
Isotope's stand-in loader and be sent to the glibc container. It reads the
stream as it comes, so the image is never unpacked to disk, and a symlink is
never followed.
"""
import struct
import sys
import tarfile

PT_INTERP = 3
HEAD = 64 * 1024


def interp(f, size):
    """The PT_INTERP string of the ELF in f, "" for an ELF without one,
    None when f is not an ELF."""
    data = f.read(min(size, HEAD))
    if len(data) < 52 or data[:4] != b"\x7fELF":
        return None
    cls, enc = data[4], data[5]
    if cls not in (1, 2) or enc not in (1, 2):
        return None
    end = "<" if enc == 1 else ">"
    if cls == 2:
        if len(data) < 64:
            return None
        phoff, = struct.unpack_from(end + "Q", data, 32)
        phentsize, phnum = struct.unpack_from(end + "HH", data, 54)
    else:
        phoff, = struct.unpack_from(end + "I", data, 28)
        phentsize, phnum = struct.unpack_from(end + "HH", data, 42)

    def need(n):
        nonlocal data
        if n > len(data) and n <= size:
            data += f.read(n - len(data))
        return n <= len(data)

    for i in range(phnum):
        off = phoff + i * phentsize
        if not need(off + phentsize) or phentsize < (56 if cls == 2 else 32):
            return ""
        if struct.unpack_from(end + "I", data, off)[0] != PT_INTERP:
            continue
        if cls == 2:
            p_offset, = struct.unpack_from(end + "Q", data, off + 8)
            p_filesz, = struct.unpack_from(end + "Q", data, off + 32)
        else:
            p_offset, = struct.unpack_from(end + "I", data, off + 4)
            p_filesz, = struct.unpack_from(end + "I", data, off + 16)
        if p_filesz > 4096 or not need(p_offset + p_filesz):
            return "?"
        return data[p_offset:p_offset + p_filesz].split(b"\0", 1)[0].decode("utf-8", "replace")
    return ""


def main():
    with tarfile.open(fileobj=sys.stdin.buffer, mode="r|") as tar:
        for m in tar:
            if not m.isreg() or m.size < 52:
                continue
            f = tar.extractfile(m)
            if f is None:
                continue
            i = interp(f, m.size)
            if i is None:
                continue
            name = m.name[2:] if m.name.startswith("./") else m.name
            print("/" + name.lstrip("/") + "\t" + i)


if __name__ == "__main__":
    main()
