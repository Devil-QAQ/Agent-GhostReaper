"use client";

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";

import { iconOf } from "@/components/icons";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { fetchJSON, TASK_STATUS, useTaskContext } from "@/lib/api";
import { NAV_ITEMS, defaultTaskTab, enabledTaskTabs } from "@/lib/nav";

// 命令面板(P3-10,⌘J 搜索跳转版):搜页面 / 任务 / 发现,选中即跳。
// 页面与任务走面板已有数据(cmdk 本地模糊过滤);发现(声明)跨任务,
// 按 250ms 去抖走 /api/search。动作版不做(够用再升级,通用规则)。
export function CommandPalette({ open, onOpenChange, tasks }) {
  const { taskID } = useTaskContext();
  const list = Array.isArray(tasks) ? tasks : [];
  const current = list.find((t) => t.id === taskID) || null;

  const [query, setQuery] = React.useState("");
  const [claims, setClaims] = React.useState([]);
  const seqRef = React.useRef(0); // 迟到响应丢弃(只收最后一次)

  // 每次打开重置(上次搜索词与结果不带入)。
  React.useEffect(() => {
    if (open) {
      setQuery("");
      setClaims([]);
    }
  }, [open]);

  // 发现搜索:关键词非空才查;250ms 去抖;乱序响应按序号丢弃。
  React.useEffect(() => {
    if (!open) return;
    const q = query.trim();
    if (!q) {
      seqRef.current++;
      setClaims([]);
      return;
    }
    const seq = ++seqRef.current;
    const timer = setTimeout(async () => {
      const { ok, body } = await fetchJSON(`/api/search?q=${encodeURIComponent(q)}`);
      if (seq === seqRef.current && ok) setClaims(Array.isArray(body.claims) ? body.claims : []);
    }, 250);
    return () => clearTimeout(timer);
  }, [query, open]);

  const go = (hash) => {
    onOpenChange(false);
    location.hash = hash;
  };

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay
          className="fixed inset-0 z-50 bg-black/60 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
        />
        <DialogPrimitive.Content
          className="fixed top-[15%] left-1/2 z-50 w-[min(560px,92vw)] -translate-x-1/2 overflow-hidden rounded-2xl border border-border/55 bg-popover shadow-float duration-200 data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95"
        >
          <DialogPrimitive.Title className="sr-only">命令面板</DialogPrimitive.Title>
          <Command label="命令面板">
            <CommandInput
              value={query}
              onValueChange={setQuery}
              placeholder="搜索页面、任务、发现"
            />
            <CommandList>
              <CommandEmpty>无结果</CommandEmpty>

              <CommandGroup heading="页面">
                {NAV_ITEMS.filter((i) => i.enabled).map((item) => (
                  <CommandItem
                    key={item.id}
                    value={`page:${item.id}`}
                    keywords={[item.title]}
                    onSelect={() => go(`/${item.id}`)}
                  >
                    {React.createElement(iconOf(item.icon), {
                      className: "size-4 shrink-0 text-muted-foreground",
                    })}
                    <span className="truncate">{item.title}</span>
                  </CommandItem>
                ))}
              </CommandGroup>

              {current && (
                <CommandGroup heading={`任务空间 · ${current.title || current.id}`}>
                  {enabledTaskTabs().map((t) => (
                    <CommandItem
                      key={t.id}
                      value={`tab:${current.id}:${t.id}`}
                      keywords={[t.title]}
                      onSelect={() => go(`/task/${current.id}/${t.id}`)}
                    >
                      <span className="truncate">{t.title}</span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              )}

              <CommandGroup heading="任务">
                {list.map((t) => (
                  <CommandItem
                    key={t.id}
                    value={`task:${t.id}`}
                    keywords={[t.title]}
                    onSelect={() => go(`/task/${t.id}/${defaultTaskTab()}`)}
                  >
                    <span className="max-w-[60%] truncate" title={t.title || t.id}>
                      {t.title || t.id}
                    </span>
                    <span className="ml-auto shrink-0 text-xs text-muted-foreground">
                      {TASK_STATUS[t.status]?.label || t.status}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>

              {claims.length > 0 && (
                <CommandGroup heading="发现">
                  {claims.map((c) => (
                    <CommandItem
                      key={c.claim_id}
                      value={`claim:${c.claim_id}`}
                      keywords={[c.title, c.task_title, c.claim_id]}
                      onSelect={() => go(`/task/${c.task_id}/findings`)}
                    >
                      {React.createElement(iconOf("shieldAlert"), {
                        className: "size-4 shrink-0 text-muted-foreground",
                      })}
                      <span className="max-w-[55%] truncate" title={c.title}>
                        {c.title}
                      </span>
                      <span
                        className="ml-auto shrink-0 truncate text-xs text-muted-foreground"
                        title={c.task_title || c.task_id}
                      >
                        {c.task_title || c.task_id}
                      </span>
                    </CommandItem>
                  ))}
                </CommandGroup>
              )}
            </CommandList>
            {/* 键位提示:一行陈述(8.5 能短则短) */}
            <div className="flex items-center gap-3 border-t border-border/55 px-4 py-2 text-xs text-muted-foreground">
              <span>↑↓ 选择</span>
              <span>↵ 跳转</span>
              <span className="ml-auto">esc 关闭</span>
            </div>
          </Command>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
