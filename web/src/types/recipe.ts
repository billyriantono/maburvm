// Recipe is a saved first-boot script, injected as cloud-init user-data when
// a VM is created so it runs once on first boot. A public one is in the app
// catalog staff publish to every customer.
export interface RecipeInput {
  /** Named in the script as {{key}}. */
  key: string
  label: string
  description?: string
  required: boolean
  default?: string
  secret?: boolean
}

export interface Recipe {
  id: string;
  user_id: string;
  name: string;
  description: string;
  script: string;
  is_public: boolean;
  inputs: RecipeInput[] | null;
  created_at: string;
  updated_at: string;
}

export interface RecipeRequest {
  name: string;
  description?: string;
  script: string;
  inputs?: RecipeInput[];
  /** Publishing is for staff who manage templates; ignored for anyone else. */
  is_public?: boolean;
}
