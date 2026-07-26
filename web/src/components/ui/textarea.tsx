import * as React from "react"

import { cn } from "@/lib/utils"

const Textarea = React.forwardRef<HTMLTextAreaElement, React.ComponentProps<"textarea">>(
  ({ className, ...props }, ref) => (
    <textarea
      ref={ref}
      className={cn(
        "flex min-h-20 w-full resize-y rounded-md border bg-white px-2.5 py-2 text-sm text-foreground placeholder:text-[#8c959f] disabled:cursor-not-allowed disabled:bg-[#f6f8fa] disabled:text-muted-foreground",
        className,
      )}
      {...props}
    />
  ),
)
Textarea.displayName = "Textarea"

export { Textarea }
