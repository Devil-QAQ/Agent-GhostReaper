"use client";

import * as React from "react";
import { EmptyState } from "@/components/empty-state";
import { iconOf } from "@/components/icons";
import { NodeSheetHost } from "@/views/node-sheets";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { SearchInput } from "@/components/search-input";
import { usePolling } from "@/lib/api";
import { useViewportHeight } from "@/lib/use-viewport-height";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PanoramaEngine, samePanoramaChain } from "@/lib/panorama-engine";

function PanoSearch({ tid, onLocate }) {
 const [q,setQ]=React.useState(""),[hits,setHits]=React.useState([]),[open,setOpen]=React.useState(false),[pending,setPending]=React.useState(false),[failed,setFailed]=React.useState(false);
 React.useEffect(()=>{const ctrl=new AbortController();if(!q.trim()){setHits([]);setOpen(false);setPending(false);return()=>ctrl.abort();}setPending(true);setFailed(false);const timer=setTimeout(async()=>{try{const r=await fetch(`/api/tasks/${tid}/pview/search?q=${encodeURIComponent(q.trim())}`,{signal:ctrl.signal});if(!r.ok)throw new Error("搜索失败");const rows=await r.json();if(ctrl.signal.aborted)return;setHits(Array.isArray(rows)?rows:[]);setOpen(true);}catch(e){if(!ctrl.signal.aborted){setHits([]);setFailed(true);setOpen(true);}}finally{if(!ctrl.signal.aborted)setPending(false);}},120);return()=>{clearTimeout(timer);ctrl.abort();};},[q,tid]);
 const choose=(h)=>{setOpen(false);onLocate(h);};
 return <div className="relative w-48 max-w-full"><SearchInput aria-label="搜索节点" placeholder="搜索地址、名称或编号" value={q} onChange={e=>setQ(e.target.value)} onFocus={()=>q.trim()&&setOpen(true)} onBlur={()=>setOpen(false)} onKeyDown={e=>{if(e.key==="Escape")setOpen(false);if(e.key==="Enter"&&hits[0]){e.preventDefault();choose(hits[0]);}}}/>{open&&<div className="absolute top-9 left-0 z-20 max-h-72 w-80 max-w-[calc(100vw-48px)] overflow-auto rounded-xl border border-border/55 bg-popover p-1 shadow-lg" role="list" aria-label="搜索结果">{pending?<div className="px-3 py-2 text-xs text-muted-foreground">搜索中</div>:hits.length?hits.map(h=><button key={h.id} className="flex w-full flex-col gap-1 rounded-lg px-3 py-2 text-left text-xs hover:bg-hover-lift" onPointerDown={e=>e.preventDefault()} onClick={()=>choose(h)}><span className="break-words">{h.ti}</span><span className="num text-muted-foreground">{h.id}</span>{h.match&&<span className="break-words text-muted-foreground">{h.match}</span>}</button>):<div className="px-3 py-2 text-xs text-muted-foreground">{failed?"搜索失败":"没有匹配节点"}</div>}</div>}</div>;
}

async function panoFocusView(tid, id, eng, branch) {
 const query = new URLSearchParams({width:String(Math.round(eng.w)),height:String(Math.round(eng.h))});
 if(branch?.source)query.set("source",branch.source);
 if(branch?.target)query.set("target",branch.target);
 const response = await fetch(`/api/tasks/${tid}/pview/links/${encodeURIComponent(id)}?${query}`);
 if (!response.ok) { const error=new Error("关联读取失败");error.status=response.status;throw error; }
 return response.json();
}

function PanoBranches({ view, nodes, branch, pending, onChange, onAll }) {
 const byID=new Map(nodes?.map(n=>[n.id,n])||[]);
 const options=(ids)=>(ids||[]).filter(id=>byID.has(id));
 const sources=options(view?.sources),targets=options(view?.targets),selected=Boolean(branch.source||branch.target);
 const chooser=(label,ids,key)=>ids.length>1&&<div className="flex items-center gap-2"><span className="text-xs text-muted-foreground">{label}</span><Select value={branch[key]||"all"} disabled={pending} onValueChange={value=>onChange({...branch,[key]:value==="all"?"":value})}><SelectTrigger aria-label={`${label}分支`} className="h-8 w-44 text-xs"><SelectValue/></SelectTrigger><SelectContent className="max-w-sm"><SelectItem value="all">全部{label}（{ids.length}）</SelectItem>{ids.map(id=>{const n=byID.get(id),title=(n.t==="step"?`${id} `:"")+n.ti;return <SelectItem key={id} value={id} title={title}><span className="block max-w-xs truncate text-xs">{title}</span></SelectItem>;})}</SelectContent></Select></div>;
 return <>{chooser("来源",sources,"source")}{chooser("去向",targets,"target")}{selected&&<Button variant="outline" size="sm" className="h-8 text-xs" onClick={onAll}>全部关联</Button>}</>;
}

export function PanoramaPage({ currentTask, focus }) {
 return <PanoramaTask key={currentTask?.id || "none"} currentTask={currentTask} focus={focus}/>;
}

function PanoramaTask({ currentTask, focus }) {
 const viewportRef=useViewportHeight();
 const tid=currentTask?.id,canvasRef=React.useRef(null),mmRef=React.useRef(null),engineRef=React.useRef(null),requestRef=React.useRef(0),initialCameraRef=React.useRef(null);
 const historyRef=React.useRef([]),branchRef=React.useRef({source:"",target:""}),viewRef=React.useRef(null),selectionRef=React.useRef(null),navigationRef=React.useRef(null);
 const [sheet,setSheet]=React.useState(null),[hover,setHover]=React.useState(null),[focused,setFocused]=React.useState(null),[viewport,setViewport]=React.useState({coverage:100,position:.5});
 const [branch,setBranch]=React.useState(branchRef.current),[view,setView]=React.useState(null),[depth,setDepth]=React.useState(0),[pending,setPending]=React.useState(false),[focusError,setFocusError]=React.useState("");
 const {data,error}=usePolling(tid?`/api/tasks/${tid}/pview/skeleton`:"/api/healthz",30000,{immediate:true,live:tid?[`pview:${tid}`]:null});
 const chooseBranch=React.useCallback(next=>{branchRef.current=next;setBranch(next);},[]);
 const rememberView=React.useCallback(next=>{viewRef.current=next;setView(next);},[]);
 const openSheet=React.useCallback(target=>{if(!target?.id)return;setHover(null);setSheet({type:target.type,id:target.id,panorama:target.type==="step",resultIDs:target.type==="step"?viewRef.current?.results?.[target.id]?.map(f=>f.id):undefined});},[]);
 const refreshFocus=React.useCallback(async(preserveCamera=false)=>{
  const eng=engineRef.current,id=eng?.focus;if(!id)return;
  const token=++requestRef.current,selection={...branchRef.current};setPending(true);setFocusError("");
  try{
   let next;
   try{next=await panoFocusView(tid,selectionRef.current||id,eng,selection);}catch(error){
    if(error.status!==400||!(selection.source||selection.target))throw error;
    if(token!==requestRef.current||eng!==engineRef.current||eng.focus!==id)return;
    chooseBranch({source:"",target:""});next=await panoFocusView(tid,selectionRef.current||id,eng);
   }
   if(token!==requestRef.current||eng!==engineRef.current||eng.focus!==id)return;
   const navigation=navigationRef.current;
   eng.reconcileFocus(next,preserveCamera&&!navigation,navigation?.camera||null);
   if(navigation?.record&&navigation.previous.id&&!navigation.previous.source&&!navigation.previous.target&&!branchRef.current.source&&!branchRef.current.target&&samePanoramaChain(navigation.beforeView,next)&&historyRef.current?.at(-1)===navigation.previous)historyRef.current.pop();
   navigationRef.current=null;rememberView(next);setDepth((historyRef.current?.length||0));setPending(false);
  }catch{
   if(token!==requestRef.current||eng!==engineRef.current||eng.focus!==id)return;
   const navigation=navigationRef.current;
   if(navigation?.previous.id===id){selectionRef.current=navigation.previous.ref;chooseBranch({source:navigation.previous.source,target:navigation.previous.target});if(navigation.record&&historyRef.current?.at(-1)===navigation.previous)historyRef.current.pop();}
   else if(eng.focusPending)eng.reconcileFocus({node:{id},ids:[...(eng.active||[])]});
   eng.resumeArrivals();navigationRef.current=null;setDepth((historyRef.current?.length||0));setPending(false);setFocusError("读取关联失败");
  }
 },[tid,chooseBranch,rememberView]);
 const navigate=React.useCallback((target,record=true)=>{
  const eng=engineRef.current;if(!eng)return;
  const id=eng.resolve(target.id);if(!id)return;
  const source=target.source||"",next=target.target||"";
  if(record&&id===eng.focus&&source===branchRef.current.source&&next===branchRef.current.target&&(target.ref||target.id)===selectionRef.current)return;
  const inFlight=navigationRef.current,transient=record&&inFlight?.record;
  const previous=transient?{...inFlight.previous,camera:{...eng.cam}}:{id:eng.focus,ref:selectionRef.current,source:branchRef.current.source,target:branchRef.current.target,camera:{...eng.cam}};
  const beforeView=transient?inFlight.beforeView:viewRef.current;
  if(transient&&historyRef.current?.at(-1)===inFlight.previous)historyRef.current.pop();
  const changed=id!==previous.id||(target.ref||target.id)!==previous.ref||source!==previous.source||next!==previous.target,remember=record&&changed;
  if(remember)historyRef.current.push(previous);
  navigationRef.current={previous,beforeView,record:remember,camera:target.camera};
  setDepth((historyRef.current?.length||0));setHover(null);setSheet(null);
  selectionRef.current=target.ref||target.id;chooseBranch({source,target:next});
  if(id!==eng.focus){rememberView(null);eng.enterFocus({id});}
  refreshFocus();
 },[chooseBranch,rememberView,refreshFocus]);
 const enter=React.useCallback(n=>navigate({id:n.id,ref:n.ref}),[navigate]);
 const back=React.useCallback(()=>{
  const eng=engineRef.current;if(!eng?.focus)return;
  const previous=historyRef.current.pop();setHover(null);setSheet(null);setDepth((historyRef.current?.length||0));
  if(previous?.id){navigate(previous,false);return;}
  requestRef.current++;navigationRef.current=null;selectionRef.current=null;setPending(false);setFocusError("");chooseBranch({source:"",target:""});rememberView(null);eng.exitFocus(previous?.camera);
 },[navigate,chooseBranch,rememberView]);
 const leave=React.useCallback(()=>{
  historyRef.current=[];
  back();
 },[back]);
 const allRelated=React.useCallback(()=>{
  const id=engineRef.current?.focus;if(!id)return;
  let index=-1;for(let i=(historyRef.current?.length||0)-1;i>=0;i--){const old=historyRef.current[i];if(old.id===id&&!old.source&&!old.target){index=i;break;}}
  if(index>=0){const previous=historyRef.current[index];historyRef.current.splice(index);setDepth((historyRef.current?.length||0));navigate(previous,false);}
  else navigate({id,ref:selectionRef.current},false);
 },[navigate]);
 const ready=Boolean(data?.nodes?.length);
 React.useEffect(()=>{setSheet(null);setHover(null);setFocused(null);requestRef.current++;initialCameraRef.current=null;historyRef.current=[];navigationRef.current=null;setDepth(0);setPending(false);setFocusError("");chooseBranch({source:"",target:""});rememberView(null);},[tid,chooseBranch,rememberView]);
 React.useEffect(()=>{if(!ready||!canvasRef.current)return;const eng=new PanoramaEngine(canvasRef.current,mmRef.current,{onNodeClick:enter,onOpenDetail:openSheet,onBackgroundClick:leave,onHover:(n,p)=>setHover(n?{title:n.ti,id:n.id,...p}:null),onFocus:setFocused,onViewport:setViewport});engineRef.current=eng;const mm=mmRef.current,onJump=e=>eng.minimapJump(e);mm?.addEventListener("click",onJump);return()=>{mm?.removeEventListener("click",onJump);eng.destroy();if(engineRef.current===eng)engineRef.current=null;};},[ready,tid,enter,openSheet,leave]);
 React.useEffect(()=>{const eng=engineRef.current;if(!data||!eng)return;eng.setData(data);if(!initialCameraRef.current&&eng.fitted)initialCameraRef.current={...eng.cam};if(eng.focus)refreshFocus(true);else{historyRef.current=[];navigationRef.current=null;setDepth(0);chooseBranch({source:"",target:""});rememberView(null);}},[data,ready,tid,refreshFocus,chooseBranch,rememberView]);
 React.useEffect(()=>{if(focus&&data&&engineRef.current)enter({id:focus});},[focus,ready,enter]);
 const nodes=data?.nodes||[],selectedIDs=focused&&view?new Set(view.ids||[]):null;
 const visibleNodes=selectedIDs?nodes.filter(n=>selectedIDs.has(n.id)):nodes;
 const facts=new Set(visibleNodes.flatMap(n=>((selectedIDs?view?.results?.[n.id]:n.results)||[]).map(f=>f.id))).size;
 const reset=(all)=>{requestRef.current++;navigationRef.current=null;selectionRef.current=null;historyRef.current=[];setDepth(0);setPending(false);setFocusError("");chooseBranch({source:"",target:""});rememberView(null);setHover(null);setSheet(null);all?engineRef.current?.fitAll():engineRef.current?.exitFocus(initialCameraRef.current);};
 return <div ref={viewportRef} className="flex min-h-[420px] min-w-0 flex-col gap-3" data-viewport-panel="panorama"><PageHeader title="全景图" subtitle={focused?"点节点切换关联 · 点空白退出聚合":"拖动平移 · 点节点看关联"}>
 <div className="flex min-w-0 flex-wrap items-center gap-2"><PanoSearch tid={tid} onLocate={enter}/><Button variant="outline" size="sm" className="h-8 text-xs" onClick={()=>reset(false)}>初始视野</Button><Button variant="outline" size="sm" className="h-8 text-xs" onClick={()=>reset(true)}>查看全景</Button>{focused&&<><Button variant="outline" size="sm" className="h-8 text-xs" onClick={back}>上一步</Button>{view&&<PanoBranches view={view} nodes={nodes} branch={branch} pending={pending} onChange={next=>navigate({id:focused,ref:selectionRef.current,...next})} onAll={allRelated}/>}</>}{focusError&&<span className="flex items-center gap-2 text-xs text-muted-foreground" role="status">{focusError}<Button variant="ghost" size="sm" className="h-8 text-xs" onClick={()=>refreshFocus()}>重试</Button></span>}</div></PageHeader>
 <div className="relative min-h-0 flex-1 overflow-hidden rounded-2xl bg-background shadow-sm" data-panorama-state={focused?"focus":"overview"} data-panorama-depth={depth}>
 {!data?<div className="absolute inset-0 flex flex-col gap-4 p-6"><div className="h-12 w-48 animate-pulse rounded-xl bg-card"/><div className="h-12 w-80 animate-pulse rounded-xl bg-card"/><div className="h-12 w-64 animate-pulse rounded-xl bg-card"/>{error&&<p className="text-xs text-muted-foreground">读取全景图失败</p>}</div>:!nodes.length?<EmptyState icon="globe" title="暂无资产与意图" hint="任务产生资产、意图或漏洞后显示"/>:<>
 <canvas ref={canvasRef} className="absolute inset-0 size-full touch-none" aria-label={focused&&view?`任务全景图 · ${view.node?.id} · ${view.ids?.length||0} 个节点`:"任务全景图"}/>
 <div aria-label="全景图图例" className="pointer-events-none absolute left-3 top-3 z-10 flex max-w-[calc(100%-24px)] flex-wrap items-center gap-x-3 gap-y-1 rounded-lg bg-background/85 px-3 py-2 text-xs text-muted-foreground backdrop-blur-sm">
   <span><span className="text-status-success">■</span>{" "}资产</span>
   <span><span className="text-pano-intent">●</span>{" "}意图／事实</span>
   <span>漏洞：<span className="text-severity-critical">Critical</span>{" · "}<span className="text-severity-high">High</span>{" · "}<span className="text-severity-medium">Medium</span>{" · "}<span className="text-severity-low">Low</span></span>
   <span>箭头：来源 → 产出</span>
 </div>
 {hover&&<div className="pointer-events-none absolute left-3 bottom-3 z-10 max-w-sm rounded-lg bg-popover px-3 py-2 text-xs shadow-lg"><div className="break-words">{hover.title}</div><div className="num text-muted-foreground">{hover.id}</div></div>}
 <div className="absolute right-3 bottom-3 z-10 flex flex-col items-end gap-2"><div className="flex gap-1 rounded-xl bg-card p-1 shadow-sm"><Button variant="ghost" size="sm" className="h-8 px-2" aria-label="缩小" onClick={()=>engineRef.current?.zoomBy(1/1.3)}>{React.createElement(iconOf("minus"),{className:"size-3.5"})}</Button><Button variant="ghost" size="sm" className="h-8 px-2" aria-label="放大" onClick={()=>engineRef.current?.zoomBy(1.3)}>{React.createElement(iconOf("plus"),{className:"size-3.5"})}</Button></div><canvas ref={mmRef} className="h-24 w-40 cursor-pointer rounded-lg border border-border/55 bg-card" aria-label="全景图小地图"/></div>
 </>}
 </div>
 <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground" data-panorama-footer>
 <span>{selectedIDs?"当前关联：":""}{visibleNodes.filter(n=>n.t==="asset").length} 个资产 · {visibleNodes.filter(n=>n.t==="step").length} 个意图 · {facts} 个事实 · {visibleNodes.filter(n=>n.t==="claim").length} 个漏洞</span>
 <div className="flex min-w-0 max-w-full items-center gap-2"><label htmlFor="pano-position" className="shrink-0">横向位置</label><input id="pano-position" type="range" min="0" max="1000" value={Math.round(viewport.position*1000)} onChange={e=>engineRef.current?.setHorizontal(Number(e.target.value)/1000)} className="w-28 min-w-0 accent-primary"/><span className="num shrink-0">可见 {viewport.coverage}%</span></div>
 </div>
 <NodeSheetHost tid={tid} value={sheet} onChange={setSheet} dismissAll/>
 </div>;
}
