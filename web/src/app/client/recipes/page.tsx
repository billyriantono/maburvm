"use client"

import { ActingAccountScope } from "@/lib/acting-account"
import { RecipeManager } from "@/components/recipe-manager"

function ClientRecipesPageBody() {
  return <RecipeManager mode="client" />
}

// A team member picks which account's resources the page shows.
export default function ClientRecipesPage() {
  return (
    <ActingAccountScope>
      <ClientRecipesPageBody />
    </ActingAccountScope>
  )
}
