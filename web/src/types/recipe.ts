// Recipe is a user's saved first-boot script (Virtualizor "Recipes"), injected
// as cloud-init user-data when a VM is created so it runs once on first boot.
export interface Recipe {
  id: string;
  user_id: string;
  name: string;
  description: string;
  script: string;
  created_at: string;
  updated_at: string;
}

export interface RecipeRequest {
  name: string;
  description?: string;
  script: string;
}
