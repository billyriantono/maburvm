// Checks of network values typed into a form, mirrored from the server so a
// person sees the problem before sending. The server checks them again.

const MAC = /^([0-9a-f]{2})[:-]([0-9a-f]{2})[:-]([0-9a-f]{2})[:-]([0-9a-f]{2})[:-]([0-9a-f]{2})[:-]([0-9a-f]{2})$/i

/** Why a MAC cannot be given to a card, or "" when it can. */
export function macProblem(input: string): string {
  const m = MAC.exec(input.trim())
  if (!m) return "Write it as six pairs of hex digits, for example 52:54:00:12:34:56."
  if (parseInt(m[1], 16) & 1) return "That is a multicast address; a card needs one whose first byte is even."
  if (m.slice(1).every((b) => b === "00")) return "00:00:00:00:00:00 cannot be used."
  return ""
}

/** The addresses in a box with one per line (or separated by commas or spaces). */
export function parseAddressList(input: string): string[] {
  return Array.from(new Set(input.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean)))
}

/** "bridge:uplink" lines back into pairs; blank lines are skipped. */
export function parseVLANBridges(input: string): { bridge: string; uplink: string }[] {
  return input
    .split(/[\n,]+/)
    .map((l) => l.trim())
    .filter(Boolean)
    .map((l) => {
      const [bridge, uplink = ""] = l.split(":").map((s) => s.trim())
      return { bridge, uplink }
    })
}

const IFNAME = /^[A-Za-z0-9._-]{1,15}$/

/** Why a list of VLAN bridges cannot be saved, or "" when it can. */
export function vlanBridgesProblem(list: { bridge: string; uplink: string }[]): string {
  const seen = new Set<string>()
  for (const { bridge, uplink } of list) {
    if (!IFNAME.test(bridge) || !IFNAME.test(uplink)) return `Write each line as bridge:uplink with interface names, for example br1:bond0 (got "${bridge}:${uplink}").`
    if (bridge === uplink) return `${bridge} cannot be its own uplink.`
    if (seen.has(bridge)) return `${bridge} is named twice.`
    seen.add(bridge)
  }
  return ""
}
