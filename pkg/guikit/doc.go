// Package guikit is the shared look of Isotope's graphical tools, built on
// Gio: the night palette, the embedded fonts and text styles, the widgets
// (buttons, fields, cards, panels, warnings, links, pick lists) and the
// window frame (the desktop background, the sidebar with the step list, the
// page slide and the footer with its buttons), with the window-wide keys.
//
// The mocks the sizes come from are in CSS pixels; every size here is in dp,
// so the window scales with the output and with the app's zoom (Scaled,
// WindowKeys).
//
// An app keeps one Theme and one Window, and draws each frame as
//
//	gtx = guikit.Scaled(gtx, scale)
//	guikit.WindowKeys(gtx, &scale, back)
//	win.Layout(gtx, nil, func(gtx layout.Context) layout.Dimensions {
//		return th.Frame(gtx, steps, body, footer)
//	})
//
// Nothing here knows what an app asks or does: that stays in the app.
package guikit
