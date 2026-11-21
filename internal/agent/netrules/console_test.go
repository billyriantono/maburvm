package netrules

import (
	"reflect"
	"strings"
	"testing"
)

func TestConsoleShield(t *testing.T) {
	if got := strings.Join(ConsoleShieldRule(5901), " "); got != "! -i lo -p tcp --dport 5901 -j DROP" {
		t.Fatalf("rule = %q", got)
	}
	// As `iptables -S` prints them, chain declaration included.
	have := ShieldedPorts([]string{
		"-N MABURVM-CONSOLE",
		"-A MABURVM-CONSOLE ! -i lo -p tcp -m tcp --dport 5900 -j DROP",
		"-A MABURVM-CONSOLE ! -i lo -p tcp -m tcp --dport 5904 -j DROP",
	})
	if !reflect.DeepEqual(have, []int{5900, 5904}) {
		t.Fatalf("ports read back = %v", have)
	}
	add, remove := ConsoleShieldPlan(have, []int{5904, 5907, 5907})
	if !reflect.DeepEqual(add, []int{5907}) || !reflect.DeepEqual(remove, []int{5900}) {
		t.Fatalf("add %v remove %v", add, remove)
	}
	if add, remove := ConsoleShieldPlan(nil, nil); add != nil || remove != nil {
		t.Fatalf("nothing to do, got add %v remove %v", add, remove)
	}
}
