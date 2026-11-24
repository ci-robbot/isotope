package main

// Codec choice for Bluetooth audio. WirePlumber keeps one card per device
// and each codec is a profile of that card. The profiles are read from one
// `pw-dump` call (JSON), because wpctl 0.5.7 does not print them: its
// `inspect` shows only properties. The write is `wpctl set-profile`. This is
// the only place the page runs a CLI tool and parses what it prints.
//
// The card is found by address: BlueZ's Device1.Address is the card's
// api.bluez5.address (its device.name is bluez_card.XX_XX_...).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/kairos-community/isotope/pkg/i18n"
)

// Profile is one A2DP profile of a Bluetooth card.
type Profile struct {
	Name   string // a2dp-sink-aac
	Label  string // AAC
	Active bool
}

// toolRunner runs a program and returns its output. The tests swap it with
// setTool; a page load may still be running when a test ends.
var (
	toolMu     sync.RWMutex
	toolRunner = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).Output()
		return string(out), err
	}
)

func runTool(name string, args ...string) (string, error) {
	toolMu.RLock()
	f := toolRunner
	toolMu.RUnlock()
	return f(name, args...)
}

func setTool(f func(name string, args ...string) (string, error)) (old func(name string, args ...string) (string, error)) {
	toolMu.Lock()
	defer toolMu.Unlock()
	old, toolRunner = toolRunner, f
	return old
}

// profileLabels maps WirePlumber's A2DP profile names to what the page
// shows. An unknown codec keeps its raw name.
var profileLabels = map[string]string{
	"a2dp-sink-sbc":        "SBC",
	"a2dp-sink-sbc_xq":     "SBC-XQ",
	"a2dp-sink-aac":        "AAC",
	"a2dp-sink-aptx":       "aptX",
	"a2dp-sink-aptx_hd":    "aptX HD",
	"a2dp-sink-ldac":       "LDAC",
	"a2dp-sink-lc3plus_h3": "LC3",
	"a2dp-sink-opus_05":    "Opus",
	"a2dp-sink-opus_g":     i18n.Mark("Opus (gaming)"),
}

// pwObject is the part of a pw-dump object the page reads. pw-dump prints
// {"id", "type", "version", "permissions", "info": {"props": {...},
// "params": {"EnumProfile": [...], "Profile": [...]}}} for a device.
type pwObject struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props  map[string]any `json:"props"`
		Params struct {
			EnumProfile []struct {
				Index       int    `json:"index"`
				Name        string `json:"name"`
				Description string `json:"description"`
				Available   string `json:"available"`
			} `json:"EnumProfile"`
			Profile []struct {
				Index int `json:"index"`
			} `json:"Profile"`
		} `json:"params"`
	} `json:"info"`
}

// card is a bluez card with its profiles.
type card struct {
	id       int
	profiles []cardProfile
}

type cardProfile struct {
	index  int
	name   string
	desc   string
	active bool
}

// parseCard finds the bluez5 device with this address in pw-dump's JSON;
// nil when there is none. A profile that is not available (available "no")
// is left out.
func parseCard(dump, address string) (*card, error) {
	var objs []pwObject
	if err := json.Unmarshal([]byte(dump), &objs); err != nil {
		return nil, fmt.Errorf("pw-dump: %w", err)
	}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Device" {
			continue
		}
		if api, _ := o.Info.Props["device.api"].(string); api != "bluez5" {
			continue
		}
		if addr, _ := o.Info.Props["api.bluez5.address"].(string); !strings.EqualFold(addr, address) {
			continue
		}
		active := -1
		if len(o.Info.Params.Profile) > 0 {
			active = o.Info.Params.Profile[0].Index
		}
		c := &card{id: o.ID}
		for _, p := range o.Info.Params.EnumProfile {
			if p.Available == "no" {
				continue
			}
			c.profiles = append(c.profiles, cardProfile{index: p.Index, name: p.Name, desc: p.Description, active: p.Index == active})
		}
		return c, nil
	}
	return nil, nil
}

// findCard is the card of the device with this BlueZ address, nil when
// PipeWire has none (the device is not connected as audio).
func findCard(address string) (*card, error) {
	out, err := runTool("pw-dump")
	if err != nil {
		return nil, err
	}
	return parseCard(out, address)
}

// isA2DP tells the profiles the page offers: playback codecs only. The
// headset profile (calls) and "off" are not a sound-quality choice.
func isA2DP(name string) bool { return name == "a2dp-sink" || strings.HasPrefix(name, "a2dp-sink-") }

var reCodec = regexp.MustCompile(`codec ([^)]+)`)

// norm makes "aptX HD", "aptX-HD" and "aptx_hd" the same key.
func norm(s string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(s))
}

// label is what the page shows. WirePlumber gives the best codec the bare
// name a2dp-sink, so that profile's codec is read from its description
// ("... (A2DP Sink, codec AAC)"); with none there, it is "Automatic".
func (p cardProfile) label() string {
	if p.name != "a2dp-sink" {
		return profileLabel(p.name)
	}
	m := reCodec.FindStringSubmatch(p.desc)
	if m == nil {
		return i18n.X("codec", "Automatic")
	}
	codec := strings.TrimSpace(m[1])
	for name, label := range profileLabels {
		if norm(label) == norm(codec) || norm(strings.TrimPrefix(name, "a2dp-sink-")) == norm(codec) {
			return i18n.T(label)
		}
	}
	return codec
}

func profileLabel(name string) string {
	if l, ok := profileLabels[name]; ok {
		return i18n.T(l)
	}
	return name
}

// Profiles are the A2DP profiles of the device with this BlueZ address
// (Device1.Address), the active one marked. None when the device has no
// card.
func Profiles(address string) ([]Profile, error) {
	c, err := findCard(address)
	if err != nil || c == nil {
		return nil, err
	}
	var out []Profile
	for _, p := range c.profiles {
		if isA2DP(p.name) {
			out = append(out, Profile{Name: p.name, Label: p.label(), Active: p.active})
		}
	}
	return out, nil
}

// SetProfile makes profile (a Profile.Name) the active one of the device
// with this BlueZ address.
func SetProfile(address, profile string) error {
	c, err := findCard(address)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New(i18n.T("this device has no audio card yet"))
	}
	for _, p := range c.profiles {
		if p.name == profile {
			_, err := runTool("wpctl", "set-profile", strconv.Itoa(c.id), strconv.Itoa(p.index))
			return err
		}
	}
	return fmt.Errorf(i18n.T("the device has no profile %s"), profile)
}
