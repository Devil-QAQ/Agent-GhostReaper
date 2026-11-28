"use client";

import * as React from "react";
import { toast } from "sonner";
import { Field, Fold } from "@/components/form-bits";
import { iconOf } from "@/components/icons";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { fetchJSON, postJSON, usePolling } from "@/lib/api";

const ROOT = "/api/search-services";

export function SearchConfigPage() {
  const { data, error, setData } = usePolling(ROOT, 10000);
  const services = Array.isArray(data?.services) ? data.services : [];
  const providers = Array.isArray(data?.providers) ? data.providers : [];
  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState(undefined);
  const [deleting, setDeleting] = React.useState("");
  const [form, setForm] = React.useState({});
  const [busy, setBusy] = React.useState(false);
  const [advanced, setAdvanced] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [quickTest, setQuickTest] = React.useState(null);
  React.useEffect(() => { setQuickTest(null); }, [form]);
  const provider = providers.find((item) => item.kind === form.kind);
  const custom = !provider && form.kind !== "github";

  function edit(service) {
    const next = service || [{ kind: "github", name: "GitHub" }, ...providers].find((item) => !services.some((saved) => saved.kind === item.kind)) || { kind: "custom", name: "" };
    setEditing(service || null);
    setDeleting("");
    setAdvanced(false);
    setTesting(false);
    setQuickTest(null);
    setForm({ kind: next.kind || "custom", custom_limits: service?.custom_limits || false, per_minute: service?.per_minute ?? (next.kind === "github" ? 9 : 0), code_per_minute: service?.code_per_minute || 9, per_hour: service?.per_hour || 54, per_day: service?.per_day || 0, qps: service?.qps || 0, limit_plan: service?.limit_plan || "", page_size: service?.page_size || next.fixed_size || 10, name: next.name || "", url: next.url || "", enabled: service?.enabled ?? true,
      method: service?.method || "GET", encoding: service?.encoding || "json", response_mode: service?.response_mode || "text",
      headers: JSON.stringify(service?.headers || {}, null, 2), params: JSON.stringify(service?.params || { q: "{{query}}" }, null, 2),
      results_path: service?.results_path ?? "results", title_path: service?.title_path ?? "title", url_path: service?.url_path ?? "url", snippet_path: service?.snippet_path ?? "content",
      api_key: "", clear_key: false, proxy_dirty: false,
      proxy_protocol: service?.proxy_endpoint?.protocol || "direct", proxy_host: service?.proxy_endpoint?.host || "", proxy_port: service?.proxy_endpoint?.port || "",
      proxy_username: "", proxy_password: "", proxy_clear_auth: false });
    setOpen(true);
  }

  function configure() { setEditing(undefined); setDeleting(""); setOpen(true); }

  function requestPayload() {
    if (!form.name?.trim()) { toast.error("填写服务名称"); return; }
    if (form.kind !== "github" && !form.url?.trim()) { toast.error("填写搜索接口地址"); return; }
    let headers, params;
    try {
      if (custom) {
      headers = JSON.parse(form.headers || "{}");
      params = JSON.parse(form.params || "{}");
      if (!headers || Array.isArray(headers) || typeof headers !== "object" || Object.values(headers).some((v) => typeof v !== "string")) throw new Error();
      if (!params || Array.isArray(params) || typeof params !== "object") throw new Error();
      }
    } catch { toast.error("请求头和参数须为 JSON 对象，请检查格式"); return; }
    const payload = { kind: form.kind, custom_limits: Boolean(form.custom_limits), limits_version: 1, qps: Number(form.qps), limit_plan: form.limit_plan, per_minute: Number(form.per_minute), per_hour: Number(form.per_hour), per_day: Number(form.per_day), page_size: Number(form.page_size), name: form.name, url: form.url, enabled: form.enabled, method: form.method, encoding: form.encoding, headers, params,
      response_mode: form.response_mode, results_path: form.results_path, title_path: form.title_path, url_path: form.url_path, snippet_path: form.snippet_path };
    if (form.kind === "github") payload.code_per_minute = Number(form.code_per_minute);
    if (form.clear_key) payload.api_key = "";
    else if (form.api_key?.trim()) payload.api_key = form.api_key?.trim();
    if (form.proxy_dirty) {
      payload.proxy_config = { protocol: form.proxy_protocol, host: form.proxy_host, port: Number(form.proxy_port), clear_auth: Boolean(form.proxy_clear_auth) };
      if (form.proxy_username) payload.proxy_config.username = form.proxy_username;
      if (form.proxy_password) payload.proxy_config.password = form.proxy_password;
    }
    return payload;
  }

  async function testDraft() {
    const payload = requestPayload(); if (!payload) return;
    setBusy(true); setQuickTest(null);
    try {
      const r = await postJSON(`${ROOT}${editing ? `/${editing.id}` : ""}/test`, payload);
      setQuickTest(r.ok ? { result: r.body } : { error: r.body.error || "测试失败" });
      const refreshed = await fetchJSON(ROOT); if (refreshed.ok) setData(refreshed.body);
    } catch { setQuickTest({ error: "网络未连通" }); }
    finally { setBusy(false); }
  }

  async function save() {
    const payload = requestPayload(); if (!payload) return;
    setBusy(true);
    try {
      const r = await postJSON(`${ROOT}${editing ? `/${editing.id}` : ""}`, payload);
      if (!r.ok) { toast.error(r.body.error || "保存失败"); return; }
      setData(r.body);
      setEditing(undefined);
      toast.success("已保存，对下一次搜索生效");
    } catch { toast.error("网络未连通"); }
    finally { setBusy(false); }
  }

  async function remove(id) {
    setBusy(true);
    try {
      const r = await fetchJSON(`${ROOT}/${id}`, { method: "DELETE" });
      if (!r.ok) { toast.error(r.body.error || "删除失败"); return; }
      setData(r.body);
      setDeleting("");
      toast.success("已删除搜索服务");
    } catch { toast.error("网络未连通"); }
    finally { setBusy(false); }
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader title="搜索配置" subtitle="外网搜索服务">
        <Button size="sm" variant="outline" disabled={!data} onClick={configure}>
          {React.createElement(iconOf("settings"), { className: "size-4" })}配置
        </Button>
      </PageHeader>
      {error && <p role="alert" className="text-xs text-status-danger">{error}</p>}
      {!data && !error ? <p className="text-xs text-muted-foreground">读取中</p> : services.length ? (
        <div className="grid auto-rows-fr grid-cols-1 gap-3 lg:grid-cols-2">
          {services.map((service) => (
            <Card key={service.id} data-card-kind="search-service" role="button" tabIndex={0} aria-label={`打开 ${service.name}`} className="h-full min-w-0 cursor-pointer transition-shadow duration-150 hover:shadow-card" onClick={() => edit(service)} onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") { e.preventDefault(); edit(service); }
            }}>
              <CardContent className="flex h-full min-w-0 flex-col gap-3 p-4">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="t-sec flex min-w-0 items-center gap-2 leading-none">{React.createElement(iconOf("search"), { className: "size-4 shrink-0 text-muted-foreground" })}<span className="break-all">{service.name}</span></span>
                  <Badge variant={service.available ? "success" : "outline"}>{!service.enabled ? "已停用" : service.available === false ? "待配置" : "已启用"}</Badge>
                </div>
                <div className="flex flex-col gap-1">
                  <p className="truncate text-xs text-muted-foreground" title={service.url}>{service.url}</p>
                  {service.kind === "github" && <>
                    <p className="text-xs text-muted-foreground">仓库 {service.quota?.minute_used || 0}/{service.per_minute} · 代码 {service.quota?.code_minute_used || 0}/{service.quota?.code_per_minute || 9} 次/分钟 · 读取 {service.quota?.hour_used || 0}/{service.per_hour} 次/小时</p>
                    <GitHubWait quota={service.quota} />
                    {service.quota_error && <p role="alert" className="text-xs text-status-danger">{service.quota_error}</p>}
                  </>}
                  {providers.some((item) => item.kind === service.kind) ? <>
                    <p className="text-xs text-muted-foreground">查询 {service.asset_quota?.day_used || 0} 次/24小时 · {service.qps ? `${service.qps} 次/秒` : service.per_minute ? `${service.per_minute} 次/分钟` : service.per_day ? `${service.per_day} 次/24小时上限` : "本地不限额"}</p>
                    <AssetWait quota={service.asset_quota} />
                    {service.quota_error && <p role="alert" className="text-xs text-status-danger">{service.quota_error}</p>}
                  </> : service.kind !== "github" && <p className="text-xs text-muted-foreground">{service.method} · {service.response_mode === "text" ? "原始响应" : "结构化结果"}</p>}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      ) : !error && <EmptyState icon="search" title="暂无搜索服务" action={{ label: "新增服务", onClick: () => edit(null) }} />}
      <Dialog open={open} onOpenChange={(value) => { if (!busy) setOpen(value); }}>
        <DialogContent hideClose={busy} className="flex max-h-[88vh] max-w-2xl flex-col gap-4 overflow-hidden p-6">
          <DialogHeader className="shrink-0 border-b pb-4 pr-6">
            <DialogTitle>{editing === undefined ? "搜索配置" : editing ? "编辑服务" : "新增服务"}</DialogTitle>
            <DialogDescription>{editing === undefined ? "每项配置对应一个搜索服务" : "保存后对下一次搜索生效"}</DialogDescription>
          </DialogHeader>
          {editing === undefined ? <>
            <div className="flex min-h-0 flex-col gap-3 overflow-y-auto">
              {services.length ? services.map((service) => <div key={service.id} className="flex flex-col gap-2 rounded-lg border border-border/55 p-3">
                <div className="flex min-w-0 items-center gap-2">
                  <div className="min-w-0 flex-1"><p className="truncate text-sm font-medium">{service.name}</p><p className="truncate text-xs text-muted-foreground">{service.url}</p></div>
                  <Button size="sm" variant="ghost" disabled={busy} aria-label={`编辑 ${service.name}`} onClick={() => edit(service)}>编辑</Button>
                  <Button size="sm" variant="ghost" disabled={busy} aria-label={`删除 ${service.name}`} onClick={() => setDeleting(service.id)}>删除</Button>
                </div>
                {deleting === service.id && <div className="flex flex-wrap items-center gap-2 border-t pt-2">
                  <p className="flex-1 text-xs text-muted-foreground">删除此搜索服务及配置记录？</p>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDeleting("")}>取消</Button>
                  <Button size="sm" variant="destructive" disabled={busy} onClick={() => remove(service.id)}>确认删除</Button>
                </div>}
              </div>) : <p className="text-xs text-muted-foreground">暂无搜索服务</p>}
            </div>
            <DialogFooter className="shrink-0 border-t pt-4"><Button size="sm" onClick={() => edit(null)}>新增服务</Button></DialogFooter>
          </> : <>
            <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
              <Field label="站点类型"><Select value={form.kind || "custom"} disabled={busy || Boolean(editing)} onValueChange={(kind) => { const p = providers.find((item) => item.kind === kind); setForm({ ...form, kind, name: p?.name || (kind === "github" ? "GitHub" : ""), url: p?.url || "", per_minute: kind === "github" ? 9 : 0, code_per_minute: 9, per_day: 0, qps: 0, limit_plan: "", page_size: Math.min(p?.fixed_size || 10, p?.max_size || 10), custom_limits: false, api_key: "", clear_key: false }); }}><SelectTrigger aria-label="搜索站点类型"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="github" disabled={!editing && services.some((item) => item.kind === "github")}>GitHub</SelectItem>{providers.map((item) => <SelectItem key={item.kind} value={item.kind} disabled={!editing && services.some((saved) => saved.kind === item.kind)}>{item.name}</SelectItem>)}<SelectItem value="custom">自定义 API</SelectItem></SelectContent></Select></Field>
              {custom && <Field label="名称"><Input aria-label="搜索服务名称" value={form.name || ""} maxLength={80} disabled={busy} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>}
              {custom && <>
              <Field label="请求方式"><Select value={form.method || "GET"} disabled={busy} onValueChange={(method) => setForm({ ...form, method })}>
                <SelectTrigger aria-label="搜索请求方式"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="GET">GET</SelectItem><SelectItem value="POST">POST</SelectItem></SelectContent>
              </Select></Field>
              <Field label="接口地址"><Input aria-label="搜索接口地址" type="url" value={form.url || ""} disabled={busy} onChange={(e) => setForm({ ...form, url: e.target.value })} /></Field>
              </>}

              {!provider?.no_key && <Field label={form.kind === "github" ? "Token" : form.kind === "ddpoc" ? "登录Cookie" : "密钥"}>
                <div className="flex w-full min-w-0 items-center gap-2">
                  <Input className="min-w-0 flex-1" aria-label={form.kind === "github" ? "GitHub Token" : form.kind === "ddpoc" ? "DDPoc 登录Cookie" : "搜索服务密钥"} type="password" autoComplete="off" value={form.api_key || ""} disabled={busy || form.clear_key} placeholder={form.clear_key ? "保存后清除" : editing?.has_key ? `已配置 ${editing.key_mask}` : form.kind === "github" ? "可选，留空匿名访问" : form.kind === "ddpoc" ? "填入 web1 值或包含 web1 的 Cookie" : provider ? "配置 API Key 后可用" : "输入密钥"} onChange={(e) => setForm({ ...form, api_key: e.target.value })} />
                  {editing?.has_key && <Button className="shrink-0" size="sm" variant={form.clear_key ? "secondary" : "ghost"} disabled={busy} onClick={() => setForm({ ...form, clear_key: !form.clear_key })}>{form.clear_key ? "取消清除" : "清除"}</Button>}
                </div>
              </Field>}
              {form.kind === "ddpoc" && <p className="text-xs text-muted-foreground">使用登录账号搜索并读取可见页面</p>}
              {provider?.no_key && <p className="text-xs text-muted-foreground">无需密钥，可直接保存；代理可在高级设置中配置</p>}
              {!custom && <Fold open={advanced} onToggle={() => setAdvanced(!advanced)}>高级设置</Fold>}
              {!custom && advanced && <>
              <Field label="名称"><Input aria-label="搜索服务名称" value={form.name || ""} maxLength={80} disabled={busy} onChange={(e) => setForm({ ...form, name: e.target.value })} /></Field>
              {provider && <Field label="基础地址"><Input aria-label="搜索站点基础地址" type="url" disabled={busy} value={form.url || ""} onChange={(e) => setForm({ ...form, url: e.target.value })} /></Field>}
              {provider && <><Fold withArrow={false}>请求额度</Fold><AssetLimits form={form} setForm={setForm} provider={provider} profiles={data?.limit_profiles?.[form.kind] || []} busy={busy} /><AssetWait quota={services.find((item) => item.id === editing?.id)?.asset_quota} /><a className="w-fit text-xs text-muted-foreground underline" href={provider.docs} target="_blank" rel="noreferrer">{form.kind === "ddpoc" ? "站点入口" : "接口文档"}</a></>}
              {form.kind === "github" && <><Fold withArrow={false}>请求额度</Fold><GitHubLimits form={form} setForm={setForm} hasKey={Boolean(editing?.has_key)} busy={busy} /><GitHubWait quota={services.find((item) => item.id === editing?.id)?.quota} /></>}
              <Fold withArrow={false}>出口代理</Fold>
              <ProxyFields form={form} setForm={setForm} hasAuth={Boolean(editing?.proxy_endpoint?.has_auth)} busy={busy} />
              </>}
              {custom && <>
              <p className="text-xs text-muted-foreground">{"在请求头或参数中用 {{api_key}} 引用密钥"}</p>
              <Field label="请求参数"><Textarea aria-label="搜索请求参数" className="min-h-24 num text-xs" value={form.params || ""} disabled={busy} onChange={(e) => setForm({ ...form, params: e.target.value })} /></Field>
              <p className="text-xs text-muted-foreground">{"{{query}} 为搜索词；GET 使用查询参数，POST 使用请求正文"}</p>
              <Fold open={advanced} onToggle={() => setAdvanced(!advanced)}>接口格式</Fold>
              {advanced && <div className="flex flex-col gap-4">
                <Field label="请求头"><Textarea aria-label="搜索请求头" className="min-h-24 num text-xs" value={form.headers || ""} disabled={busy} placeholder='{"Authorization":"Bearer {{api_key}}"}' onChange={(e) => setForm({ ...form, headers: e.target.value })} /></Field>
                {form.method === "POST" && <Field label="正文格式"><Select value={form.encoding || "json"} onValueChange={(encoding) => setForm({ ...form, encoding })}><SelectTrigger aria-label="搜索正文格式"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="json">JSON</SelectItem><SelectItem value="form">表单</SelectItem></SelectContent></Select></Field>}
                <Field label="返回格式"><Select value={form.response_mode || "text"} onValueChange={(response_mode) => setForm({ ...form, response_mode })}><SelectTrigger aria-label="搜索返回格式"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="text">原始响应</SelectItem><SelectItem value="json">结构化结果</SelectItem></SelectContent></Select></Field>
                {form.response_mode === "json" && <>
                  <Field label="列表路径"><Input aria-label="搜索结果列表路径" value={form.results_path || ""} placeholder="results，留空读取整个响应" onChange={(e) => setForm({ ...form, results_path: e.target.value })} /></Field>
                  <Field label="标题字段"><Input aria-label="搜索结果标题字段" value={form.title_path || ""} onChange={(e) => setForm({ ...form, title_path: e.target.value })} /></Field>
                  <Field label="链接字段"><Input aria-label="搜索结果链接字段" value={form.url_path || ""} onChange={(e) => setForm({ ...form, url_path: e.target.value })} /></Field>
                  <Field label="摘要字段"><Input aria-label="搜索结果摘要字段" value={form.snippet_path || ""} onChange={(e) => setForm({ ...form, snippet_path: e.target.value })} /></Field>
                  <p className="text-xs text-muted-foreground">字段支持 data.results 等层级路径</p>
                </>}
              </div>}
              </>}
              {custom && <>
              <Fold withArrow={false}>出口代理</Fold>
              <ProxyFields form={form} setForm={setForm} hasAuth={Boolean(editing?.proxy_endpoint?.has_auth)} busy={busy} />
              </>}

              {editing?.kind === "github" && advanced && <><Fold open={testing} onToggle={() => setTesting(!testing)}>测试已保存配置</Fold>{testing && <GitHubTest key={editing.id} service={services.find((item) => item.id === editing.id) || editing} onUpdated={async () => { const r = await fetchJSON(ROOT); if (r.ok) setData(r.body); }} />}</>}
              {editing && provider && advanced && <><Fold open={testing} onToggle={() => setTesting(!testing)}>测试已保存配置</Fold>{testing && <AssetTest key={editing.id} defaultQuery={data?.tests?.[form.kind]?.query} service={services.find((item) => item.id === editing.id) || editing} onUpdated={async () => { const r = await fetchJSON(ROOT); if (r.ok) setData(r.body); }} />}</>}
              <label className="flex items-center gap-2 text-sm"><Checkbox checked={Boolean(form.enabled)} disabled={busy} onCheckedChange={(enabled) => setForm({ ...form, enabled: Boolean(enabled) })} />启用服务</label>
              <p className="text-xs text-muted-foreground">测试当前填写的配置，不保存；查询 {data?.tests?.[form.kind]?.query || "example.com"}{data?.tests?.[form.kind]?.size ? `，最多 ${data.tests[form.kind].size} 条` : ""}</p>
              {quickTest?.error && <p role="alert" className="text-xs text-status-danger">{quickTest.error}</p>}
              {quickTest?.result && <><p role="status" className="text-xs text-muted-foreground">测试成功</p><pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all num text-xs">{JSON.stringify(quickTest.result.result?.data ?? quickTest.result.result, null, 2)}</pre></>}
            </div>
            <DialogFooter className="shrink-0 border-t pt-4">
              <Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(undefined)}>返回</Button>
              <Button size="sm" variant="outline" disabled={busy} onClick={testDraft}>{busy ? "请求中" : "快速测试"}</Button>
              <Button size="sm" disabled={busy} onClick={save}>{busy ? "处理中" : "保存"}</Button>
            </DialogFooter>
          </>}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function AssetLimits({ form, setForm, provider, profiles, busy }) {
  const profile = profiles.find((item) => item.id === form.limit_plan) || profiles[0];
  const value = (name) => form.custom_limits ? form[name] || "" : name === "qps" ? (profile?.qps ? profile.qps * 0.9 : "") : profile?.[name] ? Math.floor(profile[name] * 0.9) : "";
  return <div className="flex flex-col gap-3">
    {profiles.length > 1 && <Field label="账号档位"><Select value={profile?.id} disabled={busy} onValueChange={(limit_plan) => setForm({ ...form, limit_plan })}><SelectTrigger aria-label="搜索账号档位"><SelectValue /></SelectTrigger><SelectContent>{profiles.map((item) => <SelectItem key={item.id} value={item.id}>{item.label}</SelectItem>)}</SelectContent></Select></Field>}
    <label className="flex items-center gap-2 text-sm"><Checkbox checked={Boolean(form.custom_limits)} disabled={busy} onCheckedChange={(enabled) => setForm({ ...form, custom_limits: Boolean(enabled), qps: value("qps"), per_minute: value("per_minute"), per_day: value("per_day") })} />自定额度</label>
    {[["每秒", "qps", 10000], ["每分钟", "per_minute", 10000], ["24小时", "per_day", 1000000]].map(([label, name, max]) => <Field key={name} label={label}><Input aria-label={`搜索额度 ${name}`} type="number" min={0} max={max} step={name === "qps" ? "any" : 1} value={value(name)} placeholder="不额外限制" disabled={busy || !form.custom_limits} onChange={(e) => setForm({ ...form, [name]: e.target.value })} /></Field>)}
    <Field label="每页条数"><Input aria-label="站点查询默认每页条数" type="number" min={1} max={provider.max_size} value={form.page_size || ""} disabled={busy || Boolean(provider.fixed_size)} onChange={(e) => setForm({ ...form, page_size: e.target.value })} /></Field>
    <p className="text-xs text-muted-foreground">{profile ? `默认按${profile.label}官方请求上限的 90% 设置` : "未查到公开统一请求上限，默认不额外限制"}</p>
    {profile && <><p className="text-xs text-muted-foreground">{profile.note}</p><a className="w-fit text-xs text-muted-foreground underline" href={profile.source} target="_blank" rel="noreferrer">额度依据</a></>}
    <p className="text-xs text-muted-foreground">所有任务及测试共用；空值仅表示本地不限额，官方限制仍生效</p>
    {Boolean(provider.fixed_size) && <p className="text-xs text-muted-foreground">Shodan 每页固定返回 100 条</p>}
  </div>;
}

function AssetWait({ quota }) {
  const at = new Date(quota?.retry_at || 0);
  return at.getTime() > Date.now() ? <p role="status" className="text-xs text-status-warning">查询暂停，{at.toLocaleString("zh-CN", { hour12: false })} 后可重试</p> : null;
}

function AssetTest({ service, onUpdated, defaultQuery }) {
  const [query, setQuery] = React.useState(defaultQuery || "1.1.1.1");
  const [page, setPage] = React.useState("1");
  const [options, setOptions] = React.useState("{}");
  const [result, setResult] = React.useState(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  async function run() {
    if (!query.trim()) { setError("填写查询语句"); return; }
    let extra;
    try { extra = JSON.parse(options); if (!extra || Array.isArray(extra) || typeof extra !== "object") throw new Error(); }
    catch { setError("附加参数须为 JSON 对象"); return; }
    setBusy(true); setResult(null); setError("");
    try {
      const r = await postJSON(`${ROOT}/${service.id}/assets`, { query, page: Number(page), options: extra });
      if (r.ok) setResult(r.body); else setError(r.body.error || "请求未完成");
    } catch { setError("网络未连通"); }
    finally { setBusy(false); try { await onUpdated(); } catch { /* Polling refreshes quota. */ } }
  }
  return <div className="flex flex-col gap-3">
    <p className="text-xs text-muted-foreground">使用已保存配置；查询可能消耗平台额度</p>
    <Field label="查询语句"><Textarea aria-label="搜索测试查询语句" value={query} disabled={busy} placeholder="填写该平台的查询语法" onChange={(e) => setQuery(e.target.value)} /></Field>
    <Field label="页码"><Input aria-label="搜索测试页码" type="number" min={1} max={1000000} value={page} disabled={busy} onChange={(e) => setPage(e.target.value)} /></Field>
    <Field label="附加参数"><Textarea aria-label="搜索测试附加参数" className="num text-xs" value={options} disabled={busy} onChange={(e) => setOptions(e.target.value)} /></Field>
    <Button className="w-fit" size="sm" variant="outline" disabled={busy || !service.available} onClick={run}>{busy ? "测试中" : "测试"}</Button>
    {error && <p role="alert" className="text-xs text-status-danger">{error}</p>}
    {result && <><p role="status" className="text-xs text-muted-foreground">请求成功 · 第 {result.page} 页 · 每页 {result.size} 条</p><pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all num text-xs">{JSON.stringify(result.data, null, 2)}</pre></>}
  </div>;
}

function ProxyFields({ form, setForm, hasAuth, busy }) {
  const [authOpen, setAuthOpen] = React.useState(false);
  function change(values) { setForm({ ...form, ...values, proxy_dirty: true }); }
  return <div className="flex flex-col gap-3">
    <Field label="协议"><Select value={form.proxy_protocol || "direct"} disabled={busy} onValueChange={(proxy_protocol) => change({ proxy_protocol, proxy_port: form.proxy_port || (proxy_protocol === "http" ? "80" : proxy_protocol === "https" ? "443" : "1080") })}>
      <SelectTrigger aria-label="搜索代理协议"><SelectValue /></SelectTrigger>
      <SelectContent><SelectItem value="direct">直连</SelectItem><SelectItem value="http">HTTP</SelectItem><SelectItem value="https">HTTPS</SelectItem><SelectItem value="socks4a">SOCKS4a</SelectItem><SelectItem value="socks5">SOCKS5</SelectItem></SelectContent>
    </Select></Field>
    {form.proxy_protocol !== "direct" && <>
      <Field label="地址"><div className="flex w-full min-w-0 gap-2">
        <Input className="min-w-0 flex-1" aria-label="搜索代理地址" value={form.proxy_host || ""} placeholder="127.0.0.1" disabled={busy} onChange={(e) => change({ proxy_host: e.target.value })} />
        <Input className="w-24 shrink-0 num" aria-label="搜索代理端口" type="number" min={1} max={65535} value={form.proxy_port || ""} placeholder="端口" disabled={busy} onChange={(e) => change({ proxy_port: e.target.value })} />
      </div></Field>
      <Fold open={authOpen} onToggle={() => setAuthOpen(!authOpen)}>代理认证{hasAuth && !form.proxy_clear_auth ? " · 已配置" : " · 可选"}</Fold>
      {authOpen && <>
        <Field label="用户名"><Input aria-label="搜索代理用户名" autoComplete="off" value={form.proxy_username || ""} disabled={busy || form.proxy_clear_auth} placeholder={hasAuth ? "留空保留" : "可选"} onChange={(e) => change({ proxy_username: e.target.value })} /></Field>
        {form.proxy_protocol !== "socks4a" && <Field label="密码"><Input aria-label="搜索代理密码" autoComplete="off" type="password" value={form.proxy_password || ""} disabled={busy || form.proxy_clear_auth} placeholder={hasAuth ? "留空保留" : "可选"} onChange={(e) => change({ proxy_password: e.target.value })} /></Field>}
        {hasAuth && <label className="flex items-center gap-2 text-sm"><Checkbox checked={Boolean(form.proxy_clear_auth)} disabled={busy} onCheckedChange={(value) => change({ proxy_clear_auth: Boolean(value), proxy_username: "", proxy_password: "" })} />清除已保存认证</label>}
      </>}
      {form.proxy_protocol === "socks4a" && <p className="text-xs text-muted-foreground">SOCKS4a 支持用户名，不支持密码</p>}
    </>}
  </div>;
}

function GitHubWait({ quota }) {
  const waits = [["仓库搜索", quota?.search_retry_at, quota?.search_official], ["代码搜索", quota?.code_retry_at, quota?.code_official], ["目录与文件读取", quota?.read_retry_at, quota?.read_official]];
  return waits.map(([label, value, official]) => {
    const at = new Date(value || 0);
    return at.getTime() > Date.now() ? <p key={label} role="status" className="text-xs text-status-warning">{label}：{official ? "GitHub 限流" : "达到本地上限"}，{at.toLocaleString("zh-CN", { hour12: false })} 后可重试</p> : null;
  });
}

function GitHubLimits({ form, setForm, hasKey, busy }) {
  const authenticated = !form.clear_key && Boolean(form.api_key?.trim() || hasKey);
  const minute = form.custom_limits ? form.per_minute : authenticated ? 27 : 9;
  const hour = form.custom_limits ? form.per_hour : authenticated ? 4500 : 54;
  const codeMinute = form.custom_limits ? form.code_per_minute : 9;
  return <div className="flex flex-col gap-3">
    <label className="flex items-center gap-2 text-sm"><Checkbox disabled={busy} checked={Boolean(form.custom_limits)} onCheckedChange={(value) => setForm({ ...form, custom_limits: Boolean(value), per_minute: minute, code_per_minute: codeMinute, per_hour: hour })} />自定额度</label>
    <Field label="仓库/分钟"><Input aria-label="GitHub 每分钟搜索上限" type="number" min={1} max={10000} disabled={busy || !form.custom_limits} value={minute} onChange={(e) => setForm({ ...form, per_minute: e.target.value })} /></Field>
    <Field label="代码/分钟"><Input aria-label="GitHub 每分钟代码搜索上限" type="number" min={1} max={10000} disabled={busy || !form.custom_limits} value={codeMinute} onChange={(e) => setForm({ ...form, code_per_minute: e.target.value })} /></Field>
    <Field label="读取/小时"><Input aria-label="GitHub 每小时读取上限" type="number" min={1} max={100000} disabled={busy || !form.custom_limits} value={hour} onChange={(e) => setForm({ ...form, per_hour: e.target.value })} /></Field>
    <p className="text-xs text-muted-foreground">默认取官方额度的 90%；所有任务及测试共用</p>
    <p className="text-xs text-muted-foreground">计数仅含本系统调用；匿名额度由出口 IP 共用，官方限流优先</p>
  </div>;
}

function GitHubTest({ service, onUpdated }) {
  const [action, setAction] = React.useState("search");
  const [query, setQuery] = React.useState("Hello-World");
  const [repo, setRepo] = React.useState("octocat/Hello-World");
  const [path, setPath] = React.useState("README");
  const [result, setResult] = React.useState(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  async function run() {
    setBusy(true); setError(""); setResult(null);
    try {
      const r = await postJSON(`${ROOT}/${service.id}/github`, { action, query, repo, path: action === "read" ? path : "" });
      if (r.ok) setResult(r.body); else setError(r.body.error || "请求未完成");
    } catch { setError("网络未连通"); } finally { setBusy(false); try { await onUpdated(); } catch { /* Polling refreshes quota if this refresh fails. */ } }
  }
  return <div className="flex flex-col gap-3 border-t pt-3">
    <p className="text-xs text-muted-foreground">使用已保存配置；测试计入请求额度</p>
    <Field label="测试功能"><Select disabled={busy} value={action} onValueChange={setAction}><SelectTrigger aria-label="GitHub 测试功能"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="search">搜索仓库</SelectItem><SelectItem value="search_code">搜索代码</SelectItem><SelectItem value="browse">浏览目录</SelectItem><SelectItem value="read">读取文件</SelectItem></SelectContent></Select></Field>
    {action === "search_code" && <p className="text-xs text-muted-foreground">代码搜索需要 Token</p>}
    {action === "search" || action === "search_code" ? <Field label="搜索词"><Input aria-label="GitHub 测试搜索词" value={query} onChange={(e) => setQuery(e.target.value)} /></Field> : <>
      <Field label="仓库"><Input aria-label="GitHub 测试仓库" value={repo} onChange={(e) => setRepo(e.target.value)} /></Field>
      {action === "read" && <Field label="文件路径"><Input aria-label="GitHub 测试文件路径" value={path} onChange={(e) => setPath(e.target.value)} /></Field>}
    </>}
    <Button className="w-fit" size="sm" variant="outline" disabled={busy || !service.enabled} onClick={run}>{busy ? "测试中" : "测试"}</Button>
    {error && <p role="alert" className="text-xs text-status-danger">{error}</p>}
    {result && <><p role="status" className="text-xs text-muted-foreground">请求成功 · 仓库 {result.quota?.minute_used}/{result.quota?.per_minute} · 代码 {result.quota?.code_minute_used}/{result.quota?.code_per_minute} 次/分钟 · 读取 {result.quota?.hour_used}/{result.quota?.per_hour} 次/小时</p><pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all num text-xs">{JSON.stringify(result.data, null, 2)}</pre></>}
  </div>;
}
