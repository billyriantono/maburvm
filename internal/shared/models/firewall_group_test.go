package models

import (
	"reflect"
	"testing"
)

func ids(rs []FirewallRule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// Groups placed before the VM's own rules come first, groups placed after
// come last, each side by sort order and each set by priority; the result is
// renumbered in that order so the node, which sorts by priority, keeps it.
func TestMergeFirewallRulesOrder(t *testing.T) {
	own := []FirewallRule{{ID: "own-b", Priority: 50}, {ID: "own-a", Priority: 10}}
	got := MergeFirewallRules(own, []AttachedRules{
		{Position: FirewallGroupAfter, SortOrder: 2, Rules: []FirewallRule{{ID: "after-2", Priority: 1}}},
		{Position: FirewallGroupBefore, SortOrder: 1, Rules: []FirewallRule{{ID: "before-1b", Priority: 900}, {ID: "before-1a", Priority: 5}}},
		{Position: FirewallGroupAfter, SortOrder: 1, Rules: []FirewallRule{{ID: "after-1", Priority: 1000}}},
		{Position: FirewallGroupBefore, SortOrder: 0, Rules: []FirewallRule{{ID: "before-0", Priority: 700}}},
	})
	want := []string{"before-0", "before-1a", "before-1b", "own-a", "own-b", "after-1", "after-2"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
	for i, r := range got {
		if r.Priority != i+1 {
			t.Fatalf("rule %s priority = %d, want %d", r.ID, r.Priority, i+1)
		}
	}
	if own[0].Priority != 50 {
		t.Fatal("the caller's rules must not be renumbered in place")
	}
}

// Without groups a VM's rules go out exactly as before.
func TestMergeFirewallRulesWithoutGroupsIsUnchanged(t *testing.T) {
	own := []FirewallRule{{ID: "a", Priority: 30}, {ID: "b", Priority: 30}}
	got := MergeFirewallRules(own, nil)
	if !reflect.DeepEqual(got, own) {
		t.Fatalf("got %v", got)
	}
}

// Past 1000 rules the priority stays at the node's ceiling, and the order holds
// because the node's sort is stable.
func TestMergeFirewallRulesCapsPriority(t *testing.T) {
	own := make([]FirewallRule, 1005)
	got := MergeFirewallRules(own, []AttachedRules{{Position: FirewallGroupAfter, Rules: []FirewallRule{{ID: "g"}}}})
	if last := got[len(got)-1]; last.ID != "g" || last.Priority != 1000 {
		t.Fatalf("last = %+v", last)
	}
}

func TestGroupRuleAsVMRule(t *testing.T) {
	r := FirewallGroupRule{ID: "r1", GroupID: "g", Protocol: "icmp", Action: "allow", Direction: "inbound", SourceIP: "10.0.0.0/8", Priority: 7, ICMPType: "echo-request", Log: true, Family: FirewallFamilyV4}
	v := r.AsVMRule("00000000-0000-0000-0000-000000000001")
	if v.ID != "r1" || v.VMID != "00000000-0000-0000-0000-000000000001" || v.ICMPType != "echo-request" || !v.Log || v.Family != FirewallFamilyV4 || v.SourceIP != "10.0.0.0/8" || v.Priority != 7 {
		t.Fatalf("got %+v", v)
	}
	if errs := v.Validate(); len(errs) != 0 {
		t.Fatalf("a valid group rule must validate as a VM rule: %v", errs)
	}
}
