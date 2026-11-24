package main

import (
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/kairos-community/isotope/pkg/guikit"
	"github.com/kairos-community/isotope/pkg/i18n"
)

func init() { pageMakers[PReady] = func(u *UI) page { return &readyPage{basePage: basePage{u}} } }

// readyPage is the last page: what was set and installed, what failed, and
// a log out when the language changed. Finish writes the stamp.
type readyPage struct {
	basePage
	list          widget.List
	logout, retry widget.Clickable
	logoutErr     string
}

func (p *readyPage) Footer(n nav) (leading, trailing []guikit.Action) {
	if n.Back != nil {
		leading = append(leading, *n.Back)
	}
	fin := *n.Continue
	fin.Label = i18n.T("Finish")
	return leading, []guikit.Action{fin}
}

func (p *readyPage) Update(gtx layout.Context) {
	if p.logout.Clicked(gtx) {
		p.logOut()
	}
	if p.retry.Clicked(gtx) {
		// The Apps page has the list and its Try again.
		p.u.goBackTo(PApps)
	}
}

// logoutBlocked: apps are still installing. Ending the session would kill
// the installs, so Log out waits for them.
func (p *readyPage) logoutBlocked() bool { return p.u.installing() }

// logOut writes the stamp, then ends the session. The window closes only
// when the logout started; if it did not, it stays with the error. While
// apps install it does nothing (the button is disabled).
func (p *readyPage) logOut() {
	u := p.u
	if p.logoutBlocked() {
		return
	}
	p.logoutErr = ""
	u.stamp(u.endStamp())
	if u.hooks.Logout != nil {
		if err := u.hooks.Logout(); err != nil {
			p.logoutErr = i18n.Tf("Could not log out: %v. Log out from the power menu instead.", err)
			return
		}
	}
	u.close()
}

// summarised are the pages Ready reports on: the ones that set something.
func summarised(p PageID) bool {
	switch p {
	case PWelcome, PShortcuts, PReady:
		return false
	}
	return true
}

func (p *readyPage) Body(gtx layout.Context) layout.Dimensions {
	u := p.u
	o := u.flow.Outcome
	return u.scrolled(gtx, &p.list, func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{
			layout.Rigid(u.Heading(i18n.T("Isotope is ready"), i18n.T("You can open this again from the Isotope menu (Super+Esc → Welcome)."))),
			guikit.VGap(22),
		}
		if o.LanguageChanged != "" {
			kids = append(kids, layout.Rigid(p.languageBox), guikit.VGap(16))
		}
		kids = append(kids, layout.Rigid(p.summary))
		if u.installing() {
			kids = append(kids, guikit.VGap(16), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Execute(op.InvalidateCmd{At: u.now().Add(40 * time.Millisecond)})
				return u.installProgress(gtx)
			}))
		}
		if len(o.AppsFailed) > 0 {
			kids = append(kids, guikit.VGap(16),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Warning(gtx, i18n.Tf("Could not install %s.", list(o.AppsFailed)))
				}))
			if slices.Contains(u.flow.Pages(), PApps) {
				kids = append(kids, guikit.VGap(10), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Link(gtx, &p.retry, i18n.T("Try again"))
				}))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// languageBox says to log out for the new language, with the button.
func (p *readyPage) languageBox(gtx layout.Context) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return guikit.Panel(gtx, 12, layout.UniformInset(16), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Label(gtx, guikit.StCard, guikit.ColText, i18n.Tf("Log out to use %s", u.flow.Outcome.LanguageChanged))
							}),
							guikit.VGap(4),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return u.Label(gtx, guikit.StSmall, guikit.ColDim, i18n.T("The desktop and apps change language when you sign in again."))
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Width: 16}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if p.logoutBlocked() {
							return u.Button(gtx, &p.logout, guikit.ButtonSecondary, i18n.T("Installing apps…"), false)
						}
						return u.Button(gtx, &p.logout, guikit.ButtonSecondary, i18n.T("Log out"), true)
					}),
				)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if p.logoutErr == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.Warning(gtx, p.logoutErr)
			})
		}),
	)
}

// summary lists each page with what became of it.
func (p *readyPage) summary(gtx layout.Context) layout.Dimensions {
	u := p.u
	o := u.flow.Outcome
	var kids []layout.FlexChild
	for _, id := range u.flow.Pages() {
		if !summarised(id) {
			continue
		}
		state, done := i18n.T("Unchanged"), false
		switch {
		case slices.Contains(o.Applied, id):
			// TRANSLATORS: the state of a setting on the last page: it was set.
			state, done = i18n.X("state", "Set"), true
		case slices.Contains(o.Skipped, id):
			state = i18n.T("Skipped")
		}
		detail := ""
		if id == PApps && len(o.AppsInstalled) > 0 {
			// TRANSLATORS: %s is a list of app names ("Firefox, GIMP and VLC").
			detail = i18n.Tf("Installed %s", list(o.AppsInstalled))
		}
		if len(kids) > 0 {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				r := image.Rect(0, 0, gtx.Constraints.Max.X, guikit.Dp(gtx, 1))
				guikit.FillRect(gtx, r, guikit.Over(guikit.ColSidebar, guikit.ColText, 0.06))
				return layout.Dimensions{Size: r.Max}
			}))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 11, Bottom: 11, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return p.summaryRow(gtx, PageTitle(id), detail, state, done)
			})
		}))
	}
	return guikit.Panel(gtx, 12, layout.Inset{Top: 4, Bottom: 4}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

func (p *readyPage) summaryRow(gtx layout.Context, title, detail, state string, done bool) layout.Dimensions {
	u := p.u
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	stateCol := guikit.ColDim
	if done {
		stateCol = guikit.ColText2
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if done {
				return guikit.CheckIcon(gtx, 14)
			}
			return guikit.RingIcon(gtx, 14)
		}),
		layout.Rigid(layout.Spacer{Width: 14}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.Label(gtx, guikit.StBody600, guikit.ColText, title)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if detail == "" {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return u.Label(gtx, guikit.StSmall, guikit.ColDim, detail)
					})
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Width: 16}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.Label(gtx, guikit.StSmall, stateCol, state)
		}),
	)
}

// list joins names as a sentence: "A", "A and B", "A, B and C".
func list(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	// TRANSLATORS: the end of a list of names: "Firefox, GIMP" and "VLC".
	return i18n.Tf("%[1]s and %[2]s", strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}
