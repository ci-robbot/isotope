package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
	"github.com/kairos-community/isotope/pkg/isorelease"
)

func init() {
	pageMakers[PageAbout] = func(u *UI) pageView { return &aboutPage{u: u} }
}

// userGuideURL is the user guide on the website (site/guide/), which the
// About page's "User guide" button opens. isotope-help names the same
// address; test/extensions/check-extensions-menu.sh holds the two equal.
const userGuideURL = "https://kairos-community.github.io/isotope/guide/"

// aboutPage: the release, the base, the desktop and the machine.
type aboutPage struct {
	u     *UI
	list  widget.List
	notes widget.Clickable
	guide widget.Clickable
	// graphics is the Graphics row, read when the page opens; "" hides
	// the row. Under u.mu.
	graphics string
}

func (p *aboutPage) Shown() {
	p.u.async(func() {
		g := graphicsLine(p.u.sys.Release.GPU == isorelease.GPUVM)
		p.u.mu.Lock()
		p.graphics = g
		p.u.mu.Unlock()
	})
}

// The software-rendering marker and the GPU probe; variables for the tests.
var (
	softwareMarker = func() string {
		return filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "isotope", "software-rendering")
	}
	gpuProbePath = "/usr/bin/isotope-gpu-probe"
)

// The Graphics row when the session draws in software, by the class the
// probe gave (the first word of the marker): a real GPU without a working
// driver, the same on the vm image line (which has no hardware drivers), a
// virtual machine's GPU without 3D, or only the firmware's framebuffer.
var (
	softwareGraphics         = i18n.Mark("Software (no driver for this GPU)")
	softwareGraphicsVMImage  = i18n.Mark("Software (this image has no GPU drivers)")
	softwareGraphicsVirtual  = i18n.Mark("Software (virtual GPU without 3D)")
	softwareGraphicsFirmware = i18n.Mark("Software (graphics driver off)")
)

// graphicsLine is what draws the screen: "Software (...)" when the session
// fell back to software rendering (the marker /usr/libexec/isotope-gpu-env
// writes, or ISOTOPE_SOFTWARE_RENDERING), else the renderer
// isotope-gpu-probe reads, "" when it cannot tell. vmImage: this is the vm
// image line.
func graphicsLine(vmImage bool) string {
	software := os.Getenv("ISOTOPE_SOFTWARE_RENDERING") == "1"
	class := ""
	if os.Getenv("XDG_RUNTIME_DIR") != "" {
		if b, err := os.ReadFile(softwareMarker()); err == nil {
			software = true
			class, _, _ = strings.Cut(strings.TrimSpace(string(b)), " ")
		}
	}
	if software {
		switch {
		case class == "virtual":
			return i18n.T(softwareGraphicsVirtual)
		case class == "firmware":
			return i18n.T(softwareGraphicsFirmware)
		case vmImage:
			return i18n.T(softwareGraphicsVMImage)
		}
		return i18n.T(softwareGraphics)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, gpuProbePath).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

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
				p.u.tell(i18n.T("Cannot open the release notes"), err.Error())
			}
		}
	}
	if p.guide.Clicked(gtx) {
		if err := p.u.hooks.Start("xdg-open", userGuideURL); err != nil {
			p.u.tell(i18n.T("Cannot open the user guide"), err.Error())
		}
	}
}

func (p *aboutPage) Footer() (leading, trailing []guikit.Action) {
	trailing = []guikit.Action{{Click: &p.guide, Kind: guikit.ButtonSecondary, Label: i18n.T("User guide")}}
	if p.notesURL() != "" {
		trailing = append(trailing, guikit.Action{Click: &p.notes, Kind: guikit.ButtonSecondary, Label: i18n.T("What's new in this release")})
	}
	return nil, trailing
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
		{i18n.T("Built on Hadron"), orUnknown(rel.HadronVersion)},
		{i18n.T("Desktop"), flavorName(rel.Flavor)},
		{i18n.T("Image"), orUnknown(lineLabel(rel.Line()))},
		{i18n.T("Edition"), edition},
		{i18n.T("Kernel"), orUnknown(u.sys.Kernel)},
		{i18n.T("Computer name"), orUnknown(u.sys.Hostname)},
	}
	u.mu.Lock()
	graphics := p.graphics
	u.mu.Unlock()
	if graphics != "" {
		rows = append(rows, [2]string{i18n.T("Graphics"), graphics})
	}
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		var c col
		c.add(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return guikit.Mark(gtx, 56, true, 1, 0) }),
				layout.Rigid(layout.Spacer{Width: 18}.Layout),
				layout.Rigid(u.Heading("Isotope", i18n.T("Isotope, built on Hadron. An immutable desktop from the Kairos project."))),
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
		c.add(u.dim(i18n.T("Isotope is free software under the Apache License 2.0.")))
		return c.layout(gtx)
	})
}

// focus gives the keyboard focus to t.
func focus(t event.Tag) key.FocusCmd { return key.FocusCmd{Tag: t} }
