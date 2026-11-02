"use client";

import * as React from "react";

import { iconOf } from "@/components/icons";
import { THEMES, useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";

// 主题菜单(P3-10):顶栏入口,暗 / 亮 / 跟随系统三选。下拉卡为手搭
// 轻件(浮层 token + 150ms 淡入位移,8.9),外点与 Esc 关闭——不为
// 三项菜单引入新原语依赖。
export function ThemeMenu() {
  const { theme, setTheme } = useTheme();
  const [open, setOpen] = React.useState(false);
  const rootRef = React.useRef(null);

  React.useEffect(() => {
    if (!open) return;
    const onDown = (e) => {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false);
    };
    const onKey = (e) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const current = THEMES.find((t) => t.value === theme) || THEMES[0];

  return (
    <div ref={rootRef} className="relative">
      <button
        className="flex size-8 items-center justify-center rounded-md hover:bg-hover-lift cursor-pointer"
        title={`主题 · ${current.label}`}
        onClick={() => setOpen((o) => !o)}
      >
        {React.createElement(iconOf(current.icon), { className: "size-4" })}
      </button>
      {open && (
        <div className="absolute right-0 z-50 mt-2 flex w-36 flex-col gap-0.5 rounded-md bg-popover p-1 shadow-float duration-150 animate-rise">
          {THEMES.map((t) => (
            <button
              key={t.value}
              className={cn(
                "flex h-8 items-center gap-2 rounded-md px-3 text-[13px] transition-colors duration-150 cursor-pointer",
                t.value === theme
                  ? "bg-primary/10 font-medium text-primary"
                  : "text-muted-foreground hover:bg-hover-lift hover:text-foreground"
              )}
              onClick={() => {
                setTheme(t.value);
                setOpen(false);
              }}
            >
              {React.createElement(iconOf(t.icon), { className: "size-4 shrink-0" })}
              {t.label}
              {t.value === theme && (
                <span className="ml-auto text-primary">
                  {React.createElement(iconOf("check"), { className: "size-3.5" })}
                </span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
