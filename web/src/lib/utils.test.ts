import { describe, expect, it } from "vitest"
import { bitRate, rateScale } from "./utils"

describe("network rates read in bits", () => {
  it("formats bytes per second as bits, 1000-based", () => {
    expect(bitRate(0)).toBe("0 b/s")
    expect(bitRate(NaN)).toBe("0 b/s")
    expect(bitRate(100)).toBe("800 b/s")
    expect(bitRate(125_000)).toBe("1.0 Mb/s")
    expect(bitRate(12_500_000)).toBe("100 Mb/s")
    expect(bitRate(125_000_000)).toBe("1.0 Gb/s")
  })

  it("scales a network strip in bits and a disk strip in bytes", () => {
    const net = rateScale([[12_500_000, 1_000]], [], true) // 100 Mb/s peak
    expect(net.axis).toBe("100 Mb/s")
    expect(net.top).toBe(12_500_000) // still bytes per second
    expect(net.fmt(6_250_000)).toBe("50.0 Mb/s")

    const disk = rateScale([[3 * 1024 * 1024]])
    expect(disk.axis).toBe("5 MB/s")
    expect(disk.fmt(1024 * 1024)).toBe("1.0 MB/s")
  })
})
