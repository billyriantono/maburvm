import type { RecipeInput } from "@/types/recipe"

/** The labels of required inputs still empty, with defaults counted as filled. */
export function missingInputs(inputs: RecipeInput[] | null | undefined, values: Record<string, string>): string[] {
  return (inputs ?? []).filter((i) => i.required && !(values[i.key] ?? "").trim() && !i.default).map((i) => i.label || i.key)
}

/** The script as it will run, placeholders filled; for showing before ordering. */
export function fillScript(script: string, inputs: RecipeInput[] | null | undefined, values: Record<string, string>): string {
  let out = script
  for (const i of inputs ?? []) {
    const v = (values[i.key] ?? "").trim() ? values[i.key] : (i.default ?? "")
    out = out.split(`{{${i.key}}}`).join(i.secret ? "••••" : v)
  }
  return out
}
