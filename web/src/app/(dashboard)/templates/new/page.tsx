"use client"

import { useState, useRef, useCallback } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { 
  ArrowLeft, 
  Upload, 
  Link as LinkIcon, 
  FileArchive,
  CheckCircle2,
  XCircle,
  AlertTriangle,
  Loader2,
  Shield,
  X,
  Server,
  Cloud
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

type UploadMethod = "file" | "url"
type UploadStatus = "idle" | "uploading" | "verifying" | "complete" | "error"

interface FormErrors {
  name?: string
  version?: string
  file?: string
  url?: string
}

// Toast notification component
function Toast({ message, type, onClose }: { message: string, type: "success" | "error", onClose: () => void }) {
  return (
    <div className={`fixed bottom-4 right-4 z-50 px-6 py-4 border-4 border-black shadow-neo ${
      type === "success" ? "bg-success text-black" : "bg-danger text-white"
    }`}>
      <p className="font-bold uppercase text-sm">{message}</p>
    </div>
  )
}

// Progress bar component
function ProgressBar({ progress, status }: { progress: number, status: string }) {
  return (
    <div className="w-full">
      <div className="flex justify-between items-center mb-2">
        <span className="text-sm font-bold uppercase">{status}</span>
        <span className="text-sm font-mono font-bold">{progress}%</span>
      </div>
      <div className="h-4 bg-gray-200 border-2 border-black">
        <div 
          className="h-full bg-success transition-all duration-300"
          style={{ width: `${progress}%` }}
        />
      </div>
    </div>
  )
}

export default function NewTemplatePage() {
  const router = useRouter()
  const fileInputRef = useRef<HTMLInputElement>(null)
  
  // Form state
  const [name, setName] = useState("")
  const [version, setVersion] = useState("1.0.0")
  const [osType, setOsType] = useState("")
  const [description, setDescription] = useState("")
  const [uploadMethod, setUploadMethod] = useState<UploadMethod>("file")
  const [file, setFile] = useState<File | null>(null)
  const [url, setUrl] = useState("")
  const [checksum, setChecksum] = useState("")
  const [verifyChecksum, setVerifyChecksum] = useState(false)
  
  // Upload state
  const [uploadStatus, setUploadStatus] = useState<UploadStatus>("idle")
  const [uploadProgress, setUploadProgress] = useState(0)
  const [uploadError, setUploadError] = useState("")
  const [verificationResult, setVerificationResult] = useState<"valid" | "invalid" | null>(null)
  
  // UI state
  const [errors, setErrors] = useState<FormErrors>({})
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null)
  const [isSubmitting, setIsSubmitting] = useState(false)
  
  // Handle file selection
  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const selectedFile = e.target.files?.[0]
    if (selectedFile) {
      setFile(selectedFile)
      setUploadError("")
      // Auto-fill name from filename if empty
      if (!name) {
        const baseName = selectedFile.name.replace(/\.(qcow2|img|vmdk|raw|vhdx)$/i, "")
        setName(baseName)
      }
    }
  }
  
  // Remove selected file
  const removeFile = () => {
    setFile(null)
    if (fileInputRef.current) {
      fileInputRef.current.value = ""
    }
  }
  
  // Simulate upload progress
  const simulateUpload = useCallback(async () => {
    setUploadStatus("uploading")
    setUploadProgress(0)
    
    // Simulate upload progress
    for (let i = 0; i <= 100; i += 10) {
      await new Promise(resolve => setTimeout(resolve, 200))
      setUploadProgress(i)
    }
    
    // Simulate verification
    if (verifyChecksum && checksum) {
      setUploadStatus("verifying")
      await new Promise(resolve => setTimeout(resolve, 1500))
      
      // Random verification result for demo
      const isValid = Math.random() > 0.2 // 80% success rate
      setVerificationResult(isValid ? "valid" : "invalid")
      
      if (!isValid) {
        setUploadStatus("error")
        setUploadError("Checksum verification failed. The file may be corrupted.")
        return false
      }
    }
    
    setUploadStatus("complete")
    return true
  }, [verifyChecksum, checksum])
  
  // Validate form
  const validateForm = (): boolean => {
    const newErrors: FormErrors = {}
    
    if (!name.trim()) {
      newErrors.name = "Template name is required"
    } else if (!/^[a-z0-9-]+$/.test(name)) {
      newErrors.name = "Name can only contain lowercase letters, numbers, and hyphens"
    }
    
    if (!version.trim()) {
      newErrors.version = "Version is required"
    } else if (!/^\d+\.\d+\.\d+$/.test(version)) {
      newErrors.version = "Version must be in format x.y.z"
    }
    
    if (uploadMethod === "file") {
      if (!file) {
        newErrors.file = "Please select a file to upload"
      }
    } else {
      if (!url.trim()) {
        newErrors.url = "URL is required"
      } else if (!/^https?:\/\/.+/.test(url)) {
        newErrors.url = "Please enter a valid URL"
      }
    }
    
    setErrors(newErrors)
    return Object.keys(newErrors).length === 0
  }
  
  // Handle submit
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    
    if (!validateForm()) return
    
    setIsSubmitting(true)
    
    try {
      // If using file upload, simulate upload first
      if (uploadMethod === "file" && file) {
        const uploadSuccess = await simulateUpload()
        if (!uploadSuccess) {
          setIsSubmitting(false)
          return
        }
      } else if (uploadMethod === "url") {
        // Simulate URL import
        setUploadStatus("uploading")
        setUploadProgress(0)
        
        for (let i = 0; i <= 100; i += 5) {
          await new Promise(resolve => setTimeout(resolve, 150))
          setUploadProgress(i)
        }
        
        if (verifyChecksum && checksum) {
          setUploadStatus("verifying")
          await new Promise(resolve => setTimeout(resolve, 1000))
          
          // Simulate checksum verification
          const isValid = Math.random() > 0.2
          setVerificationResult(isValid ? "valid" : "invalid")
          
          if (!isValid) {
            setUploadStatus("error")
            setUploadError("Checksum verification failed. The downloaded file may be corrupted.")
            setIsSubmitting(false)
            return
          }
        }
        
        setUploadStatus("complete")
      }
      
      // Simulate final submission
      await new Promise(resolve => setTimeout(resolve, 1000))
      
      setToast({ message: `Template "${name}" created successfully`, type: "success" })
      
      // Redirect to templates list
      setTimeout(() => {
        router.push("/templates")
      }, 1500)
      
    } catch (error) {
      setUploadStatus("error")
      setUploadError("An unexpected error occurred")
      setToast({ message: "Failed to create template", type: "error" })
    } finally {
      setIsSubmitting(false)
    }
  }
  
  return (
    <div className="max-w-3xl mx-auto">
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <Link href="/templates">
          <Button variant="ghost" size="sm" className="border-2 border-black">
            <ArrowLeft className="w-4 h-4" />
          </Button>
        </Link>
        <div>
          <h1 className="text-3xl font-black uppercase tracking-tight text-black">
            Add Template
          </h1>
          <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mt-1">
            Upload a new OS template
          </p>
        </div>
      </div>
      
      <form onSubmit={handleSubmit}>
        {/* Basic Info */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <FileArchive className="w-5 h-5" />
            Basic Information
          </h2>
          
          <div className="space-y-4">
            {/* Template Name */}
            <div>
              <label htmlFor="template-name" className="block text-sm font-bold uppercase mb-2">
                Template Name <span className="text-danger">*</span>
              </label>
              <Input
                id="template-name"
                type="text"
                placeholder="e.g., ubuntu-22.04-server"
                value={name}
                onChange={(e) => setName(e.target.value)}
                className={`border-2 border-black ${errors.name ? "border-danger" : ""}`}
              />
              {errors.name && (
                <p className="text-danger text-xs font-bold mt-1">{errors.name}</p>
              )}
              <p className="text-xs text-gray-500 mt-1">
                Lowercase letters, numbers, and hyphens only
              </p>
            </div>
            
            {/* Version */}
            <div>
              <label htmlFor="template-version" className="block text-sm font-bold uppercase mb-2">
                Version <span className="text-danger">*</span>
              </label>
              <Input
                id="template-version"
                type="text"
                placeholder="1.0.0"
                value={version}
                onChange={(e) => setVersion(e.target.value)}
                className={`border-2 border-black w-32 ${errors.version ? "border-danger" : ""}`}
              />
              {errors.version && (
                <p className="text-danger text-xs font-bold mt-1">{errors.version}</p>
              )}
            </div>
            
            {/* OS Type */}
            <div>
              <label htmlFor="os-type" className="block text-sm font-bold uppercase mb-2">
                OS Type
              </label>
              <select
                id="os-type"
                value={osType}
                onChange={(e) => setOsType(e.target.value)}
                className="h-12 px-4 border-2 border-black font-medium bg-white focus:outline-none focus:shadow-neo-sm w-full"
              >
                <option value="">Select OS Type</option>
                <option value="Ubuntu 22.04 LTS">Ubuntu 22.04 LTS</option>
                <option value="Ubuntu 24.04 LTS">Ubuntu 24.04 LTS</option>
                <option value="Debian 12">Debian 12</option>
                <option value="CentOS Stream 9">CentOS Stream 9</option>
                <option value="Rocky Linux 9">Rocky Linux 9</option>
                <option value="AlmaLinux 9">AlmaLinux 9</option>
                <option value="Windows Server 2022">Windows Server 2022</option>
                <option value="Windows Server 2019">Windows Server 2019</option>
                <option value="Other">Other</option>
              </select>
            </div>
            
            {/* Description */}
            <div>
              <label htmlFor="template-description" className="block text-sm font-bold uppercase mb-2">
                Description
              </label>
              <textarea
                id="template-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional description for this template..."
                rows={3}
                className="w-full px-4 py-3 border-2 border-black font-medium focus:outline-none focus:shadow-neo-sm resize-none"
              />
            </div>
          </div>
        </div>
        
        {/* Upload Source */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <h2 className="text-lg font-black uppercase mb-4 flex items-center gap-2">
            <Cloud className="w-5 h-5" />
            Upload Source
          </h2>
          
          {/* Upload Method Tabs */}
          <div className="flex gap-2 mb-6">
            <button
              type="button"
              onClick={() => setUploadMethod("file")}
              className={`flex-1 py-3 px-4 font-bold uppercase text-sm border-2 border-black transition-all ${
                uploadMethod === "file" 
                  ? "bg-black text-white shadow-neo" 
                  : "bg-white hover:bg-gray-50"
              }`}
            >
              <Upload className="w-4 h-4 inline mr-2" />
              File Upload
            </button>
            <button
              type="button"
              onClick={() => setUploadMethod("url")}
              className={`flex-1 py-3 px-4 font-bold uppercase text-sm border-2 border-black transition-all ${
                uploadMethod === "url" 
                  ? "bg-black text-white shadow-neo" 
                  : "bg-white hover:bg-gray-50"
              }`}
            >
              <LinkIcon className="w-4 h-4 inline mr-2" />
              Import from URL
            </button>
          </div>
          
          {/* File Upload */}
          {uploadMethod === "file" && (
            <div>
              <input
                ref={fileInputRef}
                type="file"
                accept=".qcow2,.img,.vmdk,.raw,.vhdx,.zip,.tar.gz"
                onChange={handleFileChange}
                className="hidden"
              />
              
              {!file ? (
                <button
                  type="button"
                  onClick={() => fileInputRef.current?.click()}
                  className="w-full py-12 border-2 border-dashed border-black hover:border-black hover:bg-gray-50 transition-colors"
                >
                  <Upload className="w-12 h-12 mx-auto text-gray-400 mb-4" />
                  <p className="font-bold uppercase">Click to upload</p>
                  <p className="text-sm text-gray-500 mt-1">
                    Supported: qcow2, img, vmdk, raw, vhdx, zip, tar.gz
                  </p>
                </button>
              ) : (
                <div className="flex items-center justify-between p-4 bg-gray-50 border-2 border-black">
                  <div className="flex items-center gap-3">
                    <FileArchive className="w-8 h-8 text-primary" />
                    <div>
                      <p className="font-bold">{file.name}</p>
                      <p className="text-sm text-gray-500">
                        {(file.size / (1024 * 1024 * 1024)).toFixed(2)} GB
                      </p>
                    </div>
                  </div>
                  <button
                    type="button"
                    onClick={removeFile}
                    className="p-2 hover:bg-danger hover:text-white border-2 border-transparent hover:border-black transition-colors"
                  >
                    <X className="w-5 h-5" />
                  </button>
                </div>
              )}
              {errors.file && (
                <p className="text-danger text-xs font-bold mt-2">{errors.file}</p>
              )}
            </div>
          )}
          
          {/* URL Import */}
          {uploadMethod === "url" && (
            <div>
              <label htmlFor="template-url" className="block text-sm font-bold uppercase mb-2">
                Template URL <span className="text-danger">*</span>
              </label>
              <div className="flex gap-2">
                <div className="relative flex-1">
                  <LinkIcon className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400" />
                  <Input
                    id="template-url"
                    type="url"
                    placeholder="https://example.com/template.qcow2"
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    className={`pl-10 border-2 border-black ${errors.url ? "border-danger" : ""}`}
                  />
                </div>
              </div>
              {errors.url && (
                <p className="text-danger text-xs font-bold mt-2">{errors.url}</p>
              )}
              <p className="text-xs text-gray-500 mt-2">
                The agent will download the template from the specified URL
              </p>
            </div>
          )}
          
          {/* Upload Progress */}
          {uploadStatus !== "idle" && (
            <div className="mt-6 p-4 bg-gray-50 border-2 border-black">
              {uploadStatus === "uploading" && (
                <ProgressBar 
                  progress={uploadProgress} 
                  status={uploadMethod === "file" ? "Uploading file..." : "Downloading from URL..."} 
                />
              )}
              
              {uploadStatus === "verifying" && (
                <div className="flex items-center gap-2">
                  <Loader2 className="w-5 h-5 animate-spin" />
                  <span className="font-bold uppercase">Verifying checksum...</span>
                </div>
              )}
              
              {uploadStatus === "complete" && (
                <div className="flex items-center gap-2 text-success">
                  <CheckCircle2 className="w-5 h-5" />
                  <span className="font-bold uppercase">Upload complete</span>
                </div>
              )}
              
              {uploadStatus === "error" && (
                <div className="flex items-center gap-2 text-danger">
                  <XCircle className="w-5 h-5" />
                  <span className="font-bold uppercase">{uploadError}</span>
                </div>
              )}
            </div>
          )}
        </div>
        
        {/* Checksum Verification */}
        <div className="bg-white border-4 border-black p-6 shadow-neo mb-6">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-lg font-black uppercase flex items-center gap-2">
              <Shield className="w-5 h-5" />
              Checksum Verification
            </h2>
            <label className="flex items-center gap-2 cursor-pointer">
              <input
                type="checkbox"
                checked={verifyChecksum}
                onChange={(e) => setVerifyChecksum(e.target.checked)}
                className="w-5 h-5 border-2 border-black accent-black"
              />
              <span className="font-bold uppercase text-sm">Enable</span>
            </label>
          </div>
          
          {verifyChecksum && (
            <div>
              <label htmlFor="checksum" className="block text-sm font-bold uppercase mb-2">
                Expected Checksum (SHA256)
              </label>
              <Input
                id="checksum"
                type="text"
                placeholder="e.g., a1b2c3d4e5f6..."
                value={checksum}
                onChange={(e) => setChecksum(e.target.value)}
                className="font-mono text-sm"
              />
              <p className="text-xs text-gray-500 mt-2">
                Enter the expected SHA256 checksum to verify file integrity
              </p>
              
              {verificationResult && (
                <div className={`mt-4 p-3 border-2 border-black ${
                  verificationResult === "valid" ? "bg-success" : "bg-danger"
                }`}>
                  <div className="flex items-center gap-2">
                    {verificationResult === "valid" ? (
                      <>
                        <CheckCircle2 className="w-5 h-5" />
                        <span className="font-bold uppercase">Checksum verified successfully</span>
                      </>
                    ) : (
                      <>
                        <AlertTriangle className="w-5 h-5" />
                        <span className="font-bold uppercase">Checksum verification failed</span>
                      </>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
          
          {!verifyChecksum && (
            <p className="text-sm text-gray-500">
              Optional: Enable checksum verification to ensure file integrity after upload
            </p>
          )}
        </div>
        
        {/* Submit */}
        <div className="flex gap-4 justify-end">
          <Link href="/templates">
            <Button variant="ghost" type="button" className="border-2 border-black">
              Cancel
            </Button>
          </Link>
          <Button 
            type="submit" 
            disabled={isSubmitting || uploadStatus === "uploading" || uploadStatus === "verifying"}
            className="gap-2"
          >
            {isSubmitting && <Loader2 className="w-4 h-4 animate-spin" />}
            Create Template
          </Button>
        </div>
      </form>
      
      {/* Toast */}
      {toast && (
        <Toast 
          message={toast.message} 
          type={toast.type} 
          onClose={() => setToast(null)} 
        />
      )}
    </div>
  )
}