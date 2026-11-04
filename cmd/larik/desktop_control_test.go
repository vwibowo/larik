package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseDesktopFlagsAfterOperation(t *testing.T) {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	json := fs.Bool("json", false, "")
	name := fs.String("name", "", "")
	if err := parseDesktopFlags(fs, []string{"save", "--name", "my layout", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !*json || *name != "my layout" || !reflect.DeepEqual(fs.Args(), []string{"save"}) {
		t.Fatalf("json=%v name=%q args=%q", *json, *name, fs.Args())
	}
}

func TestParseDesktopFlagsAfterLayoutValue(t *testing.T) {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	json := fs.Bool("json", false, "")
	if err := parseDesktopFlags(fs, []string{"layout", "columns", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !*json || !reflect.DeepEqual(fs.Args(), []string{"layout", "columns"}) {
		t.Fatalf("json=%v args=%q", *json, fs.Args())
	}
}
