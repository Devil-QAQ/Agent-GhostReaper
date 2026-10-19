"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

// 实时推送连接(P3-08):面板与后端之间的单条 WebSocket(/api/ws)。
// 服务端三类消息:ready(连接就绪,重连成功同义——订阅方全量重同步)、
// dirty(高频轻事件:某主题数据已变,载荷为空,订阅方自行拉取)、
// approval(低频重事件:携带该任务当前全部待决审批,供即时呈现)。
// 断线自动重连(1s 起指数退避,封顶 10s);断线期间消费方降级为轮询
// 刷新(见 lib/api.js usePolling 的 live 参数)。
const LiveContext = createContext(null);

// 重连退避:1s、2s、4s … 封顶 10s(断线后到首次重试之间是轮询降级窗)。
const RETRY_BASE_MS = 1000;
const RETRY_MAX_MS = 10000;

export function LiveProvider({ children }) {
  const [status, setStatus] = useState("connecting"); // connecting | open | closed
  const subsRef = useRef(new Map()); // 主题 -> Set<回调>

  const notify = useCallback((topic, detail) => {
    const set = subsRef.current.get(topic);
    if (!set) return;
    for (const cb of [...set]) {
      try {
        cb(detail);
      } catch {
        // 单个订阅方异常不拖垮整条推送线。
      }
    }
  }, []);

  useEffect(() => {
    let stopped = false;
    let ws = null;
    let timer = null;
    let retry = 0;
    const settle = (s) => setStatus(s);
    const connect = () => {
      if (stopped) return;
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      try {
        ws = new WebSocket(`${proto}//${location.host}/api/ws`);
      } catch {
        settle("closed");
        schedule();
        return;
      }
      ws.onmessage = (e) => {
        let m = null;
        try {
          m = JSON.parse(e.data);
        } catch {
          return;
        }
        if (m.t === "ready") {
          retry = 0;
          settle("open");
        } else if (m.t === "dirty") {
          notify(m.k);
        } else if (m.t === "approval") {
          notify(`approval:${m.task}`, m);
        }
      };
      ws.onclose = () => {
        if (stopped) return;
        settle("closed");
        schedule();
      };
      ws.onerror = () => {
        try {
          ws.close();
        } catch {}
      };
    };
    const schedule = () => {
      if (stopped) return;
      const wait = Math.min(RETRY_BASE_MS * 2 ** retry++, RETRY_MAX_MS);
      timer = setTimeout(connect, wait);
    };
    connect();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      try {
        ws && ws.close();
      } catch {}
    };
  }, [notify]);

  const api = useMemo(
    () => ({
      status,
      // subscribe(topics, cb):主题数组一次订阅,返回退订函数。
      // 审批主题的回调收到载荷对象,轻主题收到 undefined。
      subscribe(topics, cb) {
        const list = (topics || []).filter(Boolean);
        for (const t of list) {
          let set = subsRef.current.get(t);
          if (!set) subsRef.current.set(t, (set = new Set()));
          set.add(cb);
        }
        return () => {
          for (const t of list) {
            const set = subsRef.current.get(t);
            if (set) {
              set.delete(cb);
              if (!set.size) subsRef.current.delete(t);
            }
          }
        };
      },
    }),
    [status]
  );

  return <LiveContext.Provider value={api}>{children}</LiveContext.Provider>;
}

export function useLive() {
  const ctx = useContext(LiveContext);
  // 无 Provider(单测/预渲染)时给恒降级视图:状态 closed、订阅为空。
  return ctx || { status: "closed", subscribe: () => () => {} };
}
