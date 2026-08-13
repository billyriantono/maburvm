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
  /** Present only while the node is exporting this image's disk. */
  progress?: ExportProgress;
}

// ExportProgress is how far an in-flight capture has got.
//
// Deliberately no percentage: the output is compressed, so written/source is the
// compression ratio, not completion. Bytes and elapsed time are honest; a
// percentage would look precise and be wrong.
export interface ExportProgress {
  written_bytes: number;
  source_bytes: number;
  started_at: string;
  elapsed_seconds: number;
  bytes_per_second: number;
}

// CreateImageRequest for capturing an image from a VM
export interface CreateImageRequest {
  vm_id: string;
  name?: string;
}
