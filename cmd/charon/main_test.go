package main

import (
	"reflect"
	"testing"
)

func TestScopesForPermission(t *testing.T) {
	got, err := scopesForPermission("readonly")
	if err != nil {
		t.Fatalf("readonly: %v", err)
	}
	want := []string{"memory.search", "memory.read", "thread.read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readonly scopes = %v, want %v", got, want)
	}
	if _, err := scopesForPermission("admin"); err == nil {
		t.Fatal("expected invalid permission error")
	}
}

func TestSplitNonEmpty(t *testing.T) {
	got := splitNonEmpty(" default,lethe,default, * ")
	want := []string{"default", "lethe", "*"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %v, want %v", got, want)
	}
}
