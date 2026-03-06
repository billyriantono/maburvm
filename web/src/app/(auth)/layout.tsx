import { cn } from "@/lib/utils";

export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="min-h-screen flex items-center justify-center bg-[#f5f5f0] p-4">
      <div className="w-full max-w-md">
        {/* Logo/Branding */}
        <div className="text-center mb-8">
          <h1 className="text-4xl font-black tracking-tight text-black uppercase">
            MaburVM
          </h1>
          <p className="text-sm font-medium text-gray-500 mt-2 uppercase tracking-widest">
            Virtual Machine Platform
          </p>
        </div>
        
        {/* Main Card */}
        <div className={cn(
          "bg-white border-4 border-black",
          "shadow-[8px_8px_0px_0px_rgba(0,0,0,1)]",
          "p-6 sm:p-8"
        )}>
          {children}
        </div>
        
        {/* Footer */}
        <p className="text-center text-xs text-gray-400 mt-6 font-medium uppercase tracking-wider">
          Secured with enterprise-grade encryption
        </p>
      </div>
    </div>
  );
}