package main

import (
	"context"
	"strings"
	"testing"
)

func TestExecRunnerRun(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), "echo", "hi")
	if err != nil || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := (ExecRunner{}).Run(context.Background(), "false"); err == nil {
		t.Error("false must fail")
	}
}

func TestExecRunnerStart(t *testing.T) {
	p, err := ExecRunner{}.Start("sh", "-c", "echo one; echo two >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for l := range p.Lines() {
		got = append(got, l)
	}
	if err := p.Wait(); err == nil {
		t.Error("exit 3 must be an error")
	}
	if len(got) != 2 {
		t.Errorf("lines %v: stdout and stderr both", got)
	}
}
