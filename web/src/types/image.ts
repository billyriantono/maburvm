// ImageStatus matches the Go image status type
export type ImageStatus = 'pending' | 'available' | 'failed';

// Image is a Vultr/DO-style golden image captured from a VM's disk. It survives
// VM deletion and can seed a new VM (source_image_id on the create endpoint).
export interface Image {
  id: string;
  user_id: string;
  name: string;
  source_vm_id?: string;
  os_template_id?: string;
  size_bytes: number;
  checksum: string;
  status: ImageStatus;
  error_message?: string;
  created_at: string;
}

// CreateImageRequest for capturing an image from a VM
export interface CreateImageRequest {
  vm_id: string;
  name?: string;
}
