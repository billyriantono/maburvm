/** Tags as typed ("Web, prod  db") to the list sent to the panel, which checks them again. */
export function parseTags(text: string): string[] {
  return [...new Set(text.split(/[\s,]+/).map((t) => t.trim().toLowerCase()).filter(Boolean))]
}
