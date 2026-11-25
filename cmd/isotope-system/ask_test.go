package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func askTestUI() *UI {
	return newAskUI(func() {})
}

func TestAskVPNSecretJSON(t *testing.T) {
	spec, err := parseAsk([]string{"vpn-secret", "--name", "Work", "--fields", "password,otp", "--message", "Hi"})
	if err != nil || spec.name != "Work" || !reflect.DeepEqual(spec.fields, []string{"password", "otp"}) {
		t.Fatalf("%+v, %v", spec, err)
	}
	u := askTestUI()
	var out bytes.Buffer
	code := -1
	m := newAskModel(u, spec, &out, func(c int) { code = c })
	u.open(m.d)
	if m.d.choices[1].enabled() {
		t.Fatal("Connect is on with empty fields")
	}
	typeInto(m.fields[0].f, "hunter2")
	if m.d.choices[1].enabled() {
		t.Fatal("Connect is on with one field empty")
	}
	typeInto(m.fields[1].f, "123456")
	u.pick(m.d, 1)
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if code != 0 || got["password"] != "hunter2" || got["otp"] != "123456" || len(got) != 2 {
		t.Fatalf("code %d, got %v", code, got)
	}
}

func TestAskVPNSecretCancelExits1(t *testing.T) {
	spec, _ := parseAsk([]string{"vpn-secret", "--name", "Work", "--fields", "password"})
	u := askTestUI()
	var out bytes.Buffer
	code := -1
	m := newAskModel(u, spec, &out, func(c int) { code = c })
	u.open(m.d)
	u.pick(m.d, 0)
	if code != 1 || out.Len() != 0 {
		t.Fatalf("code %d, stdout %q", code, out.String())
	}
}

func TestAskBluetoothFile(t *testing.T) {
	spec, err := parseAsk([]string{"bluetooth-file", "--device", "Phone", "--file", "a.jpg", "--size", "2048"})
	if err != nil || spec.size != 2048 {
		t.Fatalf("%+v, %v", spec, err)
	}
	for i, want := range []string{`{"accept":false}`, `{"accept":true}`} {
		u := askTestUI()
		var out bytes.Buffer
		code := -1
		m := newAskModel(u, spec, &out, func(c int) { code = c })
		u.open(m.d)
		if m.d.focus != 0 {
			t.Fatalf("focus is on choice %d, not Decline", m.d.focus)
		}
		u.pick(m.d, i)
		if code != 0 || out.String() != want+"\n" {
			t.Fatalf("choice %d: code %d, stdout %q", i, code, out.String())
		}
	}
}

func TestParseAskRefuses(t *testing.T) {
	for _, a := range [][]string{
		nil, {"nope"}, {"vpn-secret"}, {"vpn-secret", "--fields"},
		{"bluetooth-file", "--file", "x", "--size", "big"}, {"bluetooth-file", "--name", "x"},
	} {
		if _, err := parseAsk(a); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
}
