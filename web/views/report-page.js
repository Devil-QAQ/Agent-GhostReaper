"use client";

import * as React from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { toast } from "sonner";

import { EmptyState } from "@/components/empty-state";
import { fmtBytes } from "@/components/exchange-raw";
import { downloadExport, ExportDialog } from "@/components/export-dialog";
import { iconOf } from "@/components/icons";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { postJSON, usePolling } from "@/lib/api";

// 报告页(8.3 交付域 [P1],P1-10 点亮;P2-13 升级为完整报告;P3-02 增
// 报告存档):本任务展示 + 导出 + 存档——正文含聚合摘要(级别分布)、
// Confirmed/待确认每条的完整反证时间线与复现步骤(Burp 可验证原文),
// 纯模板零 LLM(R17),相同账本状态字节级一致。存档(P3-02)把当前
// 渲染固化为带证据版本的快照:证据更新(绑定/补绑/收割定稿)后旧存档
// 自动标"待更新",按旧证据版本存档被拒(409)。跨任务导出在一级导航
// 「报告」页。
export function ReportPage({ currentTask }) {
  const tid = currentTask?.id;
  // 报告是确定性文本(非 JSON),用本地文本轮询:低频 5s 随任务推进更新。
  const [md, setMd] = React.useState("");
  const [error, setError] = React.useState(null);
  const [exportOpen, setExportOpen] = React.useState(false);

  React.useEffect(() => {
    if (!tid) return;
    let alive = true;
    let timer;
    const load = async () => {
      try {
        const r = await fetch(`/api/tasks/${tid}/report`);
        const text = await r.text();
        if (!alive) return;
        if (r.ok) {
          setMd(text);
          setError(null);
        } else {
          setError(text || `HTTP ${r.status}`);
        }
      } catch {
        if (alive) setError("网络未连通");
      }
    };
    load();
    timer = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [tid]);

  if (!tid) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader
          title="报告"
          subtitle="本任务完整报告 · 反证链与复现步骤随发现呈现 · 零 LLM"
        />
        <EmptyState
          icon="fileText"
          title="未选择任务"
          hint="报告随任务切换——在任务列表选择一个任务"
          action={{ label: "去任务列表", href: "/tasks" }}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="报告"
        subtitle="本任务完整报告 · 反证链与复现步骤随发现呈现 · 零 LLM"
      >
        <Button onClick={() => setExportOpen(true)}>
          {React.createElement(iconOf("download"), { className: "size-4" })}
          导出
        </Button>
      </PageHeader>

      {error ? (
        <EmptyState icon="fileText" title="读取失败" hint={error} />
      ) : md ? (
        <div className="rounded-xl border border-border/55 bg-card p-6">
          <ReportMarkdown>{md}</ReportMarkdown>
        </div>
      ) : (
        <EmptyState icon="fileText" title="报告读取中" hint="年轮账本读取中…" />
      )}

      <ExportDialog
        open={exportOpen}
        onOpenChange={setExportOpen}
        title="导出本任务报告"
        description="报告由账本确定性组装,零 LLM;zip 含逐发现分文件与流量证据包。"
        scopes={[["all", "全部发现"]]}
        urlOf={(format) => `/api/tasks/${tid}/report/export?format=${format}`}
        fallbackName={`report-${tid}.md`}
      />

      <ArchivesCard tid={tid} />
    </div>
  );
}

// ArchivesCard 报告存档(P3-02):存档 = 当前账本状态的报告快照(原文入
// 原始数据、登记入账本)。存档携带证据版本(乐观锁):证据更新后旧版本
// 存档被 409 拒;旧存档自动标「待更新」。
function ArchivesCard({ tid }) {
  const { data, error, setData } = usePolling(
    `/api/tasks/${tid}/report/archives`,
    5000,
  );
  const [busy, setBusy] = React.useState(false);
  const archives = Array.isArray(data?.archives) ? data.archives : [];
  const currentEV = data?.current_evidence_version ?? 0;

  const archive = async () => {
    setBusy(true);
    const { ok, status, body } = await postJSON(`/api/tasks/${tid}/report/archive`, {
      evidence_version: currentEV,
    });
    setBusy(false);
    if (status === 409) {
      toast.error(body?.error || "证据已更新,存档被拒");
      // 拒因即线索:拉最新证据版本后再试由操作者决定。
      fetch(`/api/tasks/${tid}/report/archives`)
        .then((r) => r.json())
        .then((b) => setData(b))
        .catch(() => {});
      return;
    }
    if (!ok) {
      toast.error(`存档失败:${body?.error || `HTTP ${status}`}`);
      return;
    }
    toast.success(`已存档(证据版本 ${body.evidence_version})`);
    // 即时回填:存档进清单(不再等下一拍)。
    fetch(`/api/tasks/${tid}/report/archives`)
      .then((r) => r.json())
      .then((b) => setData(b))
      .catch(() => {});
  };

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border/55 bg-card p-6">
      <div className="flex flex-wrap items-center gap-3">
        <div className="text-sm font-semibold">报告存档</div>
        <span className="text-xs text-muted-foreground">
          当前证据版本 <span className="tabular-nums">{currentEV}</span>(随绑定/补绑/检查归纳定稿推进)
        </span>
        <Button size="sm" className="ml-auto" disabled={busy} onClick={archive}>
          {busy ? "存档中…" : "存档"}
        </Button>
      </div>
      {error ? (
        <div className="text-xs text-status-danger">读取失败:{error}</div>
      ) : archives.length === 0 ? (
        <span className="text-xs text-muted-foreground">暂无存档;存档固化当前报告,供交付与追溯</span>
      ) : (
        <ul className="flex flex-col gap-2">
          {archives.map((a) => (
            <li
              key={a.rev}
              className={cn(
                "flex flex-wrap items-center gap-2 rounded-md border border-border/55 bg-card px-3 py-2 text-xs",
                a.stale && "border-status-warning/40",
              )}
            >
              <span className="text-muted-foreground" title={`版本号 ${a.rev}`}>
                {fmtAt(a.at)}
              </span>
              <span className="text-muted-foreground">
                证据版本 <span className="tabular-nums">{a.evidence_version}</span>
              </span>
              <span className="tabular-nums text-muted-foreground">{fmtBytes(a.size)}</span>
              {a.stale ? (
                <Badge variant="warning">待更新</Badge>
              ) : (
                <Badge variant="success">最新</Badge>
              )}
              <Button
                variant="outline"
                size="sm"
                className="ml-auto h-6 px-2 text-[11px]"
                onClick={async () => {
                  try {
                    const name = await downloadExport(
                      `/api/tasks/${tid}/report/archives/${a.rev}`,
                      `report-${tid}-r${a.rev}.md`,
                    );
                    toast.success(`已下载 ${name}`);
                  } catch (e) {
                    toast.error(`下载失败:${e?.message || "网络未连通"}`);
                  }
                }}
              >
                {React.createElement(iconOf("download"), { className: "size-3.5" })}
                下载
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function fmtAt(at) {
  if (!at) return "—";
  return String(at).replace("T", " ").replace("Z", " UTC").replace(/\.\d+/, "");
}

// ReportMarkdown:md 渲染的统一样式层(token 工具类,零硬编码色值)。
// 服务端模板产出的可信 md;react-markdown 默认不渲染原始 HTML。
function ReportMarkdown({ children }) {
  return (
    <div className="flex flex-col gap-3 text-sm">
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: (p) => <h1 className="t-page text-foreground" {...p} />,
          // P2-14b⑤:分节留白拉开(h2 mt-6),分组靠留白不靠线(8.9 §2)
          h2: (p) => (
            <h2 className="t-sec mt-6 border-b pb-2 text-foreground" {...p} />
          ),
          h3: (p) => <h3 className="mt-4 text-sm font-semibold text-foreground" {...p} />,
          p: (p) => <p className="leading-relaxed text-foreground" {...p} />,
          ul: (p) => <ul className="flex list-disc flex-col gap-1 pl-5" {...p} />,
          li: (p) => <li className="leading-relaxed" {...p} />,
          strong: (p) => <strong className="font-semibold text-foreground" {...p} />,
          blockquote: (p) => (
            <blockquote
              className="rounded-md bg-status-info-soft px-3 py-2 text-xs text-status-info"
              {...p}
            />
          ),
          hr: () => <hr className="my-2 border-border" />,
          table: (p) => (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-xs" {...p} />
            </div>
          ),
          th: (p) => (
            <th className="border-b border-border px-2 py-2 font-medium text-muted-foreground" {...p} />
          ),
          td: (p) => <td className="border-b border-border/50 px-2 py-2 align-top" {...p} />,
          code: ({ className, ...p }) => (
            <code
              className={cn(
                "rounded bg-accent px-1 py-0.5 num text-xs",
                className
              )}
              {...p}
            />
          ),
        }}
      >
        {children}
      </Markdown>
    </div>
  );
}
