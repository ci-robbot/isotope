package main

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/guikit"
)

func init() {
	pageMakers[PageUsers] = func(u *UI) pageView {
		return &laterPage{u: u, page: PageUsers,
			sub: "Add and remove the people who use this computer, change their passwords and who is an administrator.",
			body: "This page is coming. On Isotope the users live in the system's configuration (/oem), which is read again at every boot, " +
				"so a user added with useradd alone is gone after a restart. Isotope System will change them through the update service, " +
				"which keeps that configuration and the running system in step. Until then, users are set up by the installer."}
	}
	pageMakers[PageDisks] = func(u *UI) pageView {
		return &laterPage{u: u, page: PageDisks,
			sub:  "The disks of this computer and the ones plugged in: space used, mount, eject and format USB disks.",
			body: "This page is coming. USB disks already mount from the file manager (udisks2); the system disk is never offered for changes."}
	}
	pageMakers[PageAbout] = func(u *UI) pageView { return &aboutPage{u: u} }
}

// laterPage is a page listed in the sidebar that is not there yet: it says
// what it will do and what to use until then.
type laterPage struct {
	u         *UI
	page      Page
	sub, body string
	list      widget.List
}

func (p *laterPage) Shown()                                      {}
func (p *laterPage) Update(layout.Context)                       {}
func (p *laterPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }
func (p *laterPage) Body(gtx layout.Context) layout.Dimensions {
	return p.u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(p.u.Heading(p.page.Title(), p.sub))
		c.gap(24)
		c.add(p.u.para(p.body))
		return c.layout(gtx)
	})
}

// aboutPage: the release, the base, the desktop and the machine.
type aboutPage struct {
	u     *UI
	list  widget.List
	notes widget.Clickable
}

func (p *aboutPage) Shown() {}

func (p *aboutPage) notesURL() string {
	rel := p.u.sys.Release
	if rel.GitHubRepo == "" || rel.Version == "" {
		return ""
	}
	return "https://github.com/" + rel.GitHubRepo + "/releases/tag/" + rel.Version
}

func (p *aboutPage) Update(gtx layout.Context) {
	if p.notes.Clicked(gtx) {
		if url := p.notesURL(); url != "" {
			if err := p.u.hooks.Start("xdg-open", url); err != nil {
				p.u.tell("Cannot open the release notes", err.Error())
			}
		}
	}
}

func (p *aboutPage) Footer() (leading, trailing []guikit.Action) {
	if p.notesURL() == "" {
		return nil, nil
	}
	return nil, []guikit.Action{{Click: &p.notes, Kind: guikit.ButtonSecondary, Label: "What's new in this release"}}
}

func (p *aboutPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	rel := u.sys.Release
	edition := "Desktop"
	switch {
	case rel.TrustedBoot:
		edition = "Trusted Boot"
	case rel.Flavor == "server":
		edition = "Server"
	}
	rows := [][2]string{
		{"Isotope", orUnknown(rel.Version)},
		{"Built on Hadron", orUnknown(rel.HadronVersion)},
		{"Desktop", flavorName(rel.Flavor)},
		{"Image", orUnknown(lineLabels[rel.Line()])},
		{"Edition", edition},
		{"Kernel", orUnknown(u.sys.Kernel)},
		{"Computer name", orUnknown(u.sys.Hostname)},
	}
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Mark(gtx, 56, true, 1, 0) }),
				layout.Rigid(layout.Spacer{Width: 18}.Layout),
				layout.Rigid(u.Heading("Isotope", "Isotope, built on Hadron. An immutable desktop from the Kairos project.")),
			)
		})
		c.gap(28)
		c.add(u.panel(func(gtx layout.Context) layout.Dimensions {
			var rc col
			for i, r := range rows {
				if i > 0 {
					rc.gap(8)
				}
				rc.add(u.row(r[0], "", func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody, guikit.ColText2, r[1])
				}))
			}
			return rc.layout(gtx)
		}))
		c.gap(16)
		c.add(u.dim("Isotope is free software under the Apache License 2.0."))
		return c.layout(gtx)
	})
}

// focus gives the keyboard focus to t.
func focus(t event.Tag) key.FocusCmd { return key.FocusCmd{Tag: t} }
