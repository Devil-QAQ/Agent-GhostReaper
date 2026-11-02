"use client";

import { Toaster as Sonner } from "sonner";

import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

// Toast:操作反馈通知(8.5)。配色走 token(随主题三态切换,P3-10)。
function Toaster(props) {
  const { theme } = useTheme();
  return (
    <Sonner
      theme={theme === "light" ? "light" : "dark"}
      position="top-center"
      toastOptions={{
        style: {
          background: "var(--popover)",
          color: "var(--foreground)",
          border: "1px solid var(--border)",
        },
        // 亮色底下投影淡一档(8.4 亮色系值),暗色沿用浮层大投影。
        classNames: {
          toast: cn(
            "rounded-xl",
            theme === "light" ? "shadow-card" : "shadow-float"
          ),
        },
      }}
      {...props}
    />
  );
}

export { Toaster };
