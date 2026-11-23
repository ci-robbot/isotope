// Package shares is what the isotope command (cmd/isotope/: the root
// daemon that mounts a share, and the tray that mounts the saved ones at
// login) and Isotope System (cmd/isotope-system/: the Disks page) share
// about a network share: its address and its checks, so the window refuses
// what the daemon would refuse, and the user's list of saved shares.
//
// A share is an SMB share (smb://host[:port]/share[/path]) or an NFSv4
// export (nfs://host[:port]/path). No user, password, query or fragment is
// part of an address: the user and domain are saved beside it, and the
// password only in the user's keyring.
package shares

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The two protocols.
const (
	SMB = "smb"
	NFS = "nfs"
)

// maxURL bounds an address before any other check.
const maxURL = 1024

// Location is a parsed share address.
type Location struct {
	// Scheme is SMB or NFS.
	Scheme string
	// Host is a host name or an IP address, without brackets.
	Host string
	// Port is 0 for the protocol's own (445, 2049).
	Port uint16
	// Share is the SMB share name; "" for NFS.
	Share string
	// Path is the rest: for SMB a folder in the share ("/a/b", or ""),
	// for NFS the export ("/", "/srv/data").
	Path string
}

// Parse reads and checks a share address.
func Parse(raw string) (Location, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Location{}, errors.New("type an address such as smb://server/share or nfs://server/export")
	}
	if len(raw) > maxURL {
		return Location{}, fmt.Errorf("the address is longer than %d bytes", maxURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Location{}, errors.New("this is not an address such as smb://server/share or nfs://server/export")
	}
	var l Location
	l.Scheme = strings.ToLower(u.Scheme)
	switch l.Scheme {
	case SMB, NFS:
	case "":
		return Location{}, errors.New("the address must start with smb:// or nfs://")
	default:
		return Location{}, fmt.Errorf("%s:// is not supported: use smb:// or nfs://", u.Scheme)
	}
	if u.Opaque != "" {
		return Location{}, errors.New("the address must start with smb:// or nfs://")
	}
	if u.User != nil {
		return Location{}, errors.New("put the user name in the User field, not in the address")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return Location{}, errors.New("the address cannot have a ? or a #")
	}
	l.Host = u.Hostname()
	if err := checkHost(l.Host); err != nil {
		return Location{}, err
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Location{}, errors.New("the port must be a number from 1 to 65535")
		}
		l.Port = uint16(n)
	} else if strings.HasSuffix(u.Host, ":") {
		return Location{}, errors.New("the port must be a number from 1 to 65535")
	}
	segs, err := segments(u.Path)
	if err != nil {
		return Location{}, err
	}
	switch l.Scheme {
	case SMB:
		if len(segs) == 0 {
			return Location{}, errors.New("name the share: smb://server/share")
		}
		l.Share = segs[0]
		if len(segs) > 1 {
			l.Path = "/" + strings.Join(segs[1:], "/")
		}
	case NFS:
		l.Path = "/" + strings.Join(segs, "/")
	}
	return l, nil
}

// checkHost accepts an IP address or a host name of letters, digits, dots,
// hyphens and underscores (NetBIOS names have them) that starts with a
// letter or a digit.
func checkHost(h string) error {
	if h == "" {
		return errors.New("the address has no server name")
	}
	if net.ParseIP(h) != nil {
		return nil
	}
	if len(h) > 253 {
		return errors.New("the server name is too long")
	}
	for i, r := range h {
		ok := r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_')
		if !ok || (i == 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r)) {
			return fmt.Errorf("%q is not a server name", h)
		}
	}
	return nil
}

// segments splits a decoded path into its parts, without empty ones. A
// part may not be "." or "..", and no part may hold a comma (the mount
// options' separator), a backslash or a control character.
func segments(p string) ([]string, error) {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return nil, errors.New("the path cannot hold . or ..")
		}
		if !utf8.ValidString(s) {
			return nil, errors.New("the path is not valid text")
		}
		for _, r := range s {
			if r == ',' || r == '\\' || unicode.IsControl(r) {
				return nil, errors.New("the path cannot hold a comma, a backslash or a control character")
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// hostPort is the host for a URL: an IPv6 address in brackets, and the
// port when it is set.
func (l Location) hostPort() string {
	h := l.Host
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	if l.Port != 0 {
		h += ":" + strconv.Itoa(int(l.Port))
	}
	return h
}

// String is the address in its plain form: smb://host/share/path or
// nfs://host/path, with the port when it is set.
func (l Location) String() string {
	u := url.URL{Scheme: l.Scheme, Host: l.hostPort()}
	if l.Scheme == SMB {
		u.Path = "/" + l.Share + l.Path
	} else {
		u.Path = l.Path
	}
	return u.String()
}

// Source is the device argument of mount: //host/share/path for CIFS,
// host:/path for NFS ([v6]:/path).
func (l Location) Source() string {
	h := l.Host
	if l.Scheme == SMB {
		return "//" + h + "/" + l.Share + l.Path
	}
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h + ":" + l.Path
}

// FSType is the type mount is given: cifs or nfs4.
func (l Location) FSType() string {
	if l.Scheme == SMB {
		return "cifs"
	}
	return "nfs4"
}

// DefaultName is the folder name a share gets when none is given: the last
// part of its path, the SMB share name, or the server's name.
func (l Location) DefaultName() string {
	if l.Path != "" && l.Path != "/" {
		return path.Base(l.Path)
	}
	if l.Share != "" {
		return l.Share
	}
	return l.Host
}

// Same says whether a and b are the same share: the protocol, the server
// (case does not matter), the share and the path. The port does not count,
// since the mount table does not show it.
func (a Location) Same(b Location) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) &&
		a.Share == b.Share && strings.TrimSuffix(a.Path, "/") == strings.TrimSuffix(b.Path, "/")
}

// FromMount reads a share back from a line of the mount table: its type
// (cifs, smb3, nfs4, nfs) and its source. ok is false for any other mount.
func FromMount(fstype, source string) (Location, bool) {
	switch fstype {
	case "cifs", "smb3":
		rest, ok := strings.CutPrefix(source, "//")
		if !ok {
			return Location{}, false
		}
		host, p, _ := strings.Cut(rest, "/")
		share, sub, _ := strings.Cut(p, "/")
		if host == "" || share == "" {
			return Location{}, false
		}
		l := Location{Scheme: SMB, Host: strings.Trim(host, "[]"), Share: share}
		if sub = strings.Trim(sub, "/"); sub != "" {
			l.Path = "/" + sub
		}
		return l, true
	case "nfs4", "nfs":
		i := strings.LastIndex(source, ":/")
		if i <= 0 {
			return Location{}, false
		}
		return Location{Scheme: NFS, Host: strings.Trim(source[:i], "[]"), Path: source[i+1:]}, true
	}
	return Location{}, false
}

// MaxName is the longest folder name of a mounted share, in bytes.
const MaxName = 64

// CheckName checks the folder name a share is mounted under, in
// /run/media/<user>/: one path part, not hidden, no control character.
func CheckName(n string) error {
	switch {
	case n == "":
		return errors.New("the name is empty")
	case len(n) > MaxName:
		return fmt.Errorf("the name is longer than %d bytes", MaxName)
	case !utf8.ValidString(n):
		return errors.New("the name is not valid text")
	case strings.HasPrefix(n, ".") || strings.HasPrefix(n, "-"):
		return errors.New("the name cannot start with a dot or a hyphen")
	}
	for _, r := range n {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return errors.New("the name cannot hold a slash, a backslash or a control character")
		}
	}
	return nil
}

// MaxCredential is the longest user name or domain.
const MaxCredential = 256

// CheckCredential checks a user name or a domain: printable, no comma,
// equals sign or newline (mount.cifs reads them as key=value lines), and
// not longer than MaxCredential. Empty is allowed.
func CheckCredential(what, s string) error {
	if len(s) > MaxCredential {
		return fmt.Errorf("the %s is longer than %d bytes", what, MaxCredential)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("the %s is not valid text", what)
	}
	for _, r := range s {
		if r == ',' || r == '=' || unicode.IsControl(r) {
			return fmt.Errorf("the %s cannot hold a comma, an equals sign or a control character", what)
		}
	}
	return nil
}
