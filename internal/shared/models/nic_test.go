package models

import (
	"strings"
	"testing"
)

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{"52:54:00:AB:CD:EF": "52:54:00:ab:cd:ef", " 52-54-00-ab-cd-ef ": "52:54:00:ab:cd:ef", "02:00:00:00:00:01": "02:00:00:00:00:01"} {
		if got, err := NormalizeMAC(in); err != nil || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "52:54:00:ab:cd", "01:00:5e:00:00:01", "ff:ff:ff:ff:ff:ff", "00:00:00:00:00:00", "52:54:00:ab:cd:ef:01:02", "x"} {
		if _, err := NormalizeMAC(bad); err == nil {
			t.Errorf("NormalizeMAC(%q) accepted", bad)
		}
	}
}

func TestDerivedMACIsStableAndPerCard(t *testing.T) {
	id := "6f1c1f9e-3f5e-4b1a-9a0e-2b8f4e0c1d2a"
	if DerivedMAC(id, 0) != DerivedMAC(id, 0) || DerivedMAC(id, 0) == DerivedMAC(id, 1) {
		t.Fatal("a card's derived MAC must be stable and differ from the next card's")
	}
	if _, err := NormalizeMAC(DerivedMAC(id, 1)); err != nil || !strings.HasPrefix(DerivedMAC(id, 1), "52:54:00:") {
		t.Fatalf("derived MAC %s: %v", DerivedMAC(id, 1), err)
	}
}

func TestParseNameservers(t *testing.T) {
	got, err := ParseNameservers(" 9.9.9.9, 149.112.112.112\n2620:fe::fe 9.9.9.9 ")
	if err != nil || got != "9.9.9.9,149.112.112.112,2620:fe::fe" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := ParseNameservers(""); err != nil || got != "" {
		t.Fatalf("empty: %q, %v", got, err)
	}
	for _, bad := range []string{"dns.example", "0.0.0.0", "fe80::1", "224.0.0.1", "1.1.1.1,1.0.0.1,8.8.8.8,8.8.4.4,9.9.9.9"} {
		if _, err := ParseNameservers(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseIPv6Extra(t *testing.T) {
	got, err := ParseIPv6Extra("2001:db8:20:5::/64", []string{"2001:db8:20:5::25", " 2001:DB8:20:5::25 ", "", "2001:db8:20:5:1::1"})
	if err != nil || strings.Join(got, ",") != "2001:db8:20:5::25,2001:db8:20:5:1::1" {
		t.Fatalf("got %v, %v", got, err)
	}
	for name, addrs := range map[string][]string{
		"outside":  {"2001:db8:20:6::1"},
		"network":  {"2001:db8:20:5::"},
		"own ::1":  {"2001:db8:20:5::1"},
		"ipv4":     {"192.0.2.1"},
		"too many": strings.Split(strings.Repeat("2001:db8:20:5::9,", MaxIPv6ExtraAddresses+1), ","),
	} {
		if _, err := ParseIPv6Extra("2001:db8:20:5::/64", addrs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseIPv6Extra("", nil); err == nil {
		t.Error("no prefix: accepted")
	}
}
