import * as React from "react"
import { cn } from "@/lib/utils"

export interface BadgeProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: "default" | "secondary" | "destructive" | "outline" | "success" | "warning"
}

function Badge({ className, variant = "default", ...props }: BadgeProps) {
  return (
    <div
      className={cn(
        "inline-flex items-center px-3 py-1 text-xs font-bold uppercase border-2 border-black shadow-neo-sm",
        {
          "bg-primary text-black": variant === "default",
          "bg-secondary text-black": variant === "secondary",
          "bg-danger text-white": variant === "destructive",
          "bg-success text-black": variant === "success",
          "bg-warning text-black": variant === "warning",
          "bg-white text-black": variant === "outline",
        },
        className
      )}
      {...props}
    />
  )
}

export { Badge }