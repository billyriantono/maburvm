package models

import (
	"slices"
	"testing"
)

func TestParseAndFormatCPUSet(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		out  string
	}{
		{"", nil, ""},
		{"3", []int{3}, "3"},
		{"2-5,8", []int{2, 3, 4, 5, 8}, "2-5,8"},
		{" 8 , 2-3,3 ", []int{2, 3, 8}, "2-3,8"},
		{"0-1,4-5", []int{0, 1, 4, 5}, "0-1,4-5"},
	}
	for _, c := range cases {
		got, err := ParseCPUSet(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Fatalf("ParseCPUSet(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
		if f := FormatCPUSet(got); f != c.out {
			t.Fatalf("FormatCPUSet(%v) = %q, want %q", got, f, c.out)
		}
	}
	for _, bad := range []string{"a", "5-2", "-1", "1-", "0-99999"} {
		if _, err := ParseCPUSet(bad); err == nil {
			t.Fatalf("ParseCPUSet(%q) accepted", bad)
		}
	}
}

func TestHardwareTopologyFallsBackWhenItDoesNotDivide(t *testing.T) {
	cases := []struct {
		hw            Hardware
		vcpus         int
		s, c, threads int
	}{
		{Hardware{}, 4, 1, 4, 1},
		{Hardware{Sockets: 2, Threads: 2}, 8, 2, 2, 2},
		{Hardware{Sockets: 2}, 3, 1, 3, 1},
		{Hardware{Threads: 2}, 1, 1, 1, 1},
	}
	for _, c := range cases {
		s, co, th := c.hw.Topology(c.vcpus)
		if s != c.s || co != c.c || th != c.threads {
			t.Fatalf("%+v.Topology(%d) = %d/%d/%d, want %d/%d/%d", c.hw, c.vcpus, s, co, th, c.s, c.c, c.threads)
		}
		if s*co*th != c.vcpus && c.vcpus > 0 {
			t.Fatalf("topology %d/%d/%d does not hold %d vCPUs", s, co, th, c.vcpus)
		}
	}
}

func TestHardwareValidate(t *testing.T) {
	if err := (Hardware{CPUModel: "Haswell-noTSX", Sockets: 2, Threads: 2, DedicatedCPU: true, NUMA: true, PinnedCPUs: "2-3"}).Validate(2); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Hardware{
		{CPUModel: "bad model; rm"},
		{Sockets: 9},
		{Threads: 3},
		{NUMA: true},
		{Firmware: "coreboot"},
		{PinnedCPUs: "2-4"},
	} {
		if err := bad.Validate(2); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestFreeCPUsSkipsHeldCores(t *testing.T) {
	got, ok := FreeCPUs([]int{2, 3, 4, 5, 6}, []string{"2-3", "5"}, 2)
	if !ok || !slices.Equal(got, []int{4, 6}) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := FreeCPUs([]int{2, 3}, []string{"2"}, 2); ok {
		t.Fatal("a pool with one free core gave two")
	}
}
