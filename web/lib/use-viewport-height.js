"use client";

import * as React from "react";

// 图与会话面板使用实际文档位置，避开页签换行、提示条和侧栏宽度变化。
// 文档坐标不随滚动改变，滚动不会让面板反复伸缩。
export function useViewportHeight(minHeight = 420, bottomInset = 16) {
  const ref = React.useRef(null);
  React.useLayoutEffect(() => {
    const node = ref.current;
    if (!node) return;
    let frame;
    const update = () => {
      const top = node.getBoundingClientRect().top + window.scrollY;
      const height = Math.max(minHeight, Math.floor((window.visualViewport?.height || window.innerHeight) - top - bottomInset));
      if (node.style.height !== `${height}px`) node.style.height = `${height}px`;
    };
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(update);
    };
    const observer = new ResizeObserver(measure);
    if (node.parentElement) observer.observe(node.parentElement);
    window.addEventListener("resize", measure);
    window.visualViewport?.addEventListener("resize", measure);
    // 首次数据可能来自已有缓存；画布首次适配前必须已有最终高度。
    update();
    return () => {
      cancelAnimationFrame(frame);
      observer.disconnect();
      window.removeEventListener("resize", measure);
      window.visualViewport?.removeEventListener("resize", measure);
    };
  }, [minHeight, bottomInset]);
  return ref;
}
