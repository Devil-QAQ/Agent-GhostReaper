"use client";

import * as React from "react";

// Portal 保留 React 上下文；兄弟形式的第二层抽屉通过 nested 显式递进。
export const FloatingLayerContext = React.createContext(0);

export function FloatingLayer({ nested, children }) {
  const parent = React.useContext(FloatingLayerContext);
  return (
    <FloatingLayerContext.Provider value={Math.max(parent + 1, nested ? 2 : 1)}>
      {children}
    </FloatingLayerContext.Provider>
  );
}

export function useFloatingLayer() {
  return React.useContext(FloatingLayerContext);
}

export function floatingShadow(depth) {
  return depth > 2 ? "shadow-float-deep" : depth > 1 ? "shadow-float-nested" : "shadow-float";
}
