"use client";

import * as React from "react";
import { toast } from "sonner";

import { iconOf } from "@/components/icons";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { TLS_LABELS, TRAFFIC_TYPES, tlsLabel } from "@/lib/api";
import { cn } from "@/lib/utils";

// 流量原文回放(P3-02):单条镜留痕的元数据条 + 请求包/返回包分侧原文
// (审核迭代:分开展示,各自分段读取与复制)。流量页抽屉与漏洞详情·流量
// 证据区共用(同一 API、同一组件)——原文由服务端 report 同一装配(与
// 报告/证据包同源),分段下发:首段 64KB,「继续读」逐段追加,大正文不
// 一次拉取;「复制」按需拉全该侧后写剪贴板(已读部分不重复拉)。

// byteLen 字节长度(UTF-8):服务端 offset/total 按字节计量,多字节正文
// 不得用 JS 字符数漂移。
const _enc = new TextEncoder();
function byteLen(s) {
  return _enc.encode(s).length;
}

// 复制拉全时单次请求上限(与服务端 rawSegMax 同值,1MiB)。
const COPY_SEG = 1 << 20;

// fmtBytes 字节数的人读形态(精确数字,B/KB/MB/GB 二进制台阶)。
export function fmtBytes(n) {
  const v = Number(n || 0);
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  if (v < 1024 * 1024 * 1024) return `${(v / 1024 / 1024).toFixed(1)} MB`;
  return `${(v / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

// statusVariant 状态码 → 徽标变体(2xx 成功 / 3xx 信息 / 4xx 警告 / 5xx 危险)。
export function statusVariant(code) {
  if (code >= 200 && code < 300) return "success";
  if (code >= 300 && code < 400) return "info";
  if (code >= 400 && code < 500) return "warning";
  if (code >= 500) return "danger";
  return "paused";
}

// ExchangeRaw 原文回放视图:元数据条(方法/URL/状态/证书结论/归因/原始
// 数据摘要)+ 请求包/返回包分侧分段原文。隧道记录无原文,如实呈现元数据。
// bare=素净模式(复现步骤内嵌用):省元数据条与微信息行,原始数据缩为
// 一行大小摘要——步骤头已携带方法/URL/状态,不再重复。P3-11a ③:原始
// 数据引用显示"原始数据 · 大小"式摘要,完整 hash 收进悬停(引用对账
// 用途保留,不占主视觉)。
export function ExchangeRaw({ tid, exID, bare = false }) {
  const [meta, setMeta] = React.useState(null);
  const [error, setError] = React.useState(null);

  React.useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await fetch(`/api/mirror/exchanges/${encodeURIComponent(exID)}?task=${encodeURIComponent(tid)}`);
        const body = await r.json().catch(() => ({}));
        if (!alive) return;
        if (!r.ok) {
          setError(body?.error || `HTTP ${r.status}`);
          return;
        }
        setMeta(body);
        setError(null);
      } catch {
        if (alive) setError("网络未连通");
      }
    })();
    return () => {
      alive = false;
    };
  }, [tid, exID]);

  if (error) {
    return <div className="text-xs text-status-danger">读取失败:{error}</div>;
  }
  if (!meta) {
    return <div className="text-xs text-muted-foreground">读取中…</div>;
  }

  const tlsWord = meta.tls ? tlsLabel(meta.tls) : "";
  return (
    <div className="flex flex-col gap-3">
      {/* 元数据条:一行事实,不解说(bare 模式省略——步骤头已带) */}
      {!bare && (
        <>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            {meta.request?.method && (
              <span className="font-mono font-medium">{meta.request.method}</span>
            )}
            {meta.request?.url && (
              <span className="max-w-full break-all font-mono text-muted-foreground">
                {meta.request.url}
              </span>
            )}
            {meta.response?.status ? (
              <Badge variant={statusVariant(meta.response.status)}>{meta.response.status}</Badge>
            ) : (
              <Badge variant="paused">无响应</Badge>
            )}
            <Badge variant="outline">{TRAFFIC_TYPES[meta.type] || meta.type}</Badge>
            {tlsWord && (
              <Badge variant="warning" title={`目标证书:${tlsWord} · 校验事实如实标注`}>
                目标证书:{tlsWord}
              </Badge>
            )}
            {meta.error && <Badge variant="danger">{meta.error}</Badge>}
          </div>
          <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10px] text-muted-foreground">
            <span>记录 {meta.id}</span>
            {meta.step_id && <span>步骤 {meta.step_id}</span>}
            {meta.unit_id && <span>单位 {meta.unit_id}</span>}
            {meta.request && (
              <span>
                请求 {fmtBytes(meta.request.body_size)}
                {meta.request.body_truncated ? "(捕获截断)" : ""} · 响应{" "}
                {meta.response ? fmtBytes(meta.response.body_size) : "0 B"}
                {meta.response?.body_truncated ? "(捕获截断)" : ""}
              </span>
            )}
            {meta.blob_ref && (
              <span title={meta.blob_ref}>原始数据 · {fmtBytes(meta.raw_total)}</span>
            )}
          </div>
        </>
      )}

      {/* 分侧原文(隧道无原文,如实说明)。bare 模式的证书结论为包外
          信息行(P4-08 ⑥:结论不进包文,复制请求包即贴 Burp 可发) */}
      {meta.raw_total > 0 && meta.request ? (
        <>
          {bare && tlsWord && (
            <div className="text-xs text-muted-foreground">目标证书:{tlsWord}</div>
          )}
          <RawSide tid={tid} exID={exID} side="req" label="请求包" />
          {(meta.response || meta.error) && (
            <RawSide tid={tid} exID={exID} side="resp" label="返回包" />
          )}
          {bare && meta.blob_ref && (
            <div
              className="font-mono text-[10px] text-muted-foreground"
              title={meta.blob_ref}
            >
              原始数据 · {fmtBytes(meta.raw_total)}
            </div>
          )}
        </>
      ) : (
        <div className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
          {meta.type === "tunnel"
            ? "隧道留痕仅元数据(非 TLS 协议或解密接管失败,无原文可回放)"
            : "该记录无原文"}
        </div>
      )}
    </div>
  );
}

// RawSide 单侧原文(请求包/返回包):分段读取 + 复制(复制按需拉全该侧)。
function RawSide({ tid, exID, side, label }) {
  const [text, setText] = React.useState("");
  const [read, setRead] = React.useState(0); // 已读字节游标(与服务端同口径)
  const [total, setTotal] = React.useState(0);
  const [hasMore, setHasMore] = React.useState(false);
  const [loading, setLoading] = React.useState(false);
  const [copying, setCopying] = React.useState(false);
  const [failed, setFailed] = React.useState(null);

  const segURL = (offset, limit) => {
    const p = new URLSearchParams({ task: tid, side, offset: String(offset) });
    if (limit) p.set("limit", String(limit));
    return `/api/mirror/exchanges/${encodeURIComponent(exID)}/raw?${p.toString()}`;
  };

  React.useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await fetch(segURL(0));
        const b = await r.json().catch(() => ({}));
        if (!alive) return;
        if (!r.ok) {
          setFailed(b?.error || `HTTP ${r.status}`);
          return;
        }
        setText(b.text || "");
        setRead(byteLen(b.text || ""));
        setTotal(b.total || 0);
        setHasMore(Boolean(b.has_more));
      } catch {
        if (alive) setFailed("网络未连通");
      }
    })();
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tid, exID, side]);

  const readMore = async () => {
    if (loading || !hasMore) return;
    setLoading(true);
    try {
      const r = await fetch(segURL(read));
      const b = await r.json().catch(() => ({}));
      if (r.ok) {
        setText((t) => t + (b.text || ""));
        setRead((x) => x + byteLen(b.text || ""));
        setTotal(b.total || total);
        setHasMore(Boolean(b.has_more));
      }
    } catch {
      /* 网络闪断:保留已读段,再次点继续读重试 */
    }
    setLoading(false);
  };

  const copy = async () => {
    if (copying) return;
    setCopying(true);
    try {
      // 已读部分不重复拉;未读部分按 1MiB 段拉全后拼合。
      let full = text;
      let offset = read;
      let more = hasMore;
      while (more) {
        const r = await fetch(segURL(offset, COPY_SEG));
        const b = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(b?.error || `HTTP ${r.status}`);
        full += b.text || "";
        offset += byteLen(b.text || "");
        more = Boolean(b.has_more);
      }
      await navigator.clipboard.writeText(full);
      toast.success(`已复制${label} ${total.toLocaleString("en-US")} 字节`);
    } catch (e) {
      toast.error(`复制失败:${e?.message || "剪贴板不可用"}`);
    }
    setCopying(false);
  };

  if (failed) {
    return (
      <div className="flex flex-col gap-1">
        <SideHeader label={label} />
        <div className="text-xs text-status-danger">读取失败:{failed}</div>
      </div>
    );
  }
  if (total === 0) {
    return (
      <div className="flex flex-col gap-1">
        <SideHeader label={label} />
        <span className="text-xs text-muted-foreground">(无内容)</span>
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-1">
      <SideHeader
        label={label}
        copy={{
          onClick: copy,
          busy: copying,
          disabled: total === 0,
        }}
        read={read}
        total={total}
        hasMore={hasMore}
        loading={loading}
        onMore={readMore}
      />
      <pre className="max-h-72 overflow-auto rounded-md bg-input-surface p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
        {text}
      </pre>
    </div>
  );
}

// SideHeader 侧栏头:标签 + 字节计量(仅有未读段时显示,小包免噪)+
// 复制/继续读(数据表工具栏形态)。
function SideHeader({ label, copy, read, total, hasMore, loading, onMore }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <span className="font-medium text-foreground">{label}</span>
      {hasMore && total > 0 && (
        <span className="tabular-nums text-[10px]">
          已读 {read.toLocaleString("en-US")} / {total.toLocaleString("en-US")} 字节
        </span>
      )}
      {copy && (
        <Button
          variant="outline"
          size="sm"
          className="ml-auto h-6 px-2 text-[11px]"
          disabled={copy.busy || copy.disabled}
          onClick={copy.onClick}
          title={`复制${label}全文`}
        >
          {React.createElement(iconOf("copy"), { className: "size-3" })}
          {copy.busy ? "复制中…" : "复制"}
        </Button>
      )}
      {hasMore && (
        <Button
          variant="outline"
          size="sm"
          className={cn("h-6 px-2 text-[11px]", !copy && "ml-auto")}
          disabled={loading}
          onClick={onMore}
        >
          {loading ? "读取中…" : "继续读"}
        </Button>
      )}
    </div>
  );
}

// TrafficTypeBadge 类型徽标(HTTP/HTTPS/隧道;https 附证书结论短标)。
export function TrafficTypeBadge({ type, tls }) {
  const word = tls ? `${TRAFFIC_TYPES[type] || type} · ${tlsLabel(tls)}` : TRAFFIC_TYPES[type] || type;
  return (
    <span
      className={cn(
        "rounded px-1.5 py-0.5 font-mono text-[10px]",
        type === "https"
          ? "bg-status-info-soft text-status-info"
          : type === "tunnel"
            ? "bg-muted text-muted-foreground"
            : "bg-accent text-foreground",
      )}
      title={tls ? `目标证书:${tlsLabel(tls)}` : undefined}
    >
      {word}
    </span>
  );
}
