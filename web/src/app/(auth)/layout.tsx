import { cn } from "@/lib/utils";

export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <div className="w-full max-w-md">
        {/* Logo/Branding */}
        <div className="text-center mb-8">
          <h1 className="text-4xl font-bold tracking-tight text-foreground">
            MaburVM
          </h1>
          <p className="text-sm font-medium text-muted-foreground mt-2">
            Virtual Machine Platform
          </p>
        </div>

        {/* Main Card */}
        <div className={cn(
          "bg-card text-card-foreground border rounded-lg shadow-sm",
          "p-6 sm:p-8"
        )}>
          {children}
        </div>

        {/* Footer */}
        <p className="text-center text-xs text-muted-foreground mt-6">
          &copy; {new Date().getFullYear()} MaburVM. All rights reserved.
        </p>
      </div>
    </div>
  );
}