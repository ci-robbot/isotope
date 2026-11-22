module github.com/kairos-community/isotope/system

go 1.25

require (
	gioui.org v0.10.3
	github.com/godbus/dbus/v5 v5.2.2
	github.com/kairos-community/isotope/guikit v0.0.0-00010101000000-000000000000
	github.com/kairos-community/isotope/update v0.0.0-00010101000000-000000000000
)

require (
	gioui.org/shader v1.0.9 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	golang.org/x/exp/shiny v0.0.0-20250408133849-7e4ce0ab07d0 // indirect
	golang.org/x/image v0.26.0 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
)

replace (
	github.com/kairos-community/isotope/guikit => ../guikit
	github.com/kairos-community/isotope/update => ../update
)
