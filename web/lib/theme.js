"use client";

import * as React from "react";

// 主题三态(P3-10,8.1"暗色为默认,亮色/跟随系统为可切换项"):
// dark / light / system。持久键 gr.theme,未设置 = dark(暗色缺省)。
// <html> 上的 .light class 是视觉真值:首帧前由 layout 内联 boot script
// 落定(防闪烁,见 app/layout.js);此后本模块在切换与系统偏好变化时
// 维护它。跨组件同步走自定义事件(模块单例,无需 Provider)。

export const THEMES = [
  { value: "dark", label: "暗色", icon: "moon" },
  { value: "light", label: "亮色", icon: "sun" },
  { value: "system", label: "跟随系统", icon: "monitor" },
];

const KEY = "gr.theme";
const EVT = "gr:theme";

export function storedTheme() {
  try {
    const v = localStorage.getItem(KEY);
    return THEMES.some((t) => t.value === v) ? v : "dark";
  } catch {
    return "dark";
  }
}

function systemLight() {
  return window.matchMedia("(prefers-color-scheme: light)").matches;
}

// applyClass 按主题落 .light class(唯一写点;boot script 是首帧副本)。
export function applyThemeClass(theme) {
  const light = theme === "light" || (theme === "system" && systemLight());
  document.documentElement.classList.toggle("light", light);
}

export function setTheme(theme) {
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // 存不进(隐私模式等)也照常切换,仅本次会话生效。
  }
  applyThemeClass(theme);
  window.dispatchEvent(new Event(EVT));
}

// useTheme:当前主题(初始 dark,客户端首帧后对齐真值——渲染期不读
// localStorage,避免 SSG 预渲染与 hydration 不一致)。
export function useTheme() {
  const [theme, setLocal] = React.useState("dark");
  React.useEffect(() => {
    const sync = () => setLocal(storedTheme());
    sync();
    window.addEventListener(EVT, sync);
    // 跟随系统态下系统切换即时生效(其主题值不受影响,只重落 class)。
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    const onChange = () => applyThemeClass(storedTheme());
    mq.addEventListener("change", onChange);
    return () => {
      window.removeEventListener(EVT, sync);
      mq.removeEventListener("change", onChange);
    };
  }, []);
  return { theme, setTheme };
}
