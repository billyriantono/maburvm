import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 px-6 py-3 font-bold uppercase text-sm tracking-wide border-2 border-black transition-all duration-150 ease-out shadow-neo hover:shadow-neo-hover active:shadow-neo-active hover:translate-x-[-2px] hover:translate-y-[-2px] active:translate-x-[2px] active:translate-y-[2px] disabled:opacity-50 disabled:pointer-events-none disabled:translate-x-0 disabled:translate-y-0 disabled:shadow-neo focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-black focus-visible:ring-offset-2",
  {
    variants: {
      variant: {
        default: "bg-primary text-black hover:bg-yellow-300",
        secondary: "bg-secondary text-black hover:bg-cyan-300",
        accent: "bg-accent text-white hover:bg-pink-400",
        destructive: "bg-danger text-white hover:bg-red-500",
        success: "bg-success text-black hover:bg-lime-300",
        warning: "bg-warning text-black hover:bg-orange-300",
        ghost: "bg-transparent hover:bg-gray-200 border-black",
        outline: "bg-transparent border-2 border-black hover:bg-black hover:text-white",
      },
      size: {
        default: "h-12 px-6 py-3",
        sm: "h-8 px-4 py-2 text-xs",
        lg: "h-14 px-8 py-4 text-base",
        xl: "h-16 px-10 py-5 text-lg",
        icon: "h-12 w-12",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    return (
      <button
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        {...props}
      />
    );
  }
);
Button.displayName = "Button";

export { Button, buttonVariants };