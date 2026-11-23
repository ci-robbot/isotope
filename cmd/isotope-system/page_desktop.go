package main

import (
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/isorelease"
)

func init() { pageMakers[PageDesktop] = func(u *UI) pageView { return &desktopPage{u: u} } }

// desktopPage: the running desktop and image line, and the switch to
// another desktop or line, as `isotope switch` does: the newest release of
// the chosen flavor and line, through the same install flow.
type desktopPage struct {
	u    *UI
	list widget.List

	flavors [4]widget.Clickable
	line    widget.Clickable
}

func (p *desktopPage) Shown() {}

func (p *desktopPage) Footer() (leading, trailing []guikit.Action) { return nil, nil }

// otherLine is the image line a switch offers, "" on Trusted Boot (one
// line, the signed UKI).
func otherLine(rel isorelease.Release) string {
	switch rel.Line() {
	case isorelease.LineUKI:
		return ""
	case isorelease.GPUVM:
		return isorelease.GPUFull
	}
	return isorelease.GPUVM
}

func (p *desktopPage) Update(gtx layout.Context) {
	u := p.u
	rel := u.sys.Release
	for i, f := range flavorNames {
		if p.flavors[i].Clicked(gtx) && f.ID != rel.Flavor {
			p.switchTo(f.ID, rel.Line(), "the "+f.Name+" desktop", switchWarning)
		}
	}
	if p.line.Clicked(gtx) {
		if l := otherLine(rel); l != "" {
			p.switchTo(rel.Flavor, l, "the "+lineLabels[l]+" image", lineWarning(l))
		}
	}
}

// switchTo installs the newest release of flavor on line.
func (p *desktopPage) switchTo(flavor, line, what, warning string) {
	u, d := p.u, p.u.hooks.Daemon
	if d == nil {
		u.tell("Cannot switch", "The update service (isotope-updated) is not available.")
		return
	}
	u.async(func() {
		tags, err := d.Releases()
		if err != nil {
			u.tell("Cannot switch", "Could not list the releases: "+Describe(err))
			return
		}
		tag := newestTag(tags)
		if tag == "" {
			u.tell("Cannot switch", "No release was found.")
			return
		}
		ref, err := releaseRef(u.sys.Release, flavor, line, tag)
		if err != nil {
			u.tell("Cannot switch", err.Error())
			return
		}
		u.startInstall(what+" ("+tag+")", ref, false, warning)
	})
}

func (p *desktopPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	rel := u.sys.Release
	st, ok, why := u.status()
	can := ok && installsAllowed(st) && u.sys.Admin
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(u.Heading("Desktop & image", "Isotope comes with four desktops, each in a hardware and a virtual machine image. Switching installs the newest release of the other one, like an update: your files and apps stay, the new desktop starts at the next boot."))
		c.gap(24)
		if u.sys.ReleaseErr != "" {
			c.add(u.errorBox("This system has no Isotope release file: " + u.sys.ReleaseErr))
			return c.layout(gtx)
		}
		if rel.Flavor == isorelease.FlavorServer || rel.Flavor == isorelease.FlavorAgent {
			c.add(u.para("This edition has no desktop to switch."))
			return c.layout(gtx)
		}
		c.add(u.lead("Running " + flavorName(rel.Flavor) + " · " + lineLabels[rel.Line()]))
		if !ok {
			c.gap(12)
			c.add(u.errorBox(orUnknown(why)))
		} else if !installsAllowed(st) {
			c.gap(12)
			c.add(u.notice("Switching waits until the current job ends or the pending restart is done: " + headline(st) + "."))
		}
		if !u.sys.Admin {
			c.gap(12)
			c.add(u.notice("Only an administrator (a member of the admin group) can switch the desktop."))
		}
		c.gap(24)
		c.add(u.caps("DESKTOP"))
		c.gap(10)
		c.add(func(gtx layout.Context) layout.Dimensions {
			var kids []layout.FlexChild
			for i, f := range flavorNames {
				if i > 0 {
					kids = append(kids, layout.Rigid(layout.Spacer{Width: 10}.Layout))
				}
				sub := "Switch…"
				if f.ID == rel.Flavor {
					sub = "Running"
				}
				kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if !can && f.ID != rel.Flavor {
						gtx = gtx.Disabled()
					}
					return u.ChoiceCard(gtx, &p.flavors[i], f.ID == rel.Flavor, f.Name, sub)
				}))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		})
		c.gap(10)
		c.add(u.dim(switchWarning))
		if l := otherLine(rel); l != "" {
			c.gap(28)
			c.add(u.caps("IMAGE"))
			c.gap(10)
			label := "Switch to the virtual machine image"
			if l == isorelease.GPUFull {
				label = "Switch to the hardware image"
			}
			c.add(u.row(label, lineWarning(l), u.buttons(guikit.Action{Click: &p.line, Kind: guikit.ButtonSecondary, Label: "Switch…", Disabled: !can})))
		} else {
			c.gap(28)
			c.add(u.dim("This system boots with Trusted Boot: it has one image, the signed UKI. Another desktop stays on it."))
		}
		return c.layout(gtx)
	})
}
