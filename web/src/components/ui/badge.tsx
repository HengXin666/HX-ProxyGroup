import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4",
  {
    variants: {
      variant: {
        default: "border-[#b6d8ff] bg-[#ddf4ff] text-[#0550ae]",
        secondary: "border-border bg-[#f6f8fa] text-[#57606a]",
        success: "border-[#aceebb] bg-[#dafbe1] text-[#116329]",
        warning: "border-[#f2cc60] bg-[#fff8c5] text-[#7d4e00]",
        destructive: "border-[#ff8182] bg-[#ffebe9] text-[#a40e26]",
        outline: "border-border bg-white text-[#57606a]",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
)

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <div className={cn(badgeVariants({ variant }), className)} {...props} />
}

export { Badge, badgeVariants }
