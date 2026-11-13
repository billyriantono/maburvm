import { describe, it, expect } from "vitest"
import { contrast, onAccent, hsl, initials, themeVars, withDefaults, monogram } from "./core"

describe("branding core", () => {
  it("matches known WCAG ratios", () => {
    expect(contrast("#000000", "#ffffff")).toBeCloseTo(21, 1)
    expect(contrast("#777777", "#ffffff")).toBeCloseTo(4.48, 2)
  })
  it("puts dark text on light accents and light text on dark ones", () => {
    expect(onAccent("#ffd500")).toBe("#0a0a0a")
    expect(onAccent("#0a3d91")).toBe("#fafafa")
  })
  it("converts to the token form", () => {
    expect(hsl("#ff0000")).toBe("0 100% 50%")
    expect(hsl("#ffffff")).toBe("0 0% 100%")
  })
  it("leaves the stylesheet's colours alone for a bad or missing colour", () => {
    expect(themeVars({ accent: "red" })).toEqual({})
    expect(themeVars({ accent: "" })).toEqual({})
    expect(themeVars({ accent: "#14674a" })["--primary"]).toBe(hsl("#14674a"))
  })
  it("builds initials and a monogram", () => {
    expect(initials("Acme Cloud")).toBe("AC")
    expect(initials("nimbus")).toBe("NI")
    expect(initials("  ")).toBe("?")
    expect(monogram({ name: "<b>", accent: "#112233" })).not.toContain("%3Cb%3E")
  })
  it("never returns an empty name", () => {
    expect(withDefaults({ name: "" }).name).toBe("MaburVM")
    expect(withDefaults(null).name).toBe("MaburVM")
  })
})
