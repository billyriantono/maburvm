import Link from 'next/link'
import { Home, AlertTriangle } from 'lucide-react'

export default function NotFound() {
  return (
    <div className="min-h-screen bg-[#FFE500] flex flex-col items-center justify-center p-4">
      <div className="bg-white border-4 border-black p-8 md:p-12 shadow-[8px_8px_0px_0px_rgba(0,0,0,1)] max-w-lg w-full text-center">
        <div className="flex justify-center mb-6">
          <div className="bg-[#FF4444] p-4 border-4 border-black shadow-[4px_4px_0px_0px_rgba(0,0,0,1)]">
            <AlertTriangle className="w-16 h-16 text-white" />
          </div>
        </div>
        
        <h1 className="text-6xl font-black uppercase tracking-tight border-b-4 border-black pb-4 mb-6">
          404
        </h1>
        
        <h2 className="text-2xl font-bold uppercase mb-4">
          Page Not Found
        </h2>
        
        <p className="text-gray-700 font-medium mb-8 border-2 border-dashed border-black p-4">
          The page you are looking for does not exist, has been moved, or is temporarily unavailable.
        </p>
        
        <Link 
          href="/dashboard"
          className="inline-flex items-center justify-center gap-2 px-8 py-4 font-black uppercase text-lg border-4 border-black bg-white text-black transition-all hover:bg-[#00F0FF] hover:-translate-y-1 hover:-translate-x-1 shadow-[4px_4px_0px_0px_rgba(0,0,0,1)] hover:shadow-[8px_8px_0px_0px_rgba(0,0,0,1)] active:translate-y-0 active:translate-x-0 active:shadow-[0px_0px_0px_0px_rgba(0,0,0,1)]"
        >
          <Home className="w-6 h-6" />
          Return Home
        </Link>
      </div>
    </div>
  )
}
