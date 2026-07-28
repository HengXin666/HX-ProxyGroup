import * as React from "react"

import { cn } from "@/lib/utils"

const Input = React.forwardRef<HTMLInputElement, React.ComponentProps<"input">>(
  ({ className, type, ...props }, ref) => (
    <input
      ref={ref}
      type={type}
      className={cn(
        "flex h-8 w-full rounded-md border bg-card px-2.5 text-sm text-foreground placeholder:text-[#8c959f] disabled:cursor-not-allowed disabled:bg-muted/60 disabled:text-muted-foreground",
        className,
      )}
      {...props}
    />
  ),
)
Input.displayName = "Input"

export { Input }
