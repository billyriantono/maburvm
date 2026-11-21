package models

import "testing"

func TestVMLimitsValidate(t *testing.T) {
	ok := []VMLimits{
		{},
		{CPUCapPercent: 50, CPUWeight: 200, IOPS: 500, MBps: 100},
		{ReadIOPS: 1000, WriteIOPS: 200, ReadMBps: 200, WriteMBps: 50},
		{CPUCapPercent: 100, CPUWeight: MaxCPUWeight},
	}
	for _, l := range ok {
		if err := l.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", l, err)
		}
	}
	bad := map[string]VMLimits{
		"cap over 100":           {CPUCapPercent: 101},
		"negative cap":           {CPUCapPercent: -1},
		"weight too high":        {CPUWeight: MaxCPUWeight + 1},
		"iops too low":           {IOPS: 5},
		"throughput negative":    {ReadMBps: -1},
		"total with split iops":  {IOPS: 500, ReadIOPS: 100},
		"total with split mbps":  {MBps: 100, WriteMBps: 10},
		"iops above the ceiling": {WriteIOPS: MaxIOPSLimit + 1},
		"mbps above the ceiling": {MBps: MaxMBpsLimit + 1},
	}
	for name, l := range bad {
		if l.Validate() == nil {
			t.Errorf("%s: %+v accepted", name, l)
		}
	}
}

func TestVMLimitsWithThrottle(t *testing.T) {
	base := VMLimits{CPUCapPercent: 80, CPUWeight: 200, IOPS: 2000}
	if got := base.WithThrottle(nil); got != base {
		t.Fatalf("no throttle changed the limits: %+v", got)
	}
	got := base.WithThrottle(&VMLimits{CPUCapPercent: 25, CPUWeight: 50, IOPS: 300, MBps: 20})
	want := VMLimits{CPUCapPercent: 25, CPUWeight: 50, IOPS: 300, MBps: 20}
	if got != want {
		t.Errorf("throttle: got %+v, want %+v", got, want)
	}
	// A throttle looser than the VM's own limit leaves the VM's limit.
	if got := base.WithThrottle(&VMLimits{CPUCapPercent: 90, IOPS: 5000}); got.CPUCapPercent != 80 || got.IOPS != 2000 || got.CPUWeight != 200 {
		t.Errorf("a looser throttle loosened the limits: %+v", got)
	}
	// Against a split, a throttle's total caps each direction and no total is set.
	split := VMLimits{ReadIOPS: 1000, WriteIOPS: 100}
	got = split.WithThrottle(&VMLimits{IOPS: 300})
	if got.IOPS != 0 || got.ReadIOPS != 300 || got.WriteIOPS != 100 {
		t.Errorf("split: got %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("merged limits do not validate: %v", err)
	}
	// Only one direction limited: the other gets the throttle's total.
	if got := (VMLimits{ReadMBps: 50}).WithThrottle(&VMLimits{MBps: 10}); got.ReadMBps != 10 || got.WriteMBps != 10 || got.MBps != 0 {
		t.Errorf("one-sided split: got %+v", got)
	}
}
