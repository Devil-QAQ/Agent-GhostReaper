"use client";

import * as React from "react";

import { cn } from "@/lib/utils";
import { compositionInputHandlers } from "@/lib/composition-input";

function Textarea({ className, ...props }) {
  const composing = React.useRef(false);
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "flex min-h-16 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
      {...compositionInputHandlers(composing, props)}
    />
  );
}

export { Textarea };
